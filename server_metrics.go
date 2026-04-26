package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

var (
	serverIngestRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_server_ingest_requests_total",
			Help: "Total number of metrics ingest requests handled by the server.",
		},
		[]string{"path", "mode", "status"},
	)
	serverKafkaPublishTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_server_kafka_publish_total",
			Help: "Total number of Kafka publish attempts from the server.",
		},
		[]string{"topic", "status"},
	)
	serverVMWritesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_server_vm_writes_total",
			Help: "Total number of VictoriaMetrics write attempts from the server.",
		},
		[]string{"status"},
	)
	serverVMWriteDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cloudops_server_vm_write_duration_seconds",
			Help:    "VictoriaMetrics write latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
	serverMetricsProcessDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cloudops_server_metrics_process_duration_seconds",
			Help:    "End-to-end server metrics processing duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
	serverAlertNotificationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_server_alert_notifications_total",
			Help: "Total number of alert notification attempts from the server.",
		},
		[]string{"level", "status"},
	)
	serverAlertEventsSavedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_server_alert_events_saved_total",
			Help: "Total number of alert event persistence attempts.",
		},
		[]string{"level", "status"},
	)
)

func init() {
	prometheus.MustRegister(
		serverIngestRequestsTotal,
		serverKafkaPublishTotal,
		serverVMWritesTotal,
		serverVMWriteDurationSeconds,
		serverMetricsProcessDurationSeconds,
		serverAlertNotificationsTotal,
		serverAlertEventsSavedTotal,
	)
}

func observeDuration(start time.Time, observer prometheus.Observer) {
	observer.Observe(time.Since(start).Seconds())
}

func saveAlertEventObserved(db *gorm.DB, record AlertEventRecord) error {
	err := saveAlertEvent(db, record)
	if err != nil {
		serverAlertEventsSavedTotal.WithLabelValues(record.Level, "failed").Inc()
		return err
	}
	serverAlertEventsSavedTotal.WithLabelValues(record.Level, "success").Inc()
	return nil
}
