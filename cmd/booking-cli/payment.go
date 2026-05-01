package main

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application"
	paymentApp "booking_monitor/internal/application/payment"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/messaging"
	paymentInfra "booking_monitor/internal/infrastructure/payment"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runPaymentWorker is the `payment` subcommand entry. Runs the Kafka
// order.created consumer as its own process so payment compensation can
// scale / restart independently of the API server. stdlog.Fatalf on
// config-load failure because the app logger is constructed downstream
// of config and isn't available yet.
func runPaymentWorker(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),
		fx.Provide(
			fx.Annotate(paymentInfra.NewMockGateway, fx.As(new(domain.PaymentGateway))),
			paymentApp.NewService,
			func(cfg *config.Config, logger *mlog.Logger) *messaging.KafkaConsumer {
				return messaging.NewKafkaConsumer(&cfg.Kafka, logger)
			},
		),
		fx.Invoke(installPaymentWorker),
	)
	app.Run()
}

// installPaymentWorker wires the tracer + Kafka consumer into the fx
// lifecycle. Previously the payment process had NO tracer, which broke
// trace-context continuity coming off the order.created topic.
func installPaymentWorker(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	cfg *config.Config,
	consumer *messaging.KafkaConsumer,
	service application.PaymentService,
	logger *mlog.Logger,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installPaymentWorker: %w", err)
	}

	// Worker metrics listener (Phase 2 checkpoint O3). Without this
	// the kafka_consumer_retry_total + payment-side counters never
	// reach Prometheus. Listener is independent of the consumer
	// goroutine — its failure does NOT escalate via shutdowner.
	if err := bootstrap.InstallMetricsListener(lc, cfg, logger); err != nil {
		return fmt.Errorf("installPaymentWorker: metrics listener: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			logger.L().Info("Starting Payment Service Worker...")
			go func() {
				if err := consumer.Start(runCtx, service); err != nil && !errors.Is(err, context.Canceled) {
					logger.L().Error("Payment consumer stopped with error", tag.Error(err))
					// Shutdowner.Shutdown() returns an error only in lifecycle
					// races (called before Done or after Stop) — both are
					// non-actionable: the fatal cause was just logged, and
					// normal fx lifecycle will take over regardless. This
					// pattern repeats at every goroutine-level fatal
					// escalation in this file; the `_` discard is deliberate.
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.L().Info("Stopping Payment Service Worker...")
			cancel()
			if err := consumer.Close(); err != nil {
				logger.L().Error("Kafka consumer close error", tag.Error(err))
			}
			shutdownErr := tp.Shutdown(ctx)
			if shutdownErr != nil {
				logger.L().Error("tracer shutdown error", tag.Error(shutdownErr))
			}
			_ = logger.Sync() // zap Sync on stderr returns an OS-specific error; swallow.
			return shutdownErr
		},
	})
	return nil
}
