package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

type metricsEvent struct {
	MachineID string  `json:"MachineID"`
	IP        string  `json:"IP"`
	CPUUsage  float64 `json:"CPUUsage"`
	MemUsage  float64 `json:"MemUsage"`
	DiskUsage float64 `json:"DiskUsage"`
	Timestamp int64   `json:"Timestamp"`
}

func main() {
	brokers := getEnv("KAFKA_BROKERS", "localhost:9092")
	topic := getEnv("KAFKA_TOPIC", "cloudops.metrics")
	groupID := getEnv("KAFKA_GROUP_ID", "cloudops-worker")
	processURL := getEnv("WORKER_PROCESS_TARGET_URL", "http://localhost:8080/api/v1/metrics/process")
	otelEnabled := getEnvAsBool("OTEL_ENABLED", false)
	otelServiceName := getEnv("OTEL_SERVICE_NAME", "cloudops-worker")
	otelEndpoint := getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	metricsPort := getEnv("WORKER_METRICS_PORT", "9091")

	tracingShutdown, err := initTracing(context.Background(), otelEnabled, otelServiceName, otelEndpoint)
	if err != nil {
		logKV("error", "worker_otel_init_failed", map[string]any{"err": err.Error()})
		os.Exit(1)
	}
	defer func() {
		if err := tracingShutdown(context.Background()); err != nil {
			logKV("error", "worker_otel_shutdown_failed", map[string]any{"err": err.Error()})
		}
	}()

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  strings.Split(brokers, ","),
		Topic:    topic,
		GroupID:  groupID,
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	defer reader.Close()

	startWorkerMetricsServer(metricsPort)

	httpClient := &http.Client{Timeout: 5 * time.Second}
	ctx := context.Background()

	logKV("info", "worker_started", map[string]any{"topic": topic, "brokers": brokers, "target": processURL})

	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			workerKafkaMessagesTotal.WithLabelValues(topic, "read_error").Inc()
			logKV("error", "worker_read_message_failed", map[string]any{"err": err.Error()})
			time.Sleep(1 * time.Second)
			continue
		}
		workerKafkaMessagesTotal.WithLabelValues(topic, "read").Inc()
		processingStarted := time.Now()

		msgCtx := extractTraceContext(ctx, msg.Headers)
		msgCtx, span := otel.Tracer("cloud-ops-monitor/worker").Start(msgCtx, "worker_process_metrics")
		span.SetAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination.name", topic),
			attribute.String("messaging.kafka.message.key", string(msg.Key)),
			attribute.Int64("messaging.kafka.offset", msg.Offset),
			attribute.Int("messaging.kafka.partition", msg.Partition),
		)

		var event metricsEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			observeWorkerDuration(processingStarted, workerProcessingDurationSeconds)
			workerKafkaMessagesTotal.WithLabelValues(topic, "decode_error").Inc()
			recordSpanError(msgCtx, err)
			span.End()
			logKV("error", "worker_decode_message_failed", map[string]any{"err": err.Error()})
			continue
		}
		span.SetAttributes(attribute.String("machine_id", event.MachineID))

		body, _ := json.Marshal(map[string]any{
			"machine_id": event.MachineID,
			"ip":         event.IP,
			"cpu_usage":  event.CPUUsage,
			"mem_usage":  event.MemUsage,
			"disk_usage": event.DiskUsage,
			"timestamp":  event.Timestamp,
		})

		forwardCtx, forwardSpan := otel.Tracer("cloud-ops-monitor/worker").Start(msgCtx, "worker_forward_metrics")
		req, err := http.NewRequestWithContext(forwardCtx, http.MethodPost, processURL, bytes.NewBuffer(body))
		if err != nil {
			observeWorkerDuration(processingStarted, workerProcessingDurationSeconds)
			workerForwardTotal.WithLabelValues("build_request_error").Inc()
			recordSpanError(forwardCtx, err)
			forwardSpan.End()
			span.End()
			logKV("error", "worker_build_forward_request_failed", map[string]any{"machine_id": event.MachineID, "err": err.Error()})
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		otel.GetTextMapPropagator().Inject(forwardCtx, propagation.HeaderCarrier(req.Header))

		forwardStarted := time.Now()
		resp, err := httpClient.Do(req)
		observeWorkerDuration(forwardStarted, workerForwardDurationSeconds)
		if err != nil {
			observeWorkerDuration(processingStarted, workerProcessingDurationSeconds)
			workerForwardTotal.WithLabelValues("error").Inc()
			recordSpanError(forwardCtx, err)
			forwardSpan.End()
			span.End()
			logKV("error", "worker_forward_failed", map[string]any{"machine_id": event.MachineID, "err": err.Error()})
			continue
		}
		_ = resp.Body.Close()
		forwardSpan.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
		forwardSpan.End()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := fmt.Errorf("worker forward returned status %d", resp.StatusCode)
			observeWorkerDuration(processingStarted, workerProcessingDurationSeconds)
			workerForwardTotal.WithLabelValues("non_2xx").Inc()
			recordSpanError(msgCtx, err)
			span.End()
			logKV("error", "worker_forward_non_2xx", map[string]any{"machine_id": event.MachineID, "status": resp.StatusCode})
			continue
		}

		workerForwardTotal.WithLabelValues("success").Inc()
		observeWorkerDuration(processingStarted, workerProcessingDurationSeconds)
		span.End()
		logKV("info", "worker_forwarded_metrics", map[string]any{"machine_id": event.MachineID})
	}
}

func startWorkerMetricsServer(port string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	go func() {
		logKV("info", "worker_metrics_server_started", map[string]any{"port": port})
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logKV("error", "worker_metrics_server_failed", map[string]any{"err": err.Error()})
		}
	}()
}

func getEnv(key, defaultValue string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return defaultValue
	}
	return v
}

func getEnvAsBool(key string, defaultValue bool) bool {
	v := getEnv(key, "")
	if v == "" {
		return defaultValue
	}
	switch strings.ToLower(v) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return defaultValue
	}
}

func initTracing(ctx context.Context, enabled bool, serviceName, endpoint string) (func(context.Context) error, error) {
	if !enabled {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logKV("info", "worker_otel_tracing_enabled", map[string]any{
		"service":  serviceName,
		"endpoint": endpoint,
	})

	return func(shutdownCtx context.Context) error {
		ctx, cancel := context.WithTimeout(shutdownCtx, 5*time.Second)
		defer cancel()
		return provider.Shutdown(ctx)
	}, nil
}

func extractTraceContext(ctx context.Context, headers []kafka.Header) context.Context {
	carrier := propagation.MapCarrier{}
	for _, header := range headers {
		carrier.Set(header.Key, string(header.Value))
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func recordSpanError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func logKV(level, event string, fields map[string]any) {
	payload := map[string]any{
		"level": level,
		"event": event,
		"ts":    time.Now().Format(time.RFC3339),
	}
	for k, v := range fields {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("{\"level\":\"error\",\"event\":\"worker_log_marshal_failed\",\"err\":%q}", err.Error())
		return
	}
	log.Println(string(b))
}
