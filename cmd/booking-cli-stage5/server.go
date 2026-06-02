package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/admin"
	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/event"
	"booking_monitor/internal/application/expiry"
	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/sse"
	"booking_monitor/internal/infrastructure/api/stagehttp"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/messaging"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	fx.New(
		bootstrap.CommonModule(cfg),
		cache.BaseModule,

		// Admin SSE event stream — wires bus + hub + subscriber + handler
		// + JWT middleware + lifecycle hooks. The bus is fx-injected into
		// the worker.MessageProcessor below so post-commit order.created
		// events fan-out to the war-room dashboard. PR #121's infrastructure
		// reused; Stage 5 binary previously stubbed this with NoopBus.
		bootstrap.AdminStreamModule,

		// One *redisInventoryRepository instance satisfies both interfaces
		// (DeductInventoryNoStream is on Stage5InventoryRepository only).
		fx.Provide(
			fx.Annotate(
				cache.NewStage5RedisInventoryRepository,
				fx.As(new(domain.InventoryRepository)),
				fx.As(new(domain.Stage5InventoryRepository)),
			),
		),

		// Booking intake chain: metrics → publisher → service → worker → consumer.
		//
		// PR #129 A2: Stage 5 wraps NewKafkaIntakeService with the same
		// metrics decorator that internal/application/module.go applies
		// for the main binary, so `bookings_total{status="success"}` etc.
		// emit on Stage 5 too. Pre-A2 the war-room dashboard's
		// booking-rate panels read flat-zero on Stage 5 because the
		// base service was provided directly, bypassing
		// NewMetricsDecorator. PR #125 worked around this by pointing
		// the dashboard at `admin_event_bus_published_total` instead;
		// with this wiring in place the dashboard can use either signal
		// honestly (see docs/monitoring.md § Stage-5-compatibility note).
		fx.Provide(
			observability.NewStage5Metrics,
			observability.NewBookingMetrics,
			newIntakePublisher,
			func(
				orderRepo domain.OrderRepository,
				ticketTypeRepo domain.TicketTypeRepository,
				inventoryRepo domain.Stage5InventoryRepository,
				publisher booking.IntakePublisher,
				s5metrics booking.Stage5Metrics,
				bmetrics booking.Metrics,
				cfg *config.Config,
				logger *mlog.Logger,
			) booking.Service {
				base := booking.NewKafkaIntakeService(orderRepo, ticketTypeRepo, inventoryRepo, publisher, s5metrics, cfg, logger)
				return booking.NewMetricsDecorator(base, bmetrics)
			},
			observability.NewWorkerMetrics,
			newWorkerMessageProcessor,
			newIntakeConsumer,
		),

		// Event service — same as cmd/booking-cli; delegates to
		// event.Service.CreateEvent for PG write + Redis hydrate.
		fx.Provide(event.NewService),

		// Inventory drift detector — safety net for the publish+revert dual-failure window.
		fx.Provide(
			bootstrap.NewDriftConfig,
			bootstrap.NewPrometheusDriftMetrics,
			recon.NewInventoryDriftDetector,
		),

		// Stage 5 abandon-path compensator + expiry sweeper.
		// postgres.NewStage5Compensator returns expiry.Compensator;
		// stagehttp.Compensator is a type alias for the same interface so
		// fx injects it without an fx.As annotation.
		fx.Provide(
			postgres.NewStage5Compensator,
			bootstrap.NewExpiryConfig,
			expiry.NewStage5Sweeper,
		),

		fx.Invoke(installServer),
	).Run()
}

// newIntakePublisher wires messaging.IntakePublisher with the
// MessagingConfig pulled from the global config. WriteTimeout is read
// from cfg.Kafka.WriteTimeout (env KAFKA_WRITE_TIMEOUT, default 5s)
// so ops can tune the durability-gate latency vs availability tradeoff
// without rebuilding — the same dial the rest of the Kafka surface uses.
func newIntakeConsumer(cfg *config.Config, p worker.MessageProcessor, inv domain.InventoryRepository, m booking.Stage5Metrics, logger *mlog.Logger) booking.IntakeConsumer {
	return messaging.NewIntakeConsumer(&cfg.Kafka, p, inv, m, logger)
}

func newWorkerMessageProcessor(uow application.UnitOfWork, bus admin.Bus, metrics worker.Metrics, logger *mlog.Logger) worker.MessageProcessor {
	// Stage 5 now hosts the admin SSE stream (PR #121 infrastructure
	// reused via bootstrap.AdminStreamModule). The fx-injected bus
	// is the same one the SSE handler reads from — order.created
	// events emitted from this processor land on /admin/events/stream.
	return worker.NewMessageProcessorMetricsDecorator(
		worker.NewOrderMessageProcessor(uow, bus, logger),
		metrics,
	)
}

func newIntakePublisher(cfg *config.Config) booking.IntakePublisher {
	return messaging.NewIntakePublisher(messaging.MessagingConfig{
		Brokers:      cfg.Kafka.Brokers,
		WriteTimeout: cfg.Kafka.WriteTimeout,
	})
}

// installServer wires the HTTP layer + worker + expiry sweeper
// under a SINGLE shared `runCtx` so the OnStop chain coordinates
// all three concurrent goroutines. This is the Stage 4 pattern
// (cmd/booking-cli/server.go's `installServer` + `startBackground
// Runners` + `shutdownAll`) — Stage 5 inherits it because it has
// MORE goroutines than Stages 1+2 and per-goroutine ctx would
// risk uncoordinated shutdown order.
//
// Lifecycle:
//
//   - OnStart: launch HTTP server, worker, sweeper. Each goroutine
//     respects `runCtx.Done()` for clean shutdown.
//   - OnStop: cancel `runCtx` (signals worker + sweeper to drain),
//     `srv.Shutdown(stopCtx)` (HTTP graceful close), wait for the
//     two background goroutines to finish bounded by the fx
//     stop deadline. If they exceed the budget, return
//     `stopCtx.Err()` so fx logs the slow-shutdown.
//
// fx.Shutdowner is injected so a fatal background-goroutine error
// (worker subscribe permanently broken, HTTP listener fails after
// OnStart) escalates to fx.Shutdown(ExitCode(1)) instead of being
// logged + swallowed.
func installServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	cfg *config.Config,
	db *sql.DB,
	bookingService booking.Service,
	eventService event.Service,
	compensator expiry.Compensator,
	sweeper *expiry.Stage5Sweeper,
	intakePublisher booking.IntakePublisher,
	intakeConsumer booking.IntakeConsumer,
	driftDetector *recon.InventoryDriftDetector,
	sseHandler *sse.Handler,
	adminJWT *bootstrap.AdminJWTHandle,
	logger *mlog.Logger,
) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/livez", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := router.Group(apiV1Prefix)
	v1.POST("/book", stagehttp.HandleBook(bookingService))
	v1.GET("/orders/:id", stagehttp.HandleGetOrder(bookingService))
	v1.POST("/orders/:id/pay", stagehttp.HandlePayIntent(db, "pi_stage5_"))
	v1.POST("/events", stagehttp.HandleCreateEvent(eventService))

	// Admin SSE event stream — PR #121 infrastructure reused for the
	// Stage 5 demo binary. Per-route JWT auth via ?token= query
	// (EventSource limitation). The handler returns 503 once
	// AdminStreamModule's OnStop sets shuttingDown — coordinated with
	// the booking lifecycle via the same fx graph.
	v1.GET("/admin/events/stream", adminJWT.Func(), sseHandler.HandleStream)
	// War-room dashboard — vanilla JS + EventSource. Operator pastes
	// JWT (mint via `booking-cli admin-token`) before connecting.
	router.StaticFile("/admin/", "./web/admin/events.html")

	router.POST("/test/payment/confirm/:id", stagehttp.HandleTestConfirm(db, compensator))

	addr := ":" + cfg.Server.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Single shared runCtx coordinates the worker + sweeper goroutines'
	// shutdown signal. The HTTP server goroutine is NOT in this
	// WaitGroup — it's drained via srv.Shutdown(stopCtx) instead, which
	// blocks until ListenAndServe returns. This split is intentional:
	// the wg only covers the two background goroutines that respond to
	// ctx.Done(); the HTTP server has its own stdlib graceful-close
	// semantics that don't fit the ctx pattern.
	runCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(startCtx context.Context) error {
			// Verify Kafka broker reachability before launching goroutines.
			// Prevents HTTP traffic from arriving before the consumer group
			// is established — if the broker is down, fail fast so k8s
			// restarts rather than silently degrading (H2 fix).
			if err := intakeConsumer.Ping(startCtx); err != nil {
				return fmt.Errorf("stage5 OnStart: %w", err)
			}

			// HTTP server in its own goroutine; failure escalates
			// via shutdowner so a port-collision after OnStart returns
			// kills the process (k8s restart on the next probe).
			go func() {
				logger.Info(context.Background(), "stage5 HTTP server starting",
					mlog.String("addr", addr))
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error(context.Background(), "stage5 HTTP server failed — escalating fx.Shutdown",
						tag.Error(err))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()

			// Stage 5 Kafka consumer — drains booking.intake.v5 into
			// PG via the existing worker.MessageProcessor (UoW with
			// DecrementTicket + Order.Create). A permanent consumer
			// failure escalates via fx.Shutdown so k8s restarts the
			// pod instead of silently halving inventory throughput.
			// IntakeConsumer.Start currently only returns nil on
			// ctx-cancel; the escalation branch covers a future
			// non-nil-error return path.
			wg.Go(func() {
				if err := intakeConsumer.Start(runCtx); err != nil && runCtx.Err() == nil {
					logger.Error(context.Background(), "stage5 intake consumer failed — escalating fx.Shutdown",
						tag.Error(err))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			})

			// Inventory drift detector — safety net for the Lua-deducted
			wg.Go(func() { runDriftDetector(runCtx, driftDetector, shutdowner, logger) })

			// Expiry sweeper — delegates to Stage5Sweeper in internal/.
			wg.Go(func() { runExpirySweeper(runCtx, sweeper, logger) })
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			// 0. SSE drain hints BEFORE srv.Shutdown. fx hooks run
			//    OnStop in LIFO order, so without this explicit call
			//    AdminStreamModule's OnStop (which is responsible for
			//    these hints in production) would only fire AFTER
			//    srv.Shutdown has already killed every SSE connection
			//    — making the jittered-retry-hint design dead code.
			//    Calling here unblocks the Q15 graceful-drain story
			//    for the Stage 5 demo binary; the same hook in
			//    AdminStreamModule.OnStop is a no-op on second call
			//    (handler.shuttingDown is atomic + idempotent;
			//    BroadcastRetryHints to an empty hub returns false
			//    without side effects).
			sseHandler.SetShuttingDown(true)
			retryCtx, retryCancel := context.WithTimeout(stopCtx, 100*time.Millisecond)
			sseHandler.BroadcastRetryHints(retryCtx)
			retryCancel()

			// 1. HTTP graceful close — drain in-flight requests
			//    before signalling background goroutines so no new
			//    Kafka publishes can arrive after the consumer stops.
			//    5s sub-budget keeps HTTP bounded even when fx's stop
			//    deadline is generous.
			shutdownCtx, cancelHTTP := context.WithTimeout(stopCtx, 5*time.Second)
			defer cancelHTTP()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Error(stopCtx, "stage5 HTTP shutdown error", tag.Error(err))
			}

			// 2. Signal consumer + sweeper + drift detector to stop.
			//    HTTP is fully drained at this point so no new
			//    Publish calls are in flight.
			cancel()

			// 3. Wait for background goroutines to drain, bounded by
			//    the fx stop deadline. If they exceed it we still
			//    close the Kafka connections — leaking broker sockets
			//    is worse than a slow-shutdown log line.
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			closeKafka := func() {
				if err := intakePublisher.Close(); err != nil {
					logger.Error(stopCtx, "stage5 IntakePublisher.Close error", tag.Error(err))
				}
				if err := intakeConsumer.Close(); err != nil {
					logger.Error(stopCtx, "stage5 IntakeConsumer.Close error", tag.Error(err))
				}
			}

			select {
			case <-done:
				closeKafka()
				return nil
			case <-stopCtx.Done():
				closeKafka()
				return stopCtx.Err()
			}
		},
	})
}

// runDriftDetector runs the inventory drift detector loop in a goroutine.
// Extracted from installServer's OnStart so the logic is named and testable
// without unwrapping the fx hook closure. Escalates to fx.Shutdown after
// detector.MaxConsecutiveFailures() consecutive failures so k8s restarts
// rather than silently degrading.
func runDriftDetector(ctx context.Context, detector *recon.InventoryDriftDetector, shutdowner fx.Shutdowner, logger *mlog.Logger) {
	interval := detector.SweepInterval()
	maxFail := detector.MaxConsecutiveFailures()
	logger.Info(ctx, "stage5 inventory drift detector starting",
		mlog.Duration("sweep_interval", interval),
		mlog.Int("escalate_after_consec_failures", maxFail))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var consecFailures atomic.Int32
	for {
		select {
		case <-ctx.Done():
			logger.Info(context.Background(), "stage5 inventory drift detector stopping")
			return
		case <-ticker.C:
			if err := detector.Sweep(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				n := consecFailures.Add(1)
				logger.Error(ctx, "stage5 drift sweep failed",
					tag.Error(err),
					mlog.Int("consec_failures", int(n)))
				if int(n) >= maxFail {
					logger.Error(ctx, "stage5 drift sweep exceeded consecutive-failure budget — escalating fx.Shutdown",
						mlog.Int("consec_failures", int(n)))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
					return
				}
			} else {
				consecFailures.Store(0)
			}
		}
	}
}

// runExpirySweeper runs the expiry-sweep loop in a goroutine.
// Extracted from installServer's OnStart for the same reason as runDriftDetector.
func runExpirySweeper(ctx context.Context, sweeper *expiry.Stage5Sweeper, logger *mlog.Logger) {
	logger.Info(ctx, "stage5 expiry sweeper starting",
		mlog.Duration("sweep_interval", sweeper.SweepInterval()))
	ticker := time.NewTicker(sweeper.SweepInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info(context.Background(), "stage5 expiry sweeper stopping")
			return
		case <-ticker.C:
			sweeper.Sweep(ctx)
		}
	}
}
