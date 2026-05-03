package observability

// Cross-sweeper meta-metric. Per-sweeper Metrics interfaces (recon.Metrics,
// recon.DriftMetrics, saga.Metrics) carry the per-sweep ergonomic counters
// (resolved-by-outcome, errors-by-kind, etc.). This counter is one level
// up — it tracks the meta-failure where a sweep goroutine PANICS and is
// rescued by the loop's defer recover(). The counter is bumped from the
// boot-side wiring (cmd/booking-cli/recon.go etc.), NOT from inside the
// per-sweeper application code, because:
//
//  1. Application-layer Sweep() doesn't see its own panic — the panic
//     propagates up to the calling goroutine, which is in cmd/.
//  2. The per-sweeper Metrics interfaces deliberately stay small ("BookingMetrics
//     interface should fit on one screen") — adding RecordPanic to every
//     interface would be ceremony.
//  3. A single labelled counter is the shape Prometheus expects for "same
//     observation, different sweepers" — no per-sweeper duplication.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SweepGoroutinePanicsTotal increments when a periodic sweeper goroutine
// (Reconciler, InventoryDriftDetector, SagaWatchdog, etc.) recovers from
// a panic via the loop's defer. The counter exists because without it, a
// goroutine that panics-then-recovers-then-resumes is invisible to
// `up{}` (the metrics listener stays alive) and to TargetDown — operators
// would see "service is healthy" while every sweep is silently failing.
//
// In healthy production this counter MUST stay 0. Any non-zero rate is
// an architectural signal: a bug deterministically panicking, OR a
// previously-unhandled error class surfacing as a panic. The
// `SweepGoroutinePanic` alert fires immediately on any non-zero rate
// (no soak window) — single panics matter.
//
// `sweeper` label distinguishes which loop panicked:
//
//	"recon"               — Reconciler.Sweep (loop mode)
//	"inventory_drift"     — InventoryDriftDetector.Sweep (loop mode)
//	"saga_watchdog"       — saga.Watchdog.Sweep (loop mode)
//	"once_recon"          — Reconciler.Sweep, --once / CronJob mode
//	"once_drift"          — InventoryDriftDetector.Sweep, --once mode
//	"once_saga_watchdog"  — saga.Watchdog.Sweep, --once mode
var sweepGoroutinePanicsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "sweep_goroutine_panics_total",
		Help: "Total panics recovered inside a periodic-sweeper goroutine, labelled by sweeper. MUST stay 0 in healthy production; any > 0 means a deterministic bug has surfaced — see runbook SweepGoroutinePanic.",
	},
	[]string{"sweeper"},
)

// RecordSweepPanic bumps the counter for the named sweeper. Called from
// the cmd/booking-cli loops' defer recover() blocks.
func RecordSweepPanic(sweeper string) {
	sweepGoroutinePanicsTotal.WithLabelValues(sweeper).Inc()
}
