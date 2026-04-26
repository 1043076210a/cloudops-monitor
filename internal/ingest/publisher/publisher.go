package publisher

import "context"

type MetricsEvent struct {
	MachineID string
	IP        string
	CPUUsage  float64
	MemUsage  float64
	DiskUsage float64
	Timestamp int64
}

// Publisher is the phase-3 decoupling contract.
// HTTP handler can publish to queue while workers consume and write downstream.
type Publisher interface {
	PublishMetrics(ctx context.Context, event MetricsEvent) error
}
