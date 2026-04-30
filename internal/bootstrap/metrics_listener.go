package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// metricsListenerReadTimeout / metricsListenerWriteTimeout cap a
// single Prometheus scrape. Prometheus's default scrape_timeout is 10s
// (configured globally in deploy/prometheus/prometheus.yml); these
// budgets are deliberately tighter so a misbehaving collector cannot
// pin the listener goroutine open.
//
// metricsListenerShutdownTimeout caps OnStop's call to srv.Shutdown.
// /metrics + /healthz are both sub-ms today, but bounding the wait
// here independently of the fx-parent stop budget protects the rest of
// the lifecycle if a future handler with longer requests is registered.
const (
	metricsListenerReadTimeout      = 5 * time.Second
	metricsListenerWriteTimeout     = 30 * time.Second
	metricsListenerShutdownTimeout  = 5 * time.Second
)

// InstallMetricsListener wires a metrics-only HTTP listener into the
// fx lifecycle of a worker subcommand (`payment`, `recon`,
// `saga-watchdog`). The listener serves:
//
//   - GET /metrics   → promhttp.Handler() against prometheus.DefaultGatherer
//   - GET /healthz   → 200 OK if the process is up (no dependency probes;
//     mirrors `/livez` from the API server's ops package)
//
// `cfg.Worker.MetricsAddr` is the bind address (default `:9091`).
// Empty disables the listener entirely — useful for `--once` CronJob
// invocations where the process exits before Prometheus could scrape
// it, and for unit tests that don't want a real port binding.
//
// Why a private ServeMux instead of http.DefaultServeMux: vendored
// libraries (e.g., the Go runtime's own `net/http/pprof` if imported
// elsewhere) sometimes register handlers into the default mux as a
// side effect. Using a private mux keeps this listener's surface
// honest — the only handlers reachable are the two registered here.
//
// Why a separate listener instead of riding on the API server's
// `/metrics`: the worker subcommands have no Gin engine, no main
// HTTP server. Adding a Gin engine + middleware + cert hardening
// just to serve a single endpoint would be the wrong shape. A
// minimal `http.Server` with one handler is the simplest correct
// answer. The application-server's /metrics endpoint
// (cmd/booking-cli/server.go) stays as it is.
//
// Closes Phase 2 checkpoint O3: before this helper, recon_*,
// saga_watchdog_*, kafka_consumer_retry_total and the four
// `*_failures_total` counters were registered into worker
// processes' default prometheus registries but never scraped.
func InstallMetricsListener(lc fx.Lifecycle, cfg *config.Config, logger *mlog.Logger) error {
	addr := cfg.Worker.MetricsAddr
	if addr == "" {
		logger.L().Info("Metrics listener disabled (cfg.Worker.MetricsAddr empty)")
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       metricsListenerReadTimeout,
		ReadHeaderTimeout: metricsListenerReadTimeout,
		WriteTimeout:      metricsListenerWriteTimeout,
	}

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				logger.L().Info("Starting worker metrics listener",
					mlog.String("component", "metrics_listener"),
					mlog.String("addr", addr))
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					// Distinct log path so this can be alerted on
					// independently of the worker's main loop. Don't
					// escalate via fx.Shutdowner — losing the metrics
					// listener should NOT crash the worker (workers'
					// primary job is queue draining, not metrics
					// serving). Operators see the gap on the scrape
					// side via Prometheus `up{job="..."} == 0`.
					// `component=metrics_listener` tag makes the error
					// filterable in Loki/Grafana without grepping
					// message strings.
					logger.L().Error("Worker metrics listener failed",
						mlog.String("component", "metrics_listener"),
						tag.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(parent context.Context) error {
			// Bound the shutdown wait independently of the fx parent
			// budget. Today /metrics + /healthz are sub-ms so this
			// returns promptly; the explicit deadline is future-proofing
			// against adding a longer-running handler later. ctx is
			// derived from `parent` so caller cancellation still
			// propagates.
			ctx, cancel := context.WithTimeout(parent, metricsListenerShutdownTimeout)
			defer cancel()
			return srv.Shutdown(ctx)
		},
	})
	return nil
}
