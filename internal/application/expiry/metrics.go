package expiry

// Metrics is the D6 sweeper's observability port — every metric
// emission inside `Sweeper.Sweep` / `Sweeper.resolve` flows through
// this interface, NOT through `infrastructure/observability` global
// vars. Mirrors `saga.Metrics` shape; bootstrap layer adapts to
// Prometheus singletons.
//
// Contract notes:
//   - `SetOldestOverdueAge` and `SetBacklogAfterSweep` are Gauge SETs
//     (not Add) — point-in-time, overwritten every sweep. **On post-
//     sweep `CountOverdueAfterCutoff` failure the caller does NOT
//     call these** (gauges held at last-known-good); the failure path
//     calls `IncFindExpiredErrors` instead.
//   - `IncResolved`'s `outcome` MUST be one of:
//       "expired", "expired_overaged", "already_terminal",
//       "getbyid_error", "marshal_error", "outbox_error",
//       "transition_error".
//     The Prometheus adapter pre-warms all 7 at startup so dashboards
//     never show "no data" before traffic.
//   - `IncMaxAgeExceeded` increments alongside the `expired_overaged`
//     outcome — distinct counter so the `ExpiryMaxAgeExceeded` alert
//     can fire without filtering the busy `expiry_sweep_resolved_total`
//     vector.
//   - `IncFindExpiredErrors` covers BOTH the find-query failure
//     (`FindExpiredReservations`) AND the post-sweep count-query
//     failure (`CountOverdueAfterCutoff`) — both are "we cannot SEE
//     the backlog" failures and share the `ExpiryFindErrors` alert.
//   - `ObserveResolveDuration` is per-row; `ObserveSweepDuration` is
//     full Sweep() wall time. Distinct semantics per Codex round-3
//     F4: operators graph "is sweep falling behind?" (full-sweep)
//     separately from "are individual rows slow?" (per-row p95).
type Metrics interface {
	// IncResolved bumps the per-outcome resolution counter.
	IncResolved(outcome string)

	// IncMaxAgeExceeded bumps the dedicated max-age counter alongside
	// the `expired_overaged` outcome.
	IncMaxAgeExceeded()

	// IncFindExpiredErrors bumps the find-query / count-query failure
	// counter — "we cannot SEE the backlog".
	IncFindExpiredErrors()

	// SetOldestOverdueAge updates the gauge to the oldest still-overdue
	// row's age in seconds at sweep start (0 when none).
	SetOldestOverdueAge(seconds float64)

	// SetBacklogAfterSweep updates the gauge to the count of rows still
	// past the cutoff at sweep END (0 in steady state; non-zero means
	// BatchSize too small or rows erroring).
	SetBacklogAfterSweep(count int)

	// ObserveResolveDuration records per-row resolve wall time.
	ObserveResolveDuration(seconds float64)

	// ObserveSweepDuration records full Sweep() wall time. Operator
	// signal: a Sweep that exceeds SweepInterval = sweeper falling
	// behind even if individual resolves are fast.
	ObserveSweepDuration(seconds float64)

	// ObserveResolveAge records the age of the row (relative to
	// reserved_until) at the moment Sweeper resolved it. SLO signal:
	// p95 should sit under SweepInterval + ExpiryGracePeriod + small
	// constant in steady state.
	ObserveResolveAge(seconds float64)
}

// NopMetrics is the zero-behaviour Metrics implementation — useful
// for tests that don't want to assert metric calls and for any
// future code path that needs a Sweeper without observability.
// Mirrors `prometheus.NewNop*` and `mlog.NewNop()` patterns.
type NopMetrics struct{}

func (NopMetrics) IncResolved(string)             {}
func (NopMetrics) IncMaxAgeExceeded()             {}
func (NopMetrics) IncFindExpiredErrors()          {}
func (NopMetrics) SetOldestOverdueAge(float64)    {}
func (NopMetrics) SetBacklogAfterSweep(int)       {}
func (NopMetrics) ObserveResolveDuration(float64) {}
func (NopMetrics) ObserveSweepDuration(float64)   {}
func (NopMetrics) ObserveResolveAge(float64)      {}

// Outcome label constants. The sweeper passes these as string
// literals; centralising them as constants here lets the bootstrap
// adapter pre-warm the same set without drift.
const (
	OutcomeExpired           = "expired"
	OutcomeExpiredOveraged   = "expired_overaged"
	OutcomeAlreadyTerminal   = "already_terminal"
	OutcomeGetByIDError      = "getbyid_error"
	OutcomeMarshalError      = "marshal_error"
	OutcomeOutboxError       = "outbox_error"
	OutcomeTransitionError   = "transition_error"
)

// AllOutcomes is the canonical list of outcome label values. Adapter
// pre-warm consumes this; tests use it to assert exhaustive coverage.
var AllOutcomes = []string{
	OutcomeExpired,
	OutcomeExpiredOveraged,
	OutcomeAlreadyTerminal,
	OutcomeGetByIDError,
	OutcomeMarshalError,
	OutcomeOutboxError,
	OutcomeTransitionError,
}
