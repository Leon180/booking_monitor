package recon

import (
	"errors"
	"fmt"
	"time"
)

// Config is the application-layer view of the reconciler tunables —
// plain Go, no yaml/env tags. The wire-format counterpart with
// cleanenv tags lives in `internal/infrastructure/config.ReconConfig`;
// bootstrap (or each cmd entry's fx wiring) translates one to the
// other at process startup. Application code MUST NOT import
// infrastructure/config directly — that's the layer-violation
// finding (A1 / row 10) the Phase 2 checkpoint flagged.
//
// All defaults / rationale stay in the infrastructure-side struct's
// header comments because that's where they manifest as env-var
// defaults; this struct is just the consumed value.
type Config struct {
	// SweepInterval controls how often the loop fires when running
	// in default loop mode.
	SweepInterval time.Duration

	// ChargingThreshold is the minimum age a Charging order must
	// have before recon considers it stuck and queries the gateway.
	// Set above the typical successful Pending→Charging→Confirmed
	// duration to avoid stealing in-flight orders from the worker.
	ChargingThreshold time.Duration

	// GatewayTimeout bounds the per-order GetStatus call. Without
	// a budget, a hung gateway pins the whole sweep loop and
	// blocks SIGTERM-driven shutdown indefinitely.
	GatewayTimeout time.Duration

	// MaxChargingAge is the give-up cutoff. A Charging order older
	// than this is force-failed via `failOrder` (the DEF-CRIT path:
	// MarkFailed + outbox.Create("order.failed") in one UoW so the
	// saga compensator reverts Redis inventory).
	MaxChargingAge time.Duration

	// BatchSize bounds orders processed per sweep cycle.
	BatchSize int
}

// ErrInvalidReconConfig signals a misconfigured Config. Returned via
// errors.Is so callers can react to "any config error" without
// matching specific messages.
var ErrInvalidReconConfig = errors.New("invalid recon config")

// Validate enforces cross-field invariants. Called at process startup
// (NOT every sweep) — wiring code that builds a Config MUST surface
// validation failure as a fatal startup error rather than silently
// running with broken tunables.
//
// Most consequential check: `MaxChargingAge > ChargingThreshold`. A
// misconfigured stack where MaxChargingAge ≤ ChargingThreshold
// force-fails every stuck order on the first sweep — never queries
// the gateway, never resolves naturally, drains every Charging row
// straight to Failed.
func (c Config) Validate() error {
	if c.SweepInterval <= 0 {
		return fmt.Errorf("%w: SweepInterval must be > 0 (got %s)", ErrInvalidReconConfig, c.SweepInterval)
	}
	if c.ChargingThreshold <= 0 {
		return fmt.Errorf("%w: ChargingThreshold must be > 0 (got %s)", ErrInvalidReconConfig, c.ChargingThreshold)
	}
	if c.GatewayTimeout <= 0 {
		return fmt.Errorf("%w: GatewayTimeout must be > 0 (got %s)", ErrInvalidReconConfig, c.GatewayTimeout)
	}
	if c.MaxChargingAge <= c.ChargingThreshold {
		return fmt.Errorf("%w: MaxChargingAge (%s) must be > ChargingThreshold (%s); otherwise every stuck order is force-failed on first sweep",
			ErrInvalidReconConfig, c.MaxChargingAge, c.ChargingThreshold)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("%w: BatchSize must be > 0 (got %d)", ErrInvalidReconConfig, c.BatchSize)
	}
	return nil
}
