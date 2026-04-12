package messaging

import (
	"context"
	"fmt"
	"time"

	"booking_monitor/internal/domain"

	"github.com/segmentio/kafka-go"
)

// kafkaCloseTimeout bounds how long kafka.Writer.Close() is allowed to
// block flushing in-flight batches during shutdown. The underlying
// kafka-go Writer does not accept a ctx on Close(), so we enforce the
// timeout with a done-channel race. Without this bound, fx OnStop
// could hang indefinitely on a broker that's slow to ack the final
// flush.
const kafkaCloseTimeout = 10 * time.Second

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

// Close flushes the writer but gives up after kafkaCloseTimeout so the
// shutdown path can never block forever on a slow broker.
//
// Note: if the timeout fires, the background goroutine calling
// p.writer.Close() is leaked — kafka-go's Writer.Close() has no
// cancellation hook. This is a single-goroutine, shutdown-only leak
// that cannot be fixed without upstream support (or switching to a
// writer that accepts context on Close). Acceptable trade-off: the
// process is terminating anyway.
func (p *kafkaPublisher) Close() error {
	done := make(chan error, 1)
	go func() { done <- p.writer.Close() }()

	select {
	case err := <-done:
		return err
	case <-time.After(kafkaCloseTimeout):
		return fmt.Errorf("kafkaPublisher.Close: timed out after %s", kafkaCloseTimeout)
	}
}
