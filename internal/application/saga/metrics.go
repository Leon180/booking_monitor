package saga

import "time"

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

func (NopMetrics) SetStuckFailedOrders(int)       {}
func (NopMetrics) IncResolved(string)             {}
func (NopMetrics) IncFindStuckErrors()            {}
func (NopMetrics) ObserveResolveDuration(float64) {}
func (NopMetrics) ObserveResolveAge(float64)      {}

// CompensatorMetrics is the SAGA-COMPENSATOR-side observability
// port (sibling to `Metrics` above which is watchdog-side only).
// Added in PR-D12.4 because the Kafka-driven compensator hot path
// (`Compensator.HandleOrderFailed`) was previously uninstrumented
// — operators had no metrics for compensation throughput, error
// rates by class, or consumer lag.
//
// Why a SIBLING interface rather than extending `Metrics`:
//
//   - The watchdog and compensator are functionally separate
//     subsystems (DB-side sweeper vs Kafka-driven hot path).
//     Conflating them would force the watchdog to mock
//     compensator methods in tests and vice versa.
//   - Decision 2 from the PR-D12.4 plan + plan-stage agent review
//     CRITICAL #2: the metrics are injected DIRECTLY into the
//     compensator, NOT via a decorator. Three of the compensator's
//     return paths (compensated / already_compensated /
//     path_c_skipped) all return `nil`, indistinguishable from
//     the outside. The compensator must self-record its outcome
//     before each return.
//
// Method signatures use `time.Duration` rather than `float64
// seconds` (the watchdog's existing convention). Pinning the
// type at the application boundary instead of asking callers to
// `.Seconds()` is the more idiomatic Go shape; the adapter at
// `internal/bootstrap/sweeper_adapters.go` does the conversion
// to float64 for the Prometheus histogram/gauge.
//
// Outcome label values are documented in `compensator.go` next
// to the call sites (Slice 2). The adapter's pre-warm in
// `internal/infrastructure/observability/metrics_init.go`
// enumerates them so dashboards show 0 for unseen outcomes
// instead of "no data".
type CompensatorMetrics interface {
	// RecordEventProcessed bumps the per-outcome counter. Called
	// at every return path of `Compensator.HandleOrderFailed`
	// (the "did_record" sentinel pattern in Slice 2 enforces
	// no-fall-through). Exact label values pinned by the
	// integration test in Slice 4.
	RecordEventProcessed(outcome string)

	// ObserveLoopDuration records end-to-end compensation latency
	// from `events_outbox.created_at` (threaded through
	// `kafka.Message.Time` per Slice 0's data-path foundation) to
	// the moment the compensator commits MarkCompensated. ONLY
	// observed on the success path — `already_compensated` and
	// `path_c_skipped` are no-op return paths whose sub-millisecond
	// durations would skew p50/p99 to the floor and hide real
	// degradation.
	ObserveLoopDuration(d time.Duration)

	// SetConsumerLag updates the consumer-lag gauge with
	// `time.Since(msg.Time)` for the most-recently-processed
	// message. PERFORMANCE metric, NOT liveness — a crashed
	// consumer leaves this gauge stale at the last value.
	// Liveness is observed via `up == 0`. The
	// `SagaConsumerLagHigh` alert (Slice 3) is gated on `up == 1`
	// to prevent false positives on consumer crash.
	//
	// In multi-replica deployments (future Phase 4), each replica
	// sets the gauge independently; PromQL queries should use
	// `max by (instance)` to aggregate. Documented in the
	// Prometheus Help text.
	SetConsumerLag(d time.Duration)
}

// NopCompensatorMetrics is the zero-behaviour
// `CompensatorMetrics` implementation. Used in tests and any
// future code path that needs a Compensator without
// observability. Mirrors `NopMetrics` above.
type NopCompensatorMetrics struct{}

func (NopCompensatorMetrics) RecordEventProcessed(string) {}
func (NopCompensatorMetrics) ObserveLoopDuration(time.Duration) {}
func (NopCompensatorMetrics) SetConsumerLag(time.Duration) {}
