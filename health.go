package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type depStatus struct {
	Name      string `json:"name"`
	Up        bool   `json:"up"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func registerHealthRoutes(r *gin.Engine, db *gorm.DB, cfg AppConfig) {
	r.GET("/healthz/deps", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		mysql := checkMySQL(ctx, db)
		redis := checkRedis(ctx, cfg)
		vm := checkVictoriaMetrics(ctx, cfg)

		allUp := mysql.Up && redis.Up && vm.Up
		statusCode := http.StatusOK
		if !allUp {
			statusCode = http.StatusServiceUnavailable
		}

		c.JSON(statusCode, gin.H{
			"status": map[bool]string{true: "up", false: "degraded"}[allUp],
			"deps":   []depStatus{mysql, redis, vm},
		})
	})
}

func checkMySQL(ctx context.Context, db *gorm.DB) depStatus {
	start := time.Now()
	out := depStatus{Name: "mysql", Up: true}

	sqlDB, err := db.DB()
	if err != nil {
		out.Up = false
		out.Error = err.Error()
		out.LatencyMS = time.Since(start).Milliseconds()
		return out
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		out.Up = false
		out.Error = err.Error()
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	return out
}

func checkRedis(ctx context.Context, cfg AppConfig) depStatus {
	start := time.Now()
	out := depStatus{Name: "redis", Up: true}

	if strings.EqualFold(cfg.AlertStoreType, "memory") {
		out.Error = "ALERT_STORE=memory"
		out.LatencyMS = time.Since(start).Milliseconds()
		return out
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer client.Close()

	if err := client.Ping(ctx).Err(); err != nil {
		out.Up = false
		out.Error = err.Error()
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	return out
}

func checkVictoriaMetrics(ctx context.Context, cfg AppConfig) depStatus {
	start := time.Now()
	out := depStatus{Name: "victoriametrics", Up: true}

	healthURL, err := buildVMHealthURL(cfg.VictoriaMetricsURL)
	if err != nil {
		out.Up = false
		out.Error = err.Error()
		out.LatencyMS = time.Since(start).Milliseconds()
		return out
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		out.Up = false
		out.Error = err.Error()
		out.LatencyMS = time.Since(start).Milliseconds()
		return out
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Up = false
		out.Error = "status=" + resp.Status
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	return out
}

func buildVMHealthURL(vmIngestURL string) (string, error) {
	u, err := url.Parse(vmIngestURL)
	if err != nil {
		return "", err
	}
	u.Path = "/health"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
