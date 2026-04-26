package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	workerKafkaMessagesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_worker_kafka_messages_total",
			Help: "Total number of Kafka messages read by the worker.",
		},
		[]string{"topic", "status"},
	)
	workerForwardTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloudops_worker_forward_total",
			Help: "Total number of worker forward attempts to the server process endpoint.",
		},
		[]string{"status"},
	)
	workerForwardDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cloudops_worker_forward_duration_seconds",
			Help:    "Worker HTTP forward latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
	workerProcessingDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "cloudops_worker_processing_duration_seconds",
			Help:    "End-to-end worker message processing duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	prometheus.MustRegister(
		workerKafkaMessagesTotal,
		workerForwardTotal,
		workerForwardDurationSeconds,
		workerProcessingDurationSeconds,
	)
}

func observeWorkerDuration(start time.Time, observer prometheus.Observer) {
	observer.Observe(time.Since(start).Seconds())
}
