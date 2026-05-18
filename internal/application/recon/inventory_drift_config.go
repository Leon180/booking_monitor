package recon

import (
	"errors"
	"fmt"
	"time"
)

// DriftConfig holds the InventoryDriftDetector tunables. Same shape
// rationale as recon.Config: plain Go (no yaml/env tags) so the
// application layer doesn't import infrastructure/config — the
// translator + Validate-at-startup contract live in the bootstrap
// layer.
//
// The per-detector Config / Metrics split (vs reusing Reconciler's)
// is deliberate: the two sweepers SHOULD tune independently.
// Reconciler queries an external gateway (slow, rate-limited; needs
// generous timeouts and small batches); the drift detector reads the
// local Redis pool (sub-ms; can afford fast cadence on a much larger
// scan).
type DriftConfig struct {
	// SweepInterval is how often the drift detector loop fires when
	// running in default loop mode. Faster than ReconConfig.SweepInterval
	// is fine — Redis GET is sub-ms; the bound is "noise-floor / sweep
	// vs operational utility".
	SweepInterval time.Duration

	// AbsoluteTolerance is the |drift| ceiling for the `cache_low_excess`
	// branch — drift values within this band are NOT flagged. Models
	// the steady-state in-flight-bookings buffer between Lua deduct and
	// worker-commit. Pick from observed peak in-flight booking count.
	AbsoluteTolerance int

	// AutoRehydrate, when true, causes the detector to refresh the immutable
	// metadata key (HSET ticket_type_meta:{id}) when a cache_missing qty key
	// is detected. The qty key itself is intentionally NOT restored: SETNX
	// from a stale DB value during an active write-behind window (in-flight
	// deductions between Lua deduct and worker DB-commit) produces Redis qty >
	// DB qty → cache_high, which is P0 in flash-sale context (false 202
	// Accepted). The metadata snapshot (event_id, amount_cents, currency) is
	// immutable after creation, making HSET always safe.
	//
	// Qty restoration is operator-gated via startup rehydrate only.
	// Only applies to cache_missing. cache_high is never auto-corrected.
	AutoRehydrate bool

	// MaxConsecutiveFailures is how many consecutive Sweep errors the
	// cmd-side runner tolerates before it escalates to fx.Shutdown.
	// A permanently-broken detector (PG / Redis permission error, schema
	// drift) would otherwise emit a log storm while the
	// `inventory_drift_*` gauges stayed blank — operators relying on
	// those gauges would see absence instead of a firing alert.
	// Escalating to fx.Shutdown surfaces the misconfiguration via
	// k8s CrashLoopBackOff rather than silently degrading.
	//
	// Must be > 0. Default 5 (at a 60s sweep interval → ~5 min).
	MaxConsecutiveFailures int
}

// ErrInvalidDriftConfig signals a misconfigured DriftConfig. Mirrors
// ErrInvalidReconConfig — exported via errors.Is for caller branching.
var ErrInvalidDriftConfig = errors.New("invalid inventory drift config")

// Validate enforces cross-field invariants. Called at process startup
// (NOT every sweep) — wiring code that builds a DriftConfig MUST surface
// validation failure as a fatal startup error.
//
// AbsoluteTolerance >= 0 is the only invariant: a negative tolerance
// is meaningless (would flag every event including the within-noise
// healthy ones). Zero IS allowed — strict mode that flags every drift,
// useful in tests and idle production where in-flight bookings are
// expected to be zero.
func (c DriftConfig) Validate() error {
	if c.SweepInterval <= 0 {
		return fmt.Errorf("%w: SweepInterval must be > 0 (got %s)", ErrInvalidDriftConfig, c.SweepInterval)
	}
	if c.AbsoluteTolerance < 0 {
		return fmt.Errorf("%w: AbsoluteTolerance must be >= 0 (got %d)", ErrInvalidDriftConfig, c.AbsoluteTolerance)
	}
	if c.MaxConsecutiveFailures <= 0 {
		return fmt.Errorf("%w: MaxConsecutiveFailures must be > 0 (got %d)", ErrInvalidDriftConfig, c.MaxConsecutiveFailures)
	}
	return nil
}
