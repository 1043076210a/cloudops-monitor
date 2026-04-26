package main

import (
	"os"
	"strconv"
)

type AppConfig struct {
	MySQLDSN             string
	MySQLConnectRetries  int
	MySQLConnectInterval int
	VictoriaMetricsURL   string
	HTTPPort             string
	CPUAlertThreshold    float64
	AlertTriggerCount    int
	AlertCooldownSeconds int

	AlertStoreType string
	RedisAddr      string
	RedisPassword  string
	RedisDB        int

	NotifierType         string
	WebhookProvider      string
	WebhookURL           string
	NotifyTimeoutSeconds int

	IngestMode             string
	KafkaBrokers           string
	KafkaTopic             string
	KafkaGroupID           string
	WorkerProcessTargetURL string

	OTelEnabled              bool
	OTelServiceName          string
	OTelExporterOTLPEndpoint string
}

func loadConfig() AppConfig {
	cfg := AppConfig{
		MySQLDSN:               getEnv("MYSQL_DSN", "cloudops:cloudops@tcp(127.0.0.1:3306)/cloud_ops_monitor?charset=utf8mb4&parseTime=True&loc=Local"),
		MySQLConnectRetries:    getEnvAsInt("MYSQL_CONNECT_RETRIES", 30),
		MySQLConnectInterval:   getEnvAsInt("MYSQL_CONNECT_RETRY_INTERVAL_SECONDS", 2),
		VictoriaMetricsURL:     getEnv("VICTORIA_METRICS_URL", "http://localhost:8428/api/v1/import/prometheus"),
		HTTPPort:               getEnv("HTTP_PORT", "8080"),
		CPUAlertThreshold:      getEnvAsFloat("CPU_ALERT_THRESHOLD", 80.0),
		AlertTriggerCount:      getEnvAsInt("ALERT_TRIGGER_COUNT", 3),
		AlertCooldownSeconds:   getEnvAsInt("ALERT_COOLDOWN_SECONDS", 60),
		AlertStoreType:         getEnv("ALERT_STORE", "redis"),
		RedisAddr:              getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:          getEnv("REDIS_PASSWORD", ""),
		RedisDB:                getEnvAsInt("REDIS_DB", 0),
		NotifierType:           getEnv("NOTIFIER_TYPE", "noop"),
		WebhookProvider:        getEnv("WEBHOOK_PROVIDER", "feishu"),
		WebhookURL:             getEnv("WEBHOOK_URL", ""),
		NotifyTimeoutSeconds:   getEnvAsInt("NOTIFY_TIMEOUT_SECONDS", 5),
		IngestMode:             getEnv("INGEST_MODE", "direct"),
		KafkaBrokers:           getEnv("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:             getEnv("KAFKA_TOPIC", "cloudops.metrics"),
		KafkaGroupID:           getEnv("KAFKA_GROUP_ID", "cloudops-worker"),
		WorkerProcessTargetURL: getEnv("WORKER_PROCESS_TARGET_URL", "http://localhost:8080/api/v1/metrics/process"),
		OTelEnabled:            getEnvAsBool("OTEL_ENABLED", false),
		OTelServiceName:        getEnv("OTEL_SERVICE_NAME", "cloudops-server"),
		OTelExporterOTLPEndpoint: getEnv(
			"OTEL_EXPORTER_OTLP_ENDPOINT",
			"localhost:4317",
		),
	}
	return cfg
}

func getEnv(key, defaultValue string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	v := getEnv(key, "")
	if v == "" {
		return defaultValue
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return i
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	v := getEnv(key, "")
	if v == "" {
		return defaultValue
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultValue
	}
	return f
}

func getEnvAsBool(key string, defaultValue bool) bool {
	v := getEnv(key, "")
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}
