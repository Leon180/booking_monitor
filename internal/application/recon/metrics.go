package recon

// Metrics is the reconciler's observability port — every metric
// emission inside `Reconciler.Sweep` / `.resolve` / `.failOrder`
// flows through this interface, NOT through `infrastructure/observability`
// global vars. Decouples the application layer from the Prometheus
// singleton (the layer-violation finding A1 / row 10 from the
// Phase 2 checkpoint).
//
// The interface is wider than the project's "1-3 method" guideline
// but every method serves the same cohesive role: emit a single
// observation each from the recon sweep loop. Splitting into
// per-method interfaces would multiply the type surface without any
// caller actually composing them — matches the `BookingMetrics` /
// `WorkerMetrics` precedent already established in the codebase.
//
// The Prometheus-backed adapter lives outside this package (bootstrap
// or cmd/booking-cli/recon.go); it holds the global `prometheus.Counter`
// / `Histogram` / `Gauge` refs from `infrastructure/observability`
// and forwards each Metrics call to the right one. Tests substitute
// `NopMetrics` (zero behaviour) or a fake that captures call sequences.
//
// Contract notes:
//   - SetStuckChargingOrders is a Gauge SET, not Add — point-in-time,
//     overwritten every sweep.
//   - IncResolved's `outcome` MUST be one of:
//     "charged", "declined", "not_found", "unknown",
//     "max_age_exceeded", "transition_lost".
//     The reconciler passes these as string literals; the adapter
//     pre-warms the corresponding label values at startup so dashboards
//     never show "no data" before traffic arrives.
type Metrics interface {
	// SetStuckChargingOrders updates the stuck-Charging gauge with
	// the current (point-in-time) backlog from the latest sweep.
	SetStuckChargingOrders(count int)

	// IncFindStuckErrors bumps the FindStuckCharging query-error
	// counter. Distinct signal from per-order outcomes — "we cannot
	// even SCAN for stuck orders" vs "we tried to resolve and the
	// gateway/DB failed".
	IncFindStuckErrors()

	// IncGatewayErrors bumps the gateway.GetStatus infrastructure-
	// failure counter. Distinct from the `unknown` resolution
	// outcome (the latter is "gateway answered but verdict was
	// unclassifiable"; this counter is "gateway didn't answer").
	IncGatewayErrors()

	// IncResolved bumps the per-outcome resolution counter (see the
	// outcome list above).
	IncResolved(outcome string)

	// IncMarkErrors bumps the Mark*-or-outbox-emit counter. Catches
	// real DB failures inside the resolve path (excluding
	// ErrInvalidTransition, which is the benign worker-won-the-race
	// signal counted under outcome="transition_lost").
	IncMarkErrors()

	// IncNullIntentIDSkipped bumps the dedicated counter for stuck-
	// charging rows where the gateway-side payment intent ID is empty
	// — caused by the documented `SetPaymentIntentID` race in
	// `internal/application/payment/service.go:166` (the
	// `CreatePaymentIntent` call to the gateway succeeded but the
	// follow-on UPDATE that persisted the intent ID failed). The
	// reconciler can't call Stripe's GetStatus without an intent ID,
	// so it skips the row, leaves it in `charging`, and increments
	// this counter so ops can find the orphan via Stripe dashboard
	// (search by metadata.order_id) and reconcile manually.
	//
	// Distinct counter (not folded into IncResolved) because the
	// alert criterion is "rate > 0 for 5m → page" — any non-zero
	// rate is operationally interesting; the "transition_lost"
	// outcome under IncResolved is benign and shouldn't share an
	// alert with this one.
	IncNullIntentIDSkipped()

	// ObserveResolveDuration records the wall-clock cost of one
	// per-order resolve call.
	ObserveResolveDuration(seconds float64)

	// ObserveResolveAge records the age of the row at the moment
	// the reconciler resolved it (for SLO reporting on
	// time-to-recovery).
	ObserveResolveAge(seconds float64)

	// ObserveGatewayDuration records the wall-clock cost of one
	// gateway.GetStatus call (independent of the surrounding
	// resolve duration so we can isolate gateway-side latency).
	ObserveGatewayDuration(seconds float64)
}

// NopMetrics is the zero-behaviour Metrics implementation — useful
// for tests that don't want to assert metric calls. Mirrors the
// `prometheus.NewNop*` and `mlog.NewNop()` patterns already in the
// codebase.
type NopMetrics struct{}

func (NopMetrics) SetStuckChargingOrders(int)     {}
func (NopMetrics) IncFindStuckErrors()            {}
func (NopMetrics) IncGatewayErrors()              {}
func (NopMetrics) IncResolved(string)             {}
func (NopMetrics) IncMarkErrors()                 {}
func (NopMetrics) IncNullIntentIDSkipped()        {}
func (NopMetrics) ObserveResolveDuration(float64) {}
func (NopMetrics) ObserveResolveAge(float64)      {}
func (NopMetrics) ObserveGatewayDuration(float64) {}
