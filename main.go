package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloud-ops-monitor/internal/ingest/publisher"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/gorm"
)

// Machine 机器模型
type Machine struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	MachineID string `json:"machine_id" gorm:"size:128;uniqueIndex"`
	IP        string `json:"ip" gorm:"index"`
	Hostname  string `json:"hostname"`
	Status    string `json:"status" gorm:"default:active"`
}

type MachineRegisterPayload struct {
	MachineID string `json:"machine_id"`
	IP        string `json:"ip"`
	Hostname  string `json:"hostname"`
	Status    string `json:"status"`
}

type MetricsPayload struct {
	MachineID string  `json:"machine_id"`
	IP        string  `json:"ip"`
	CPUUsage  float64 `json:"cpu_usage"`
	MemUsage  float64 `json:"mem_usage"`
	DiskUsage float64 `json:"disk_usage"`
	Timestamp int64   `json:"timestamp"`
}

func main() {
	cfg := loadConfig()

	tracingShutdown, err := initTracing(context.Background(), cfg)
	if err != nil {
		log.Fatal("初始化 OTel tracing 失败:", err)
	}
	defer func() {
		if err := tracingShutdown(context.Background()); err != nil {
			log.Printf("关闭 OTel tracing 失败: %v", err)
		}
	}()

	db, err := openMySQLWithRetry(cfg)
	if err != nil {
		log.Fatal("数据库连接失败:", err)
	}

	// 自动建表
	db.AutoMigrate(&Machine{}, &AlertEventRecord{})
	log.Println("数据库初始化成功")

	r := gin.Default()
	if cfg.OTelEnabled {
		r.Use(otelgin.Middleware(cfg.OTelServiceName))
	}
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 健康检查
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
			"status":  "running",
		})
	})
	registerHealthRoutes(r, db, cfg)

	// 注册机器接口
	r.POST("/api/v1/machines/register", func(c *gin.Context) {
		var req MachineRegisterPayload
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.MachineID) == "" {
			c.JSON(400, gin.H{"error": "machine_id 不能为空"})
			return
		}

		if strings.TrimSpace(req.Status) == "" {
			req.Status = "active"
		}

		var machine Machine
		err := db.Where("machine_id = ?", req.MachineID).First(&machine).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		if err == gorm.ErrRecordNotFound {
			machine = Machine{
				MachineID: req.MachineID,
				IP:        req.IP,
				Hostname:  req.Hostname,
				Status:    req.Status,
			}
			if createErr := db.Create(&machine).Error; createErr != nil {
				c.JSON(500, gin.H{"error": createErr.Error()})
				return
			}
			c.JSON(200, gin.H{"registered": true, "updated": false, "machine": machine})
			return
		}

		updates := map[string]interface{}{
			"ip":       req.IP,
			"hostname": req.Hostname,
			"status":   req.Status,
		}
		if updateErr := db.Model(&machine).Updates(updates).Error; updateErr != nil {
			c.JSON(500, gin.H{"error": updateErr.Error()})
			return
		}

		if readErr := db.First(&machine, machine.ID).Error; readErr != nil {
			c.JSON(500, gin.H{"error": readErr.Error()})
			return
		}

		c.JSON(200, gin.H{"registered": true, "updated": true, "machine": machine})
	})

	// 最近告警事件查询（MVP版）
	r.GET("/api/v1/alerts/recent", func(c *gin.Context) {
		limit := 20
		if v := strings.TrimSpace(c.Query("limit")); v != "" {
			if parsed, parseErr := strconv.Atoi(v); parseErr == nil {
				if parsed > 0 && parsed <= 100 {
					limit = parsed
				}
			}
		}

		machineID := strings.TrimSpace(c.Query("machine_id"))
		level := strings.TrimSpace(c.Query("level"))
		notifyStatus := strings.TrimSpace(c.Query("notify_status"))

		var events []AlertEventRecord
		query := db.Model(&AlertEventRecord{})
		if machineID != "" {
			query = query.Where("machine_id = ?", machineID)
		}
		if level != "" {
			query = query.Where("level = ?", level)
		}
		if notifyStatus != "" {
			query = query.Where("notify_status = ?", notifyStatus)
		}

		if err := query.Order("triggered_at DESC").Limit(limit).Find(&events).Error; err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{
			"count": len(events),
			"filters": gin.H{
				"machine_id":    machineID,
				"level":         level,
				"notify_status": notifyStatus,
				"limit":         limit,
			},
			"events": events,
		})
	})

	alertStore, err := initAlertStore(cfg)
	if err != nil {
		log.Fatal("初始化告警状态存储失败:", err)
	}
	notifier := initNotifier(cfg)

	victoriaClient := &http.Client{Timeout: 3 * time.Second}

	var metricsPublisher publisher.Publisher
	if strings.EqualFold(cfg.IngestMode, "kafka") {
		kp := publisher.NewKafkaPublisher(cfg.KafkaBrokers, cfg.KafkaTopic)
		metricsPublisher = kp
		defer kp.Close()
		logKV("info", "ingest_mode_enabled", map[string]any{"mode": "kafka", "topic": cfg.KafkaTopic, "brokers": cfg.KafkaBrokers})
	} else {
		logKV("info", "ingest_mode_enabled", map[string]any{"mode": "direct"})
	}

	// Worker 专用处理入口：该入口永远走“直接处理”，防止 kafka 模式下递归回写队列
	r.POST("/api/v1/metrics/process", func(c *gin.Context) {
		var metrics MetricsPayload
		if err := c.ShouldBindJSON(&metrics); err != nil {
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics/process", "process", "bad_request").Inc()
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(metrics.MachineID) == "" {
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics/process", "process", "bad_request").Inc()
			c.JSON(400, gin.H{"error": "machine_id 不能为空"})
			return
		}
		if metrics.Timestamp <= 0 {
			metrics.Timestamp = time.Now().Unix()
		}

		if err := processMetricsPayload(c.Request.Context(), victoriaClient, db, alertStore, notifier, cfg, metrics); err != nil {
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics/process", "process", "error").Inc()
			c.JSON(500, gin.H{"status": "error", "message": err.Error()})
			return
		}
		serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics/process", "process", "success").Inc()
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.POST("/api/v1/metrics", func(c *gin.Context) {
		var metrics MetricsPayload

		if err := c.ShouldBindJSON(&metrics); err != nil {
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", cfg.IngestMode, "bad_request").Inc()
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(metrics.MachineID) == "" {
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", cfg.IngestMode, "bad_request").Inc()
			c.JSON(400, gin.H{"error": "machine_id 不能为空"})
			return
		}
		if metrics.Timestamp <= 0 {
			metrics.Timestamp = time.Now().Unix()
		}

		if strings.EqualFold(cfg.IngestMode, "kafka") {
			if metricsPublisher == nil {
				serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", "kafka", "error").Inc()
				c.JSON(500, gin.H{"status": "error", "message": "kafka publisher not initialized"})
				return
			}
			publishCtx, publishSpan := otel.Tracer("cloud-ops-monitor/server").Start(c.Request.Context(), "publish_metrics_kafka")
			publishSpan.SetAttributes(
				attribute.String("machine_id", metrics.MachineID),
				attribute.String("messaging.system", "kafka"),
				attribute.String("messaging.destination.name", cfg.KafkaTopic),
			)
			event := publisher.MetricsEvent{
				MachineID: metrics.MachineID,
				IP:        metrics.IP,
				CPUUsage:  metrics.CPUUsage,
				MemUsage:  metrics.MemUsage,
				DiskUsage: metrics.DiskUsage,
				Timestamp: metrics.Timestamp,
			}
			if err := metricsPublisher.PublishMetrics(publishCtx, event); err != nil {
				recordSpanError(publishCtx, err)
				publishSpan.End()
				serverKafkaPublishTotal.WithLabelValues(cfg.KafkaTopic, "error").Inc()
				serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", "kafka", "error").Inc()
				logKV("error", "kafka_publish_failed", map[string]any{"machine_id": metrics.MachineID, "err": err.Error()})
				c.JSON(500, gin.H{"status": "error", "message": "kafka publish failed"})
				return
			}
			publishSpan.End()
			serverKafkaPublishTotal.WithLabelValues(cfg.KafkaTopic, "success").Inc()
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", "kafka", "accepted").Inc()
			logKV("info", "metrics_queued", map[string]any{"machine_id": metrics.MachineID, "topic": cfg.KafkaTopic})
			c.JSON(202, gin.H{"status": "accepted", "mode": "kafka"})
			return
		}

		if err := processMetricsPayload(c.Request.Context(), victoriaClient, db, alertStore, notifier, cfg, metrics); err != nil {
			serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", "direct", "error").Inc()
			c.JSON(500, gin.H{"status": "error", "message": err.Error()})
			return
		}

		serverIngestRequestsTotal.WithLabelValues("/api/v1/metrics", "direct", "success").Inc()
		c.JSON(200, gin.H{"status": "ok"})
	})

	log.Printf("⚡ Server is listening on port %s...", cfg.HTTPPort)
	r.Run(":" + cfg.HTTPPort)
}

func processMetricsPayload(ctx context.Context, victoriaClient *http.Client, db *gorm.DB, alertStore AlertStateStore, notifier Notifier, cfg AppConfig, metrics MetricsPayload) error {
	processStarted := time.Now()
	defer observeDuration(processStarted, serverMetricsProcessDurationSeconds)

	tracer := otel.Tracer("cloud-ops-monitor/server")
	ctx, span := tracer.Start(ctx, "process_metrics_payload")
	defer span.End()
	span.SetAttributes(
		attribute.String("machine_id", metrics.MachineID),
		attribute.String("ip", metrics.IP),
		attribute.Float64("cpu_usage", metrics.CPUUsage),
		attribute.Float64("mem_usage", metrics.MemUsage),
		attribute.Float64("disk_usage", metrics.DiskUsage),
	)

	payload := fmt.Sprintf(`host_cpu_usage{machine_id="%s",ip="%s"} %.2f %d
host_mem_usage{machine_id="%s",ip="%s"} %.2f %d
host_disk_usage{machine_id="%s",ip="%s"} %.2f %d`,
		metrics.MachineID, metrics.IP, metrics.CPUUsage, metrics.Timestamp*1000,
		metrics.MachineID, metrics.IP, metrics.MemUsage, metrics.Timestamp*1000,
		metrics.MachineID, metrics.IP, metrics.DiskUsage, metrics.Timestamp*1000)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.VictoriaMetricsURL, strings.NewReader(payload))
	if err != nil {
		recordSpanError(ctx, err)
		serverVMWritesTotal.WithLabelValues("build_request_error").Inc()
		return fmt.Errorf("构造VM请求失败")
	}
	req.Header.Set("Content-Type", "text/plain")
	vmCtx, vmSpan := tracer.Start(ctx, "write_victoriametrics")

	vmStarted := time.Now()
	resp, err := victoriaClient.Do(req)
	observeDuration(vmStarted, serverVMWriteDurationSeconds)
	if err != nil {
		recordSpanError(vmCtx, err)
		vmSpan.End()
		serverVMWritesTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("写入VM失败")
	}
	vmSpan.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	vmSpan.End()
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logKV("error", "vm_non_2xx", map[string]any{"status": resp.StatusCode, "body": string(body)})
		serverVMWritesTotal.WithLabelValues("non_2xx").Inc()
		return fmt.Errorf("VM写入返回失败")
	}

	serverVMWritesTotal.WithLabelValues("success").Inc()
	logKV("info", "metrics_ingested", map[string]any{
		"machine_id": metrics.MachineID,
		"ip":         metrics.IP,
		"cpu":        metrics.CPUUsage,
		"mem":        metrics.MemUsage,
		"disk":       metrics.DiskUsage,
	})

	evaluateCPUAlert(ctx, db, alertStore, notifier, cfg, metrics)
	return nil
}

func initAlertStore(cfg AppConfig) (AlertStateStore, error) {
	if strings.EqualFold(cfg.AlertStoreType, "memory") {
		log.Println("ℹ️ ALERT_STORE=memory，使用内存告警状态存储")
		return NewMemoryAlertStateStore(), nil
	}

	store, err := NewRedisAlertStateStore(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Printf("⚠️ Redis不可用，降级为内存模式: %v", err)
		return NewMemoryAlertStateStore(), nil
	}
	log.Printf("✅ Redis告警状态存储已启用: %s db=%d", cfg.RedisAddr, cfg.RedisDB)
	return store, nil
}

func evaluateCPUAlert(ctx context.Context, db *gorm.DB, store AlertStateStore, notifier Notifier, cfg AppConfig, m MetricsPayload) {
	tracer := otel.Tracer("cloud-ops-monitor/server")
	ctx, span := tracer.Start(ctx, "evaluate_cpu_alert")
	defer span.End()
	span.SetAttributes(
		attribute.String("machine_id", m.MachineID),
		attribute.Float64("cpu_usage", m.CPUUsage),
		attribute.Float64("cpu_threshold", cfg.CPUAlertThreshold),
	)

	if m.CPUUsage > cfg.CPUAlertThreshold {
		count, err := store.IncrementCPUBreach(ctx, m.MachineID)
		if err != nil {
			recordSpanError(ctx, err)
			log.Printf("❌ 更新告警状态失败: machine=%s err=%v", m.MachineID, err)
			return
		}

		logKV("warn", "cpu_threshold_breach", map[string]any{
			"machine_id":   m.MachineID,
			"current_cpu":  m.CPUUsage,
			"breach_count": count,
		})

		if count >= cfg.AlertTriggerCount {
			cooldown := time.Duration(cfg.AlertCooldownSeconds) * time.Second
			acquired, err := store.TryAcquireCooldown(ctx, m.MachineID, cooldown)
			if err != nil {
				recordSpanError(ctx, err)
				log.Printf("❌ 告警收敛检查失败: machine=%s err=%v", m.MachineID, err)
				return
			}
			if acquired {
				log.Printf("🚨 [P0告警] 节点=%s ip=%s 原因=CPU连续%d次超过%.1f%% 当前CPU=%.1f%% 冷却=%ds",
					m.MachineID, m.IP, cfg.AlertTriggerCount, cfg.CPUAlertThreshold, m.CPUUsage, cfg.AlertCooldownSeconds)
				event := AlertEvent{
					Level:         "P0",
					MachineID:     m.MachineID,
					IP:            m.IP,
					CurrentCPU:    m.CPUUsage,
					ThresholdCPU:  cfg.CPUAlertThreshold,
					TriggerCount:  cfg.AlertTriggerCount,
					CooldownSec:   cfg.AlertCooldownSeconds,
					TriggeredAt:   time.Now(),
					SourceService: "cloud-ops-monitor",
				}
				notifyStatus := "sent"
				notifyError := ""
				if notifyErr := notifier.NotifyCPUAlert(ctx, event); notifyErr != nil {
					recordSpanError(ctx, notifyErr)
					serverAlertNotificationsTotal.WithLabelValues(event.Level, "failed").Inc()
					log.Printf("❌ 告警通知发送失败: machine=%s err=%v", m.MachineID, notifyErr)
					notifyStatus = "failed"
					notifyError = notifyErr.Error()
				} else {
					serverAlertNotificationsTotal.WithLabelValues(event.Level, "sent").Inc()
					logKV("info", "alert_notify_sent", map[string]any{"machine_id": m.MachineID, "level": event.Level})
				}

				record := AlertEventRecord{
					MachineID:      m.MachineID,
					IP:             m.IP,
					Level:          event.Level,
					Metric:         "cpu_usage",
					ThresholdValue: cfg.CPUAlertThreshold,
					CurrentValue:   m.CPUUsage,
					TriggerCount:   cfg.AlertTriggerCount,
					CooldownSec:    cfg.AlertCooldownSeconds,
					NotifyStatus:   notifyStatus,
					NotifyError:    notifyError,
					TriggeredAt:    event.TriggeredAt,
				}
				if saveErr := saveAlertEventObserved(db, record); saveErr != nil {
					log.Printf("❌ 告警事件落库失败: machine=%s err=%v", m.MachineID, saveErr)
				} else {
					log.Printf("🧾 告警事件已落库: machine=%s metric=cpu_usage", m.MachineID)
				}
			} else {
				log.Printf("🔕 [告警收敛] 节点=%s 告警冷却中，跳过重复发送", m.MachineID)
			}
		}
		return
	}

	prevCount, err := store.GetCPUBreachCount(ctx, m.MachineID)
	if err != nil {
		recordSpanError(ctx, err)
		log.Printf("❌ 读取告警状态失败: machine=%s err=%v", m.MachineID, err)
		return
	}

	if err := store.ResetCPUBreach(ctx, m.MachineID); err != nil {
		recordSpanError(ctx, err)
		log.Printf("❌ 重置告警状态失败: machine=%s err=%v", m.MachineID, err)
		return
	}

	if prevCount > 0 {
		logKV("info", "cpu_recovered", map[string]any{"machine_id": m.MachineID, "current_cpu": m.CPUUsage, "prev_breach_count": prevCount})

		recoveryEvent := AlertEvent{
			Level:         "RECOVERY",
			MachineID:     m.MachineID,
			IP:            m.IP,
			CurrentCPU:    m.CPUUsage,
			ThresholdCPU:  cfg.CPUAlertThreshold,
			TriggerCount:  prevCount,
			CooldownSec:   cfg.AlertCooldownSeconds,
			TriggeredAt:   time.Now(),
			SourceService: "cloud-ops-monitor",
		}
		if recoveryNotifyErr := notifier.NotifyCPURecovery(ctx, recoveryEvent); recoveryNotifyErr != nil {
			recordSpanError(ctx, recoveryNotifyErr)
			log.Printf("❌ 恢复通知发送失败: machine=%s err=%v", m.MachineID, recoveryNotifyErr)
		}
		record := AlertEventRecord{
			MachineID:      m.MachineID,
			IP:             m.IP,
			Level:          "RECOVERY",
			Metric:         "cpu_usage",
			ThresholdValue: cfg.CPUAlertThreshold,
			CurrentValue:   m.CPUUsage,
			TriggerCount:   prevCount,
			CooldownSec:    cfg.AlertCooldownSeconds,
			NotifyStatus:   "sent",
			NotifyError:    "",
			TriggeredAt:    recoveryEvent.TriggeredAt,
		}
		if saveErr := saveAlertEventObserved(db, record); saveErr != nil {
			log.Printf("❌ 恢复事件落库失败: machine=%s err=%v", m.MachineID, saveErr)
		} else {
			log.Printf("🧾 恢复事件已落库: machine=%s metric=cpu_usage", m.MachineID)
		}
	}
}
