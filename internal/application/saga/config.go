package saga

import (
	"errors"
	"fmt"
	"time"
)

// Config is the application-layer view of the watchdog tunables —
// plain Go, no yaml/env tags. The wire-format counterpart with
// cleanenv tags lives in `internal/infrastructure/config.SagaConfig`;
// bootstrap (or each cmd entry's fx wiring) translates one to the
// other at process startup. Application code MUST NOT import
// infrastructure/config directly — that's the layer-violation
// finding (A1 / row 10) the Phase 2 checkpoint flagged.
//
// All defaults / rationale stay in the infrastructure-side struct's
// header comments because that's where they manifest as env-var
// defaults; this struct is just the consumed value.
type Config struct {
	// WatchdogInterval controls how often the loop fires.
	WatchdogInterval time.Duration

	// StuckThreshold is the minimum age a Failed row must have
	// before the watchdog considers it stuck and re-drives the
	// compensator.
	StuckThreshold time.Duration

	// MaxFailedAge is the give-up cutoff. Above this age the
	// watchdog logs + counts but does NOT auto-transition (unlike
	// the reconciler's force-fail path) — moving Failed →
	// Compensated without verifying inventory was reverted is
	// unsafe. Operators investigate manually.
	MaxFailedAge time.Duration

	// BatchSize bounds orders per sweep cycle.
	BatchSize int
}

// ErrInvalidSagaConfig signals a misconfigured Config. Returned via
// errors.Is so callers can react to "any config error" without
// matching specific messages.
var ErrInvalidSagaConfig = errors.New("invalid saga config")

// Validate enforces cross-field invariants on Config. Called at
// process startup (NOT every sweep) — wiring code that builds a
// Config MUST surface validation failure as a fatal startup error
// rather than silently running with broken tunables.
//
// The most consequential check is `MaxFailedAge > StuckThreshold`:
// a misconfigured stack where MaxFailedAge ≤ StuckThreshold flags
// every stuck order as max-age-exceeded on the very first sweep,
// suppressing the legitimate retry path entirely.
func (c Config) Validate() error {
	if c.WatchdogInterval <= 0 {
		return fmt.Errorf("%w: WatchdogInterval must be > 0 (got %s)", ErrInvalidSagaConfig, c.WatchdogInterval)
	}
	if c.StuckThreshold <= 0 {
		return fmt.Errorf("%w: StuckThreshold must be > 0 (got %s)", ErrInvalidSagaConfig, c.StuckThreshold)
	}
	if c.MaxFailedAge <= c.StuckThreshold {
		return fmt.Errorf("%w: MaxFailedAge (%s) must be > StuckThreshold (%s); otherwise every stuck order is force-flagged on first sweep",
			ErrInvalidSagaConfig, c.MaxFailedAge, c.StuckThreshold)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("%w: BatchSize must be > 0 (got %d)", ErrInvalidSagaConfig, c.BatchSize)
	}
	return nil
}
