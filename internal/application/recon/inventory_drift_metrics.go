package recon

// DriftMetrics is the InventoryDriftDetector's observability port.
// Same layering rationale as recon.Metrics — application code emits
// through this interface, never through `infrastructure/observability`
// globals (closes the A1 layer-violation finding from the Phase 2
// checkpoint).
//
// Contract notes:
//   - SetDriftedEventsCount is a Gauge SET, not Add — point-in-time,
//     overwritten every sweep.
//   - IncDriftDetected's `direction` MUST be one of:
//     "cache_missing", "cache_high", "cache_low_excess".
//     The detector passes these as string literals; the adapter
//     pre-warms the corresponding label values at startup so dashboards
//     never show "no data" before a real drift event arrives.
type DriftMetrics interface {
	// SetDriftedEventsCount updates the per-sweep gauge of events that
	// crossed any of the drift branches in this sweep cycle.
	SetDriftedEventsCount(count int)

	// IncDriftDetected bumps the per-direction counter every time an
	// event is flagged.
	IncDriftDetected(direction string)

	// IncListEventsErrors increments when ListAvailable itself fails.
	// Distinct signal from per-event cache failures — "we can't even
	// SCAN events" is a different operational story from "Redis is
	// flaky". Mirrors recon.Metrics.IncFindStuckErrors.
	IncListEventsErrors()

	// IncCacheReadErrors bumps on per-event GetInventory failures
	// (Redis transient down, pool exhausted, ctx timeout). The sweep
	// continues so other events can still be checked.
	IncCacheReadErrors()

	// ObserveSweepDuration records wall-clock duration of one full
	// detection sweep — drives the "is the detector keeping up"
	// dashboard.
	ObserveSweepDuration(seconds float64)
}

// NopDriftMetrics is the zero-behaviour DriftMetrics. Mirrors
// NopMetrics for the Reconciler — useful in tests that don't assert
// metric calls.
type NopDriftMetrics struct{}

func (NopDriftMetrics) SetDriftedEventsCount(int) {}
func (NopDriftMetrics) IncDriftDetected(string)   {}
func (NopDriftMetrics) IncListEventsErrors()      {}
func (NopDriftMetrics) IncCacheReadErrors()       {}
func (NopDriftMetrics) ObserveSweepDuration(float64) {}
