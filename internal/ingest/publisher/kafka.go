package publisher

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

type KafkaPublisher struct {
	writer *kafka.Writer
}

func NewKafkaPublisher(brokersCSV, topic string) *KafkaPublisher {
	brokers := make([]string, 0)
	for _, b := range strings.Split(brokersCSV, ",") {
		v := strings.TrimSpace(b)
		if v != "" {
			brokers = append(brokers, v)
		}
	}
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: 10 * time.Millisecond,
	}
	return &KafkaPublisher{writer: w}
}

func (k *KafkaPublisher) PublishMetrics(ctx context.Context, event MetricsEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	msg := kafka.Message{
		Key:     []byte(event.MachineID),
		Value:   payload,
		Time:    time.Now(),
		Headers: traceHeadersFromContext(ctx),
	}
	return k.writer.WriteMessages(ctx, msg)
}

func (k *KafkaPublisher) Close() error {
	return k.writer.Close()
}

func traceHeadersFromContext(ctx context.Context) []kafka.Header {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	headers := make([]kafka.Header, 0, len(carrier))
	for key, value := range carrier {
		headers = append(headers, kafka.Header{
			Key:   key,
			Value: []byte(value),
		})
	}
	return headers
}
