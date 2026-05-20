package sse

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"

	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Default subscriber configuration. Override via SubscriberConfig.
//
// MaxConsecFailures = 30 with BlockTimeout = 5s means ~2.5min of
// sustained Redis failure before fx.Shutdown is escalated. Matches
// the Stage 5 drift detector pattern.
const (
	DefaultSubscriberBlockTimeout  = 5 * time.Second
	DefaultSubscriberBackoffBase   = 200 * time.Millisecond
	DefaultSubscriberBackoffMax    = 5 * time.Second
	DefaultSubscriberMaxConsecFail = 30
)

// SubscriberConfig is the tunable surface for the Redis Stream
// subscriber. See `docs/design/admin_event_streaming.md` § Q11.
type SubscriberConfig struct {
	StreamKey        string
	BlockTimeout     time.Duration
	BackoffBase      time.Duration
	BackoffMax       time.Duration
	MaxConsecFailures int
}

// DefaultSubscriberConfig returns the production defaults.
func DefaultSubscriberConfig(streamKey string) SubscriberConfig {
	return SubscriberConfig{
		StreamKey:        streamKey,
		BlockTimeout:     DefaultSubscriberBlockTimeout,
		BackoffBase:      DefaultSubscriberBackoffBase,
		BackoffMax:       DefaultSubscriberBackoffMax,
		MaxConsecFailures: DefaultSubscriberMaxConsecFail,
	}
}

// Subscriber reads the admin event Redis Stream via XREAD and pushes
// each message to the Hub for fan-out. One goroutine per pod (Q9
// fan-out architecture). Single-point-of-failure mitigation: consec
// failure threshold → fx.Shutdown(ExitCode(1)) so k8s restarts the
// pod (Q11 design).
type Subscriber struct {
	client     *redis.Client
	hub        *Hub
	shutdowner fx.Shutdowner
	cfg        SubscriberConfig
	log        *mlog.Logger

	// done is closed when Run() returns.
	done chan struct{}
}

// NewSubscriber constructs an unstarted Subscriber.
func NewSubscriber(client *redis.Client, hub *Hub, shutdowner fx.Shutdowner, cfg SubscriberConfig, logger *mlog.Logger) *Subscriber {
	applySubscriberDefaults(&cfg)
	return &Subscriber{
		client:     client,
		hub:        hub,
		shutdowner: shutdowner,
		cfg:        cfg,
		log:        logger.With(mlog.String("component", "admin_stream_subscriber")),
		done:       make(chan struct{}),
	}
}

// Done returns a channel closed when Run() exits.
func (s *Subscriber) Done() <-chan struct{} { return s.done }

// Run is the XREAD loop. Blocks until ctx is cancelled. Errors:
//   - redis.Nil: idle XREAD timeout, no events. Reset consec counter.
//   - context cancelled: clean exit.
//   - other: increment counter, exponential backoff, escalate
//     fx.Shutdown after MaxConsecFailures.
func (s *Subscriber) Run(ctx context.Context) {
	defer close(s.done)

	s.log.Info(ctx, "admin stream subscriber starting",
		mlog.String("stream_key", s.cfg.StreamKey),
		mlog.Duration("block_timeout", s.cfg.BlockTimeout),
		mlog.Int("max_consec_failures", s.cfg.MaxConsecFailures))

	// HIGH-3 (review round 1): consecFails is read/written only by
	// this single goroutine — atomic was misleading; use plain int32.
	var consecFails int32
	lastID := "$"

	for {
		if ctx.Err() != nil {
			s.log.Info(context.Background(), "admin stream subscriber stopping")
			return
		}

		msgs, err := s.client.XRead(ctx, &redis.XReadArgs{
			Streams: []string{s.cfg.StreamKey, lastID},
			Block:   s.cfg.BlockTimeout,
			Count:   100,
		}).Result()

		// 1. Idle timeout — XREAD BLOCK returned no events. Normal.
		if errors.Is(err, redis.Nil) {
			consecFails = 0
			observability.AdminStreamSubscriberConsecFailures.Set(0)
			continue
		}

		// 2. Ctx cancelled — exit
		if ctx.Err() != nil {
			return
		}

		// 3. Real failure
		if err != nil {
			consecFails++
			n := consecFails
			observability.AdminStreamSubscriberConsecFailures.Set(float64(n))
			observability.AdminStreamSubscriberXReadFailuresTotal.Inc()
			s.log.Error(ctx, "admin stream subscriber XREAD failed",
				tag.Error(err),
				mlog.Int("consec_failures", int(n)))

			if int(n) >= s.cfg.MaxConsecFailures {
				s.log.Error(ctx, "admin stream subscriber exceeded consec-failure budget — escalating fx.Shutdown",
					mlog.Int("consec_failures", int(n)))
				// MED-5 (review round 1): log shutdown errors so a
				// degraded fx state doesn't go silent.
				if shutdownErr := s.shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
					s.log.Error(ctx, "admin stream subscriber: fx.Shutdown escalation failed",
						tag.Error(shutdownErr))
				}
				return
			}

			// Exponential backoff, capped.
			backoff := s.cfg.BackoffBase << min(int(n-1), 5)
			if backoff > s.cfg.BackoffMax {
				backoff = s.cfg.BackoffMax
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}

		// 4. Happy path — got messages, broadcast each
		consecFails = 0
		observability.AdminStreamSubscriberConsecFailures.Set(0)
		for _, stream := range msgs {
			for _, msg := range stream.Messages {
				if !s.hub.Broadcast(ctx, msg) {
					// ctx cancelled or hub stopped mid-broadcast
					return
				}
				lastID = msg.ID
			}
		}
	}
}

func applySubscriberDefaults(cfg *SubscriberConfig) {
	if cfg.BlockTimeout == 0 {
		cfg.BlockTimeout = DefaultSubscriberBlockTimeout
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = DefaultSubscriberBackoffBase
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = DefaultSubscriberBackoffMax
	}
	if cfg.MaxConsecFailures == 0 {
		cfg.MaxConsecFailures = DefaultSubscriberMaxConsecFail
	}
}
