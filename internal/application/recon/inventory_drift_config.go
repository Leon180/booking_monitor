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
	return nil
}
