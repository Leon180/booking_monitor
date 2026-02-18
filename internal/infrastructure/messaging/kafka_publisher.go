package messaging

import (
	"context"
	"time"

	"booking_monitor/internal/domain"

	"github.com/segmentio/kafka-go"
)

type kafkaPublisher struct {
	writer *kafka.Writer
}

// NewKafkaPublisher creates a new Kafka-backed EventPublisher.
func NewKafkaPublisher(cfg MessagingConfig) domain.EventPublisher {
	timeout := cfg.WriteTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	w := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Balancer:               &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		WriteTimeout:           timeout,
	}
	return &kafkaPublisher{writer: w}
}

func (p *kafkaPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	return p.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Value: payload,
	})
}

func (p *kafkaPublisher) Close() error {
	return p.writer.Close()
}
