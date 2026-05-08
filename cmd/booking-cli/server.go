package main

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/fx"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/event"
	"booking_monitor/internal/application/outbox"
	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api"
	"booking_monitor/internal/infrastructure/api/booking"
	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/api/ops"
	"booking_monitor/internal/infrastructure/api/testapi"
	"booking_monitor/internal/infrastructure/api/webhook"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/messaging"
	"booking_monitor/internal/infrastructure/observability"
	paymentInfra "booking_monitor/internal/infrastructure/payment"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runServer is the `server` subcommand entry: load config, wire fx DI,
// block on app.Run until SIGINT/SIGTERM. Uses stdlog.Fatalf for config
// errors because the app logger isn't constructed until fx starts.
func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),

		cache.Module,
		application.Module,
		api.Module,
		messaging.Module,

		fx.Provide(func(cfg *config.Config) messaging.MessagingConfig {
			return messaging.MessagingConfig{
				Brokers:      cfg.Kafka.Brokers,
				WriteTimeout: cfg.Kafka.WriteTimeout,
			}
		}),
		fx.Provide(func(cfg *config.Config) int { return cfg.Kafka.OutboxBatchSize }),

		fx.Provide(observability.NewWorkerMetrics),
		fx.Provide(observability.NewBookingMetrics),

		// Payment service wiring for the HTTP server. D4 added the
		// /api/v1/orders/:id/pay endpoint, which depends on
		// payment.Service. After D7 the legacy payment_worker
		// subcommand (which had its own fx graph providing the same
		// Service for the A4 auto-charge path) is gone; the server
		// process is the only host of payment.Service now. The
		// MockGateway is wired here as the gateway adapter; real
		// Stripe integration lands in a later PR (D4.2).
		//
		// fx wiring note (D7): payment.NewService takes
		// `domain.PaymentIntentCreator` (the narrow CreatePaymentIntent
		// half of the gateway), NOT the combined `PaymentGateway`.
		// fx/dig matches by exact type key — the provider must
		// advertise PaymentIntentCreator explicitly via fx.As.
		// recon runs as a separate subcommand with its own fx graph
		// and provides its own PaymentStatusReader; there's no
		// cross-graph wiring here.
		fx.Provide(
			fx.Annotate(paymentInfra.NewMockGateway, fx.As(new(domain.PaymentIntentCreator))),
			payment.NewService,
		),
		// D5 webhook service + metrics adapters. The handler itself
		// (api/webhook) is wired via api.Module; here we satisfy its
		// dependencies (payment.WebhookService + the two metrics
		// interfaces). The application service is gated on the
		// PAYMENT_WEBHOOK_SECRET being non-empty at handler-construction
		// time — see api/webhook/module.go::newHandlerFromConfig.
		fx.Provide(
			func(
				orderRepo domain.OrderRepository,
				uow application.UnitOfWork,
				metrics payment.WebhookMetrics,
				cfg *config.Config,
				logger *mlog.Logger,
			) payment.WebhookService {
				return payment.NewWebhookService(orderRepo, uow, metrics, cfg.Payment.WebhookExpectedLiveMode, logger)
			},
			bootstrap.NewPrometheusWebhookMetrics,
			bootstrap.NewPrometheusWebhookHandlerMetrics,
		),
		// Worker fx wiring lives here (not in application/module.go)
		// because the worker subpackage imports `application` for shared
		// types (UnitOfWork, Repositories), so an `application → worker`
		// edge there would create an import cycle. Same convention used
		// by payment/saga/recon: each cmd
		// owns its subpackage's fx.Provide. CP2.6b moved this here.
		//
		// worker.Service is provided as a decorated chain:
		//   base MessageProcessor -> metrics decorator -> worker.Service
		// Future tracing decorator MUST sit between metrics and the
		// service so spans wrap the metrics work.
		fx.Provide(
			worker.DefaultRetryPolicy,
			func(
				queue worker.OrderQueue,
				uow application.UnitOfWork,
				metrics worker.Metrics,
				logger *mlog.Logger,
			) worker.Service {
				base := worker.NewOrderMessageProcessor(uow, logger)
				processor := worker.NewMessageProcessorMetricsDecorator(base, metrics)
				return worker.NewService(queue, processor, logger)
			},
		),
		// Outbox fx wiring lives here for the same import-cycle reason
		// as worker — outbox/ imports `application` for EventPublisher
		// and DistributedLock. CP2.6b moved this from
		// application/module.go.
		fx.Provide(outbox.NewRelay),
		// Saga compensator wiring lives here for the same reason —
		// saga/ imports `application` for OrderFailedEvent + UnitOfWork
		// + Repositories. CP2.6b moved this from application/module.go.
		fx.Provide(saga.NewCompensator),
		// Event service wiring — D4.1 moved this from
		// application/module.go because event.Service now imports
		// `application` for the UoW interface (CreateEvent atomically
		// creates an event + a default ticket_type, which requires
		// cross-aggregate transactional coordination).
		fx.Provide(event.NewService),
		// Start the outbox relay (with tracing) as a background
		// goroutine managed by the Fx lifecycle. The run-context is
		// derived from context.Background() rather than a
		// caller-supplied ctx because the relay must survive any
		// individual fx lifecycle hook timeout. OnStop invokes
		// cancel() explicitly so the background is still bounded by
		// the fx lifecycle — just decoupled from the OnStop ctx
		// deadline.
		fx.Invoke(func(lc fx.Lifecycle, relay *outbox.Relay) {
			traced := outbox.NewTracingDecorator(relay)
			ctx, cancel := context.WithCancel(context.Background())
			lc.Append(fx.Hook{
				OnStart: func(_ context.Context) error {
					go traced.Run(ctx)
					return nil
				},
				OnStop: func(_ context.Context) error {
					cancel()
					return nil
				},
			})
		}),
		fx.Provide(func(cfg *config.Config, rdb *redis.Client, logger *mlog.Logger) *messaging.SagaConsumer {
			return messaging.NewSagaConsumer(&cfg.Kafka, rdb, logger)
		}),

		// App-startup inventory rehydrate from DB → Redis. Runs as an
		// OnStart lifecycle hook BEFORE installServer's HTTP listener
		// so the booking hot path always sees a populated cache. See
		// `cache.RehydrateInventory` for the design rationale (Redis
		// is ephemeral, DB is truth, SETNX preserves live values).
		// Registered before installServer so its OnStart hook runs
		// first in lifecycle order.
		fx.Invoke(installInventoryRehydrate),

		fx.Invoke(installServer),
	)
	app.Run()
}

// installInventoryRehydrate registers the app-startup OnStart hook
// that scans Postgres events and populates Redis qty keys via SETNX.
// Lifecycle ordering: this fx.Invoke is registered BEFORE
// installServer in runServer, so its OnStart fires first — by the
// time HTTP starts accepting bookings, the cache is populated.
//
// Errors abort startup. A failed rehydrate means Redis state is
// unknown vs DB; serving requests in that condition would silently
// reject valid bookings as sold-out. Better to fail-fast and let the
// operator investigate (k8s liveness probe → pod restart cycle ends
// when DB or Redis is reachable again).
func installInventoryRehydrate(
	lc fx.Lifecycle,
	eventRepo domain.EventRepository,
	ticketTypeRepo domain.TicketTypeRepository,
	rdb *redis.Client,
	locker application.DistributedLock,
	cfg *config.Config,
	logger *mlog.Logger,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return cache.RehydrateInventory(ctx, cache.RehydrateInventoryParams{
				EventRepo:      eventRepo,
				TicketTypeRepo: ticketTypeRepo,
				RedisClient:    rdb,
				Locker:         locker,
				Cfg:            cfg,
				Logger:         logger,
			})
		},
	})
}

// installServer builds + wires the HTTP server, pprof server, and
// background runners. Each start* helper is small enough to keep the
// lifecycle hook readable (previously the OnStart closure was 80 lines).
func installServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	handler booking.BookingHandler,
	idempotencyRepo domain.IdempotencyRepository,
	healthHandler *ops.HealthHandler,
	webhookHandler *webhook.Handler,
	testHandler *testapi.Handler,
	logger *mlog.Logger,
	cfg *config.Config,
	workerSvc worker.Service,
	sagaConsumer *messaging.SagaConsumer,
	compensator saga.Compensator,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installServer: %w", err)
	}

	engine, err := buildGinEngine(cfg, logger, handler, idempotencyRepo, healthHandler, webhookHandler, testHandler)
	if err != nil {
		return fmt.Errorf("installServer: %w", err)
	}

	httpServer := buildHTTPServer(cfg, engine)
	var pprofServer *http.Server
	if cfg.Server.EnablePprof {
		pprofServer = buildPprofServer(cfg, logger)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			startHTTPServer(httpServer, cfg, logger, shutdowner)
			if pprofServer != nil {
				startPprofServer(pprofServer, logger)
			}
			startBackgroundRunners(runCtx, workerSvc, sagaConsumer, compensator, logger, shutdowner)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return shutdownAll(ctx, cancel, sagaConsumer, pprofServer, httpServer, tp, logger)
		},
	})
	return nil
}

// buildGinEngine constructs the Gin engine with middleware. Trusted-proxy
// config failure used to degrade silently to a Warn — that left ClientIP()
// returning the nginx pod IP, defeating rate-limits and audit logs. Now
// fatal at construction time.
//
// Coupling note: this signature accepts subpackage-typed handlers
// (`booking.BookingHandler`, `*ops.HealthHandler`) directly rather than
// re-exposing them through the umbrella `api` package. Server-side
// wiring is the legitimate place for that coupling — the wire layer
// owns the route topology and benefits from compile-time type checks.
// `api.Module` remains the single fx import for runtime wiring; the
// type imports here are a separate concern from fx graph composition.
func buildGinEngine(
	cfg *config.Config,
	logger *mlog.Logger,
	handler booking.BookingHandler,
	idempotencyRepo domain.IdempotencyRepository,
	healthHandler *ops.HealthHandler,
	webhookHandler *webhook.Handler,
	testHandler *testapi.Handler,
) (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Recovery())

	if err := r.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return nil, fmt.Errorf("buildGinEngine: SetTrustedProxies: %w", err)
	}

	// CORS — must run BEFORE Combined so OPTIONS preflights short-circuit
	// at 204 without allocating a correlation id or generating an access
	// log line. Empty allow-list disables the middleware (production-safe
	// default — same-origin / non-browser callers are unaffected).
	r.Use(middleware.CORS(cfg.Server.CORSAllowedOrigins))

	// Single combined middleware: logger + correlation ID in ONE
	// context.WithValue + ONE c.Request.WithContext (see Phase 14 GC work).
	r.Use(middleware.Combined(logger))
	r.Use(middleware.Metrics())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Health probes live at the engine root, not under /api/v1 — they
	// are operational endpoints with their own contract (k8s probe
	// targets) and must not move with API versioning.
	ops.RegisterHealthRoutes(r, healthHandler)

	// D5 webhook listener — at the root, NOT under /api/v1. Provider
	// source IPs are unpredictable + signature-authenticated; nginx's
	// /api/ rate-limit zone would throttle legitimate redelivery.
	webhook.RegisterRoutes(r, webhookHandler)

	// D5 test endpoints — gated. Production deployments leave the flag
	// false; the route group never mounts so /test/* returns 404
	// (not 401), making the surface impossible to enable accidentally.
	if cfg.Server.EnableTestEndpoints {
		testapi.RegisterRoutes(r, testHandler)
	}

	v1 := r.Group(apiV1Prefix)
	// Body-size cap on the versioned API group. Applied here (not at
	// the engine root) so future operational endpoints with different
	// caps can opt out. Industry pattern: size validation at the HTTP
	// boundary, NOT inside the storage layer (Stripe / Shopify /
	// GitHub Octokit / AWS API Gateway). See PROJECT_SPEC §6.8.
	v1.Use(middleware.BodySize(middleware.MaxBookingBodyBytes))
	booking.RegisterRoutes(v1, handler, idempotencyRepo)
	// NOTE: the legacy POST /book route (Phase 0) was removed because it
	// bypassed the nginx `location /api/` rate-limit zone. All callers
	// must use /api/v1/book. Closes action-list item H9.
	return r, nil
}

// buildHTTPServer sets explicit timeouts so we honour cfg.Server.Read/Write
// limits (r.Run() would discard them, leaving slow-loris exposure).
func buildHTTPServer(cfg *config.Config, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           h,
		ReadTimeout:       cfg.Server.ReadTimeout,
		ReadHeaderTimeout: cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       2 * cfg.Server.WriteTimeout,
		MaxHeaderBytes:    1 << 20, // 1 MiB.
	}
}

// buildPprofServer returns the operator-only listener that carries pprof
// endpoints + /admin/loglevel. Binds to 127.0.0.1 by default: heap dumps
// + goroutine traces + log-level control must not be reachable on the
// public interface without explicit opt-in (cfg.Server.PprofAddr /
// PPROF_ADDR override).
//
// Uses a private ServeMux so we don't inherit whatever http.DefaultServeMux
// accumulates elsewhere in the binary (tests, third-party libs, etc.).
func buildPprofServer(cfg *config.Config, logger *mlog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/admin/loglevel", logger.LevelHandler())

	return &http.Server{
		Addr:              cfg.Server.PprofAddr,
		Handler:           mux,
		ReadTimeout:       cfg.Server.PprofReadTimeout,
		ReadHeaderTimeout: cfg.Server.PprofReadTimeout,
		WriteTimeout:      cfg.Server.PprofWriteTimeout,
	}
}

// startHTTPServer runs ListenAndServe in a goroutine so installServer can
// return and fx can finish OnStart. Any non-ErrServerClosed exit means the
// listener died unexpectedly — escalate via fx.Shutdown so k8s restarts
// the pod instead of keeping the process up with a dead listener.
func startHTTPServer(srv *http.Server, cfg *config.Config, logger *mlog.Logger, shutdowner fx.Shutdowner) {
	go func() {
		logger.L().Info("Starting server",
			mlog.String("addr", srv.Addr),
			mlog.Duration("read_timeout", cfg.Server.ReadTimeout),
			mlog.Duration("write_timeout", cfg.Server.WriteTimeout),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Error("Server failed", tag.Error(err))
			_ = shutdowner.Shutdown(fx.ExitCode(1))
		}
	}()
}

// startPprofServer runs the operator-only pprof + /admin/loglevel listener.
// Deliberately asymmetric with startHTTPServer: pprof is a diagnostics
// sidecar, so its death is logged but must NOT escalate to fx.Shutdown —
// losing pprof should never take down the business traffic path.
func startPprofServer(srv *http.Server, logger *mlog.Logger) {
	go func() {
		logger.L().Info("pprof server started", mlog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.L().Error("pprof server failed", tag.Error(err))
		}
	}()
}

// startBackgroundRunners launches the Redis-stream worker and saga
// consumer. Either failure triggers fx shutdown so k8s restarts the pod;
// previously saga failure was Error-logged but the process kept serving
// new bookings against a dead compensation path.
func startBackgroundRunners(
	ctx context.Context,
	workerSvc worker.Service,
	sagaConsumer *messaging.SagaConsumer,
	compensator saga.Compensator,
	logger *mlog.Logger,
	shutdowner fx.Shutdowner,
) {
	go func() {
		if err := workerSvc.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.L().Error("Worker stopped with error", tag.Error(err))
			_ = shutdowner.Shutdown(fx.ExitCode(1))
		}
	}()

	go func() {
		if err := sagaConsumer.Start(ctx, compensator); err != nil && !errors.Is(err, context.Canceled) {
			logger.L().Error("Saga consumer stopped with error", tag.Error(err))
			_ = shutdowner.Shutdown(fx.ExitCode(1))
		}
	}()
}

// shutdownAll flushes HTTP + pprof + tracer + worker in order. Only the
// tracer error is returned — lost span data is a real observability gap;
// HTTP / pprof close races during fast SIGINT are expected.
func shutdownAll(
	ctx context.Context,
	cancel context.CancelFunc,
	sagaConsumer *messaging.SagaConsumer,
	pprofServer, httpServer *http.Server,
	tp *trace.TracerProvider,
	logger *mlog.Logger,
) error {
	logger.L().Info("Stopping worker services...")
	cancel()
	if err := sagaConsumer.Close(); err != nil {
		logger.L().Error("Saga consumer close error", tag.Error(err))
	}

	if pprofServer != nil {
		logger.L().Info("Shutting down pprof server")
		if err := pprofServer.Shutdown(ctx); err != nil {
			logger.L().Error("pprof server shutdown error", tag.Error(err))
		}
	}

	if httpServer != nil {
		logger.L().Info("Shutting down HTTP server")
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.L().Error("HTTP server shutdown error", tag.Error(err))
		}
	}

	// Give the tracer its own budget rooted at Background — the fx
	// OnStop ctx may already be near-expired after sagaConsumer.Close
	// + pprof.Shutdown + httpServer.Shutdown drained the shared budget
	// above. Without an independent budget, in-flight spans that were
	// still buffered would silently drop on a "context deadline
	// exceeded" return from tp.Shutdown, defeating the whole reason
	// we set up batched OTLP export.
	tpCtx, tpCancel := context.WithTimeout(context.Background(), tracerShutdownTimeout)
	defer tpCancel()

	logger.L().Info("Shutting down tracer provider")
	shutdownErr := tp.Shutdown(tpCtx)
	if shutdownErr != nil {
		logger.L().Error("tracer shutdown error", tag.Error(shutdownErr))
	}

	_ = logger.Sync() // zap Sync on stderr is OS-specific; swallow.
	return shutdownErr
}

// tracerShutdownTimeout caps tp.Shutdown wall clock independently of
// the fx OnStop ctx so leftover-budget exhaustion can't truncate span
// flush. 5s mirrors the OTel SDK's own default batch flush window.
const tracerShutdownTimeout = 5 * time.Second
