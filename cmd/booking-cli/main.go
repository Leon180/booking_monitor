package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"math/rand/v2"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"booking_monitor/internal/application"
	paymentApp "booking_monitor/internal/application/payment"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/messaging"
	"booking_monitor/internal/infrastructure/observability"
	paymentInfra "booking_monitor/internal/infrastructure/payment"
	postgresRepo "booking_monitor/internal/infrastructure/persistence/postgres"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// Code-level constants. Deployer-tunable values live in config.Config
// (see cfg.Server / cfg.Postgres); the constants below are either program
// contracts (API version), config bootstrap (can't live in config by
// definition), OTel-convention fallbacks, or CLI-tool flag defaults.
const (
	// apiV1Prefix is the single source of truth for the versioned API
	// router group. runServer registers it; stress + integration tools
	// import it to avoid drift.
	apiV1Prefix = "/api/v1"

	// Config bootstrap — can't be in config by definition.
	envConfigPath     = "CONFIG_PATH"
	defaultConfigPath = "config/config.yml"

	// OTel convention: OTEL_SERVICE_NAME is the standard env var; the
	// const is just a fallback when it's unset.
	otelServiceNameDefault = "booking-service"

	// Stress-test CLI flag defaults. Not in config because the stress
	// binary is a one-off tool, not a server process reading config.
	stressDefaultBaseURL      = "http://localhost:8080"
	stressDefaultEventID      = 1
	stressDefaultUserRangeMax = 10000
	stressClientMaxIdleConns  = 1000
	stressClientTimeout       = 10 * time.Second
)

func main() {
	rootCmd := &cobra.Command{Use: "booking-cli"}

	serverCmd := &cobra.Command{Use: "server", Short: "Run the API server", Run: runServer}
	paymentCmd := &cobra.Command{Use: "payment", Short: "Run the Payment Service worker", Run: runPaymentWorker}

	stressCmd := &cobra.Command{Use: "stress", Short: "Run stress test", Run: runStress}
	stressCmd.Flags().IntP("concurrency", "c", 1000, "Concurrency level")
	stressCmd.Flags().IntP("requests", "n", 2000, "Total requests")
	stressCmd.Flags().String("base-url", stressDefaultBaseURL, "Target base URL (scheme://host:port)")
	stressCmd.Flags().Int("event-id", stressDefaultEventID, "Event ID to book against")
	stressCmd.Flags().Int("user-range", stressDefaultUserRangeMax, "Upper bound for random user_id")

	rootCmd.AddCommand(serverCmd, stressCmd, paymentCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolveConfigPath reads CONFIG_PATH, falling back to the repo default.
// The env var lets us run under systemd / k8s initContainers where CWD differs.
func resolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(envConfigPath)); p != "" {
		return p
	}
	return defaultConfigPath
}

// commonFxOptions holds the fx providers shared by every command. Keeps
// runServer + runPaymentWorker + any future CLI from drifting on logger /
// config / DB wiring. DBMetrics is shared because every command that
// opens a Postgres connection uses PostgresUnitOfWork and therefore
// needs the rollback-failure counter.
func commonFxOptions(cfg *config.Config) fx.Option {
	return fx.Options(
		bootstrap.LogModule,
		fx.Provide(func() *config.Config { return cfg }),
		fx.Provide(provideDB),
		fx.Provide(observability.NewDBMetrics),
		postgresRepo.Module,
	)
}

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
		commonFxOptions(cfg),
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
	consumer *messaging.KafkaConsumer,
	service domain.PaymentService,
	logger *mlog.Logger,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installPaymentWorker: %w", err)
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

// runServer is the `server` subcommand entry: load config, wire fx DI,
// block on app.Run until SIGINT/SIGTERM. Uses stdlog.Fatalf for config
// errors because the app logger isn't constructed until fx starts.
func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		commonFxOptions(cfg),

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
		fx.Provide(observability.NewQueueMetrics),
		fx.Provide(func(cfg *config.Config, rdb *redis.Client, logger *mlog.Logger) *messaging.SagaConsumer {
			return messaging.NewSagaConsumer(&cfg.Kafka, rdb, logger)
		}),

		fx.Invoke(installServer),
	)
	app.Run()
}

// installServer builds + wires the HTTP server, pprof server, and
// background runners. Each start* helper is small enough to keep the
// lifecycle hook readable (previously the OnStart closure was 80 lines).
func installServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	handler api.BookingHandler,
	logger *mlog.Logger,
	cfg *config.Config,
	worker application.WorkerService,
	sagaConsumer *messaging.SagaConsumer,
	compensator application.SagaCompensator,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installServer: %w", err)
	}

	engine, err := buildGinEngine(cfg, logger, handler)
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
			startBackgroundRunners(runCtx, worker, sagaConsumer, compensator, logger, shutdowner)
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
func buildGinEngine(cfg *config.Config, logger *mlog.Logger, handler api.BookingHandler) (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Recovery())

	if err := r.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return nil, fmt.Errorf("buildGinEngine: SetTrustedProxies: %w", err)
	}

	// Single combined middleware: logger + correlation ID in ONE
	// context.WithValue + ONE c.Request.WithContext (see Phase 14 GC work).
	r.Use(api.CombinedMiddleware(logger))
	r.Use(observability.MetricsMiddleware())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := r.Group(apiV1Prefix)
	api.RegisterRoutes(v1, handler)
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
			zap.String("addr", srv.Addr),
			zap.Duration("read_timeout", cfg.Server.ReadTimeout),
			zap.Duration("write_timeout", cfg.Server.WriteTimeout),
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
		logger.L().Info("pprof server started", zap.String("addr", srv.Addr))
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
	worker application.WorkerService,
	sagaConsumer *messaging.SagaConsumer,
	compensator application.SagaCompensator,
	logger *mlog.Logger,
	shutdowner fx.Shutdowner,
) {
	go func() {
		if err := worker.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
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

	logger.L().Info("Shutting down tracer provider")
	shutdownErr := tp.Shutdown(ctx)
	if shutdownErr != nil {
		logger.L().Error("tracer shutdown error", tag.Error(shutdownErr))
	}

	_ = logger.Sync() // zap Sync on stderr is OS-specific; swallow.
	return shutdownErr
}

// initTracer constructs the OTLP tracer provider. Errors are fatal: a
// nil exporter would make the first span-export call panic.
func initTracer() (*trace.TracerProvider, error) {
	ctx := context.Background()

	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = otelServiceNameDefault
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(svcName)))
	if err != nil {
		return nil, fmt.Errorf("initTracer: resource.New: %w", err)
	}

	// WithInsecure is fine for the in-cluster Jaeger collector; gate on
	// OTEL_EXPORTER_OTLP_INSECURE once we talk to a remote collector.
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("initTracer: otlptracegrpc.New: %w", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithSampler(resolveSampler()),
		trace.WithResource(res),
		trace.WithBatcher(traceExporter),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// resolveSampler picks the trace sampler based on OTEL_TRACES_SAMPLER_RATIO.
// Accepted values:
//   - unset/empty: AlwaysSample (backwards-compat default)
//   - "0":         NeverSample
//   - "1"/"1.0":   AlwaysSample
//   - 0<r<1:       TraceIDRatioBased(r)
//   - else:        warn + fallback to AlwaysSample
func resolveSampler() trace.Sampler {
	raw := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_RATIO"))
	if raw == "" {
		return trace.AlwaysSample()
	}
	r, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		stdlog.Printf("initTracer: invalid OTEL_TRACES_SAMPLER_RATIO=%q, falling back to AlwaysSample: %v", raw, err)
		return trace.AlwaysSample()
	}
	switch {
	case r <= 0:
		return trace.NeverSample()
	case r >= 1:
		return trace.AlwaysSample()
	default:
		return trace.TraceIDRatioBased(r)
	}
}

// provideDB opens Postgres, applies pool settings, then retries Ping
// until the DB is reachable or the budget expires. PingContext is used
// so a stuck network call cannot exceed our per-attempt timeout.
func provideDB(cfg *config.Config, logger *mlog.Logger) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.Postgres.DSN)
	if err != nil {
		return nil, fmt.Errorf("provideDB: sql.Open: %w", err)
	}

	// Pool settings BEFORE the retry loop so retries exercise the
	// configured limits (previously setters ran after the first Ping,
	// which meant the initial probe burned a slot at default pool size).
	db.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	db.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	db.SetConnMaxIdleTime(cfg.Postgres.MaxIdleTime)
	// MaxLifetime forces periodic conn recycling; bounds long-lived
	// connection staleness (prepared-stmt caches, PgBouncer auth drift).
	db.SetConnMaxLifetime(cfg.Postgres.MaxLifetime)

	attempts := cfg.Postgres.PingAttempts
	interval := cfg.Postgres.PingInterval
	perAttempt := cfg.Postgres.PingPerAttempt

	totalBudget := time.Duration(attempts) * (interval + perAttempt)
	ctx, cancel := context.WithTimeout(context.Background(), totalBudget)
	defer cancel()

	var pingErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, perAttempt)
		pingErr = db.PingContext(attemptCtx)
		attemptCancel()
		if pingErr == nil {
			return db, nil
		}
		logger.L().Warn("waiting for Postgres", zap.Int("attempt", attempt), tag.Error(pingErr))
		if attempt == attempts {
			break
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return nil, fmt.Errorf("provideDB: context cancelled: %w", ctx.Err())
		}
	}
	return nil, fmt.Errorf("provideDB: postgres unreachable after %d attempts: %w", attempts, pingErr)
}

// runStress is the `stress` subcommand entry: a one-shot load generator
// against POST /api/v1/book. Exits once the job queue drains — no fx,
// no lifecycle, not a server.
func runStress(cmd *cobra.Command, _ []string) {
	// cobra validates these flags at registration; the only failure mode
	// is "flag not defined", which is a compile-time impossibility here.
	concurrency, _ := cmd.Flags().GetInt("concurrency")
	totalRequests, _ := cmd.Flags().GetInt("requests")
	baseURL, _ := cmd.Flags().GetString("base-url")
	eventID, _ := cmd.Flags().GetInt("event-id")
	userRange, _ := cmd.Flags().GetInt("user-range")

	targetURL := strings.TrimRight(baseURL, "/") + apiV1Prefix + "/book"
	fmt.Printf("Starting stress test: %d workers, %d requests, target: %s (event=%d, user_range=%d)\n",
		concurrency, totalRequests, targetURL, eventID, userRange)

	startStressTest(concurrency, totalRequests, targetURL, eventID, userRange)
}

// startStressTest orchestrates the load burst. jobs is pre-filled +
// closed so workers can range to completion without an explicit "done"
// signal. startChan is a release barrier: every worker blocks on it
// and they all unblock together, which is what flash-sale traffic
// actually looks like at the wire.
func startStressTest(concurrency, totalRequests int, url string, eventID, userRange int) {
	var successCount, failCount int64
	var wg sync.WaitGroup

	jobs := make(chan struct{}, totalRequests)
	for range totalRequests {
		jobs <- struct{}{}
	}
	close(jobs)

	startChan := make(chan struct{})

	wg.Add(concurrency)
	for range concurrency {
		go stressWorker(jobs, startChan, &wg, &successCount, &failCount, url, eventID, userRange)
	}

	// Sleep gives workers time to block on <-startChan so they start
	// flooding at approximately the same moment.
	time.Sleep(1 * time.Second)
	start := time.Now()
	fmt.Println("Flooding...")
	close(startChan)

	wg.Wait()
	duration := time.Since(start)

	fmt.Printf("Completed in %v\n", duration)
	if duration.Seconds() > 0 {
		fmt.Printf("Requests per second: %.2f\n", float64(totalRequests)/duration.Seconds())
	}
	fmt.Printf("Success: %d\n", successCount)
	fmt.Printf("Failed: %d\n", failCount)
}

// stressWorker drains the jobs channel, firing POST /book for each.
// Blocks on startChan first so every worker fires its first request
// at approximately the same instant as its peers.
func stressWorker(
	jobs <-chan struct{},
	startChan <-chan struct{},
	wg *sync.WaitGroup,
	successCount, failCount *int64,
	url string,
	eventID, userRange int,
) {
	defer wg.Done()
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        stressClientMaxIdleConns,
			MaxIdleConnsPerHost: stressClientMaxIdleConns,
		},
		Timeout: stressClientTimeout,
	}

	<-startChan

	for range jobs {
		// json.Marshal of map[string]int cannot fail for these types.
		reqBody, _ := json.Marshal(map[string]int{
			"user_id":  rand.IntN(userRange) + 1,
			"event_id": eventID,
			"quantity": 1,
		})

		resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqBody))
		if err != nil {
			atomic.AddInt64(failCount, 1)
			continue
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			fmt.Println("Error consumption body:", err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			atomic.AddInt64(successCount, 1)
		} else {
			atomic.AddInt64(failCount, 1)
		}
	}
}
