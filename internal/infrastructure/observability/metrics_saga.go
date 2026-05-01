package observability

// A5 Saga Watchdog metrics. Symmetric counterpart to recon: reconciler
// resolves stuck-Charging via gateway probes; watchdog resolves
// stuck-Failed via compensator re-drive. Counter / gauge / histogram
// naming mirrors the recon pattern so dashboards / alerts can be
// cookie-cuttered.
//
// The application-side `saga.Metrics` interface +
// `bootstrap.NewPrometheusSagaMetrics()` adapter forward into these
// vars; saga application code never imports this package directly
// (CP2 layer-fix).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SagaStuckFailedOrders is a gauge of orders currently in Failed
// state older than SAGA_STUCK_THRESHOLD. Reset to the latest count on
// every watchdog sweep (point-in-time, not cumulative). Pairs with
// the alert `saga_stuck_failed_orders > 0 for 10m` — sustained value
// = compensator falling behind or saga consumer is silently dropping
// events.
var SagaStuckFailedOrders = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "saga_stuck_failed_orders",
		Help: "Failed orders older than SAGA_STUCK_THRESHOLD, set on each watchdog sweep",
	},
)

// SagaWatchdogResolvedTotal counts watchdog resolution outcomes by
// label. Pre-warmed at startup so dashboards don't show "no data"
// for a label that genuinely had zero events.
//
// Outcomes (each maps to a distinct operator runbook so triage points
// at the right subsystem; pre-fixup-review a single `compensator_error`
// label conflated three failure modes):
//
//	"compensated"        — compensator successfully re-drove the
//	                       order from Failed → Compensated
//	"already_compensated" — race won by the saga consumer between
//	                       FindStuckFailed and the watchdog's
//	                       GetByID; row already Compensated. Benign.
//	"max_age_exceeded"   — order older than SAGA_MAX_FAILED_AGE; we
//	                       log + count + alert but do NOT auto-
//	                       transition. Operator investigates manually.
//	"getbyid_error"      — orderRepo.GetByID failed before we reached
//	                       the compensator. Operator should check DB
//	                       health, NOT Redis or compensator code.
//	"marshal_error"      — json.Marshal of synthesized OrderFailedEvent
//	                       failed. Theoretical for the fixed-shape
//	                       struct today; isolated label so a future
//	                       regression is visible.
//	"compensator_error"  — compensator.HandleOrderFailed returned an
//	                       error. Operator should check Redis revert
//	                       path + DB lock contention. Will retry next
//	                       sweep.
var SagaWatchdogResolvedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "saga_watchdog_resolved_total",
		Help: "Total saga watchdog resolution outcomes",
	},
	[]string{"outcome"},
)

// SagaWatchdogFindStuckErrorsTotal — per-sweep FindStuckFailed query
// failures. Same gap-closing role as ReconFindStuckErrorsTotal: a
// stale gauge alone can't distinguish "no orders are stuck" from
// "the watchdog itself is broken". This counter is the
// "watchdog-itself-broken" signal.
var SagaWatchdogFindStuckErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "saga_watchdog_find_stuck_errors_total",
		Help: "Total FindStuckFailed query failures (DB outage, missing index, timeout)",
	},
)

// SagaWatchdogResolveDurationSeconds — wall-clock for resolving one
// stuck-Failed order via the compensator. Pairs with the recon
// equivalent for symmetric tuning dashboards.
var SagaWatchdogResolveDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "saga_watchdog_resolve_duration_seconds",
		Help:    "Wall-clock duration of a single saga watchdog resolve operation",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)

// SagaWatchdogResolveAgeSeconds — age of a stuck-Failed order at the
// moment the watchdog picked it up. Drives SAGA_STUCK_THRESHOLD
// tuning: if p50 is consistently 90s while threshold is 60s, the
// watchdog is catching orders 30s after the threshold — likely the
// saga consumer's normal Failed→Compensated path is slow and we
// should INCREASE the threshold (avoid stealing from the consumer).
var SagaWatchdogResolveAgeSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "saga_watchdog_resolve_age_seconds",
		Help:    "Age of stuck-Failed orders at the moment the watchdog resolves them",
		Buckets: []float64{30, 60, 90, 120, 180, 300, 600, 1800, 3600, 21600, 86400}, // 30s … 24h
	},
)
