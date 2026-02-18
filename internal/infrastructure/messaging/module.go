package messaging

import (
	"context"
	"strings"
	"time"

	"booking_monitor/internal/domain"

	"go.uber.org/fx"
)

// MessagingConfig holds Kafka configuration.
type MessagingConfig struct {
	Brokers      []string
	WriteTimeout time.Duration
}

// Module provides the Kafka EventPublisher as an Fx dependency and registers
// an OnStop hook to cleanly flush and close the Kafka writer on shutdown.
var Module = fx.Module("messaging",
	fx.Provide(func(lc fx.Lifecycle, cfg MessagingConfig) domain.EventPublisher {
		pub := NewKafkaPublisher(cfg)
		lc.Append(fx.Hook{
			OnStop: func(_ context.Context) error {
				return pub.Close()
			},
		})
		return pub
	}),
)

// ParseBrokers splits a comma-separated broker string into a slice.
func ParseBrokers(raw string) []string {
	if raw == "" {
		return []string{"localhost:9092"}
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			result = append(result, s)
		}
	}
	return result
}
