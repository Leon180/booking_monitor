package observability

// D6 reservation expiry sweeper metrics. Symmetric counterpart to
// recon (A4) and saga watchdog (A5). The application-side
// `expiry.Metrics` interface + `bootstrap.NewPrometheusExpiryMetrics()`
// adapter forward into these vars; expiry application code never
// imports this package directly (CP2 layer-fix).
//
// Naming convention divergences from saga (intentional):
//   - `expiry_oldest_overdue_age_seconds` (gauge), NOT
//     `expiry_stuck_overdue_orders` — saga's gauge is "count of stuck
//     rows" but a healthy high-traffic sweeper has non-zero count
//     every tick (rows that just hit reserved_until); the operator
//     signal that matters is "are we falling behind?", which oldest
//     age captures directly. (Codex round-1 P2 fix.)
//   - `expiry_resolve_duration_seconds` (per-row, mirrors
//     `saga_watchdog_resolve_duration_seconds`) AND
//     `expiry_sweep_duration_seconds` (full Sweep() wall time, NEW —
//     no saga analogue). Distinct semantics per Codex round-3 F4:
//     operators graph "is sweep falling behind?" (full-sweep > tick)
//     separately from "are individual rows slow?" (per-row p95).
//   - `expiry_find_expired_errors_total` covers BOTH the find-query
//     failure AND the post-sweep count-query failure (same DB-blind
//     shape; round-2 F3 fold).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ExpirySweepResolvedTotal counts per-row resolution outcomes by
// label. Pre-warmed (see metrics_init.go) so dashboards don't show
// "no data" for a label that genuinely had zero events.
//
// Outcomes (mirror `expiry.AllOutcomes`):
//
//	"expired"            — happy path: row transitioned + outbox emitted
//	"expired_overaged"   — same as expired, but row was past MaxAge.
//	                       Increments alongside `ExpiryMaxAgeTotal`.
//	"already_terminal"   — row no longer awaiting_payment when D6
//	                       reached it (D5 race won, pre-UoW or
//	                       inside UoW via ErrInvalidTransition).
//	                       Benign; no order.failed emit.
//	"getbyid_error"      — orderRepo.GetByID failed before the UoW.
//	                       Operator: check DB.
//	"marshal_error"      — json.Marshal of OrderFailedEvent failed
//	                       (theoretical).
//	"outbox_error"       — Outbox.Create failed inside UoW. Note:
//	                       at the call site we cannot distinguish
//	                       outbox_error from transition_error
//	                       without a sentinel from inside the
//	                       closure — both currently fall under
//	                       transition_error. Pre-warmed for future
//	                       refinement.
//	"transition_error"   — MarkExpired returned a non-ErrInvalidTransition
//	                       error inside UoW. Will retry next sweep.
var ExpirySweepResolvedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "expiry_sweep_resolved_total",
		Help: "Total D6 expiry sweeper per-row resolution outcomes",
	},
	[]string{"outcome"},
)

// ExpiryMaxAgeTotal — increments alongside `expired_overaged`
// outcome. Distinct counter (NOT a label on the resolved-vector) so
// the `ExpiryMaxAgeExceeded` alert can fire on a thin slice without
// filtering the busy resolved vector. Operator awareness only — D6
// has already expired the row by the time this fires; the alert
// asks the operator to investigate WHY rows aged past MaxAge before
// a sweep caught them (sweeper down? deploy stuck?).
var ExpiryMaxAgeTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "expiry_max_age_total",
		Help: "Reservations expired AFTER exceeding EXPIRY_MAX_AGE — operator awareness, not gating",
	},
)

// ExpiryFindExpiredErrorsTotal — per-sweep DB-blind failure counter.
// Covers BOTH the find-query (`FindExpiredReservations`) AND the
// post-sweep count-query (`CountOverdueAfterCutoff`) failures —
// same operator response (check DB), same alert
// (`ExpiryFindErrors`). On count-query failure the backlog gauges
// are intentionally LEFT at last-known-good (round-3 F2 contract);
// this counter is the truth signal that the gauge values may be
// stale.
var ExpiryFindExpiredErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "expiry_find_expired_errors_total",
		Help: "Total FindExpiredReservations OR CountOverdueAfterCutoff query failures",
	},
)

// ExpiryOldestOverdueAgeSeconds — gauge, set per sweep to the age
// (NOW - reserved_until) of the oldest still-overdue row.
//
// Two writes per sweep, both intentional:
//  1. Sweep START — set to oldest from FindExpiredReservations[]
//     so dashboards reflect the pre-resolve view.
//  2. Sweep END — set to oldest from CountOverdueAfterCutoff so
//     dashboards reflect what's STILL overdue after this batch
//     (0 if drained; non-zero = backlog persists).
//
// Pairs with the `ExpiryOldestOverdueAge > 300 for 5m` alert. 0 in
// steady state.
var ExpiryOldestOverdueAgeSeconds = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "expiry_oldest_overdue_age_seconds",
		Help: "Age (NOW - reserved_until) of oldest still-overdue awaiting_payment row, set on each sweep",
	},
)

// ExpiryBacklogAfterSweep — gauge set at sweep END to the count of
// rows still past the cutoff after we processed our batch. 0 in
// steady state; non-zero = BatchSize too small for current
// eligibility rate OR per-row processing erroring out.
//
// **Held at last-known-good on count-query failure** (round-3 F2
// contract). The companion `ExpiryFindErrors` alert tells operators
// "current value may be stale" when that fires.
var ExpiryBacklogAfterSweep = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "expiry_backlog_after_sweep",
		Help: "Count of rows still past cutoff after the latest sweep batch (0 in steady state)",
	},
)

// ExpiryResolveDurationSeconds — per-row resolve wall time. Includes
// GetByID + status check + UoW {MarkExpired + outbox.Create}. Pairs
// with the saga / recon resolve histograms for cookie-cutter
// dashboards.
var ExpiryResolveDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "expiry_resolve_duration_seconds",
		Help:    "Wall-clock duration of a single D6 expiry resolve operation (per row)",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)

// ExpirySweepDurationSeconds — full Sweep() call wall time. Includes
// FindExpiredReservations + per-row resolve loop + post-sweep
// CountOverdueAfterCutoff. Operator dashboard signal: a Sweep that
// exceeds SweepInterval = sweeper falling behind even if individual
// resolves are fast (round-3 F4 distinct semantic from
// resolve-duration).
var ExpirySweepDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "expiry_sweep_duration_seconds",
		Help:    "Wall-clock duration of a full D6 expiry Sweep() call (find + resolve loop + post-sweep count)",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
	},
)

// ExpiryResolveAgeSeconds — age of an overdue reservation at the
// moment the sweeper resolved it. SLO signal: in steady state p95
// should sit under SweepInterval + ExpiryGracePeriod + small
// constant. Buckets span 1s … 24h since `expired_overaged` rows
// (age > MaxAge) feed this same histogram.
var ExpiryResolveAgeSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "expiry_resolve_age_seconds",
		Help:    "Age (NOW - reserved_until) of overdue reservations at resolve time",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 21600, 86400},
	},
)
