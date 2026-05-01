package messaging

import (
	"context"
	"time"

	"booking_monitor/internal/application"

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
	fx.Provide(func(lc fx.Lifecycle, cfg MessagingConfig) application.EventPublisher {
		pub := NewKafkaPublisher(cfg)
		lc.Append(fx.Hook{
			OnStop: func(_ context.Context) error {
				return pub.Close()
			},
		})
		return pub
	}),
)
