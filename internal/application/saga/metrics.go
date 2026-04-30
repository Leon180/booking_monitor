package saga

// Metrics is the watchdog-side observability port — every metric
// emission inside `Watchdog.Sweep` / `Watchdog.resolve` flows through
// this interface, NOT through `infrastructure/observability` global
// vars. Decouples the application layer from the Prometheus singleton
// (the layer-violation finding A1 / row 10 from the Phase 2
// checkpoint).
//
// The interface is wider than the project's "1-3 method" guideline,
// but every method serves the same cohesive role: emit a single
// observation each from the watchdog sweep loop. Splitting into
// per-method interfaces would just multiply the type surface without
// any caller actually composing them — this matches the
// `BookingMetrics` / `WorkerMetrics` precedent already established
// in the codebase.
//
// The Prometheus-backed adapter lives in the bootstrap layer (see
// `cmd/booking-cli/saga_watchdog.go` or `internal/bootstrap/`); it
// holds the global `prometheus.Counter` / `Histogram` / `Gauge` refs
// from `infrastructure/observability` and forwards each Metrics call
// to the right one. Tests substitute `NopMetrics` (zero behaviour)
// or a fake that captures call sequences for assertion.
//
// Contract notes:
//   - SetStuckFailedOrders is a Gauge SET, not Add — point-in-time,
//     overwritten every sweep.
//   - IncResolved's `outcome` MUST be one of:
//     "compensated", "already_compensated", "max_age_exceeded",
//     "getbyid_error", "marshal_error", "compensator_error".
//     The watchdog passes these as string literals; the adapter
//     pre-warms the corresponding label values at startup so dashboards
//     never show "no data" before traffic arrives.
type Metrics interface {
	// SetStuckFailedOrders updates the stuck-Failed gauge with the
	// current (point-in-time) backlog size from the latest sweep.
	SetStuckFailedOrders(count int)

	// IncResolved bumps the per-outcome resolution counter.
	IncResolved(outcome string)

	// IncFindStuckErrors bumps the FindStuckFailed query-error
	// counter. Distinct signal from the resolution outcomes —
	// "we cannot even SCAN for stuck orders" vs "we tried to
	// resolve and the compensator failed".
	IncFindStuckErrors()

	// ObserveResolveDuration records the wall-clock cost of one
	// per-order resolve call (re-drive of the compensator).
	ObserveResolveDuration(seconds float64)

	// ObserveResolveAge records the age of the row at the moment
	// the watchdog resolved it (for SLO reporting on time-to-recovery).
	ObserveResolveAge(seconds float64)
}

// NopMetrics is the zero-behaviour Metrics implementation — useful
// for tests that don't want to assert metric calls and for any
// future code path that needs a Watchdog without observability
// (e.g., a unit-test scaffold). Mirrors `prometheus.NewNop*` and
// `mlog.NewNop()` patterns already in the codebase.
type NopMetrics struct{}

func (NopMetrics) SetStuckFailedOrders(int)         {}
func (NopMetrics) IncResolved(string)               {}
func (NopMetrics) IncFindStuckErrors()              {}
func (NopMetrics) ObserveResolveDuration(float64)   {}
func (NopMetrics) ObserveResolveAge(float64)        {}
