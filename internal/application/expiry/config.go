package expiry

import (
	"errors"
	"fmt"
	"time"
)

// Config is the application-layer view of the D6 expiry sweeper
// tunables — plain Go, no yaml/env tags. The wire-format counterpart
// with cleanenv tags lives in
// `internal/infrastructure/config.ExpiryConfig`; bootstrap (or each
// cmd entry's fx wiring) translates one to the other at process
// startup. Application code MUST NOT import infrastructure/config
// directly — closes the layer-violation finding A1 / row 10 from the
// Phase 2 checkpoint.
//
// Why no analog of `SagaConfig.StuckThreshold`:
//   D6's eligibility check is `reserved_until <= NOW() - GracePeriod`
//   — the threshold IS the deadline itself, not an additional
//   "give-the-consumer-a-chance" delay. Saga separates StuckThreshold
//   from the deadline because the saga consumer races the watchdog
//   for the same row; D6 has no such consumer-side path (the customer
//   already missed their window).
type Config struct {
	// SweepInterval controls how often the loop fires.
	SweepInterval time.Duration

	// ExpiryGracePeriod is a polite head-start for in-flight D5
	// webhooks. Default 2s; 0 = tight mode (the predicate is then
	// simply `reserved_until <= NOW()`). The grace doesn't change
	// correctness — D5's `MarkPaid` predicate has its own
	// `reserved_until > NOW()` guard so a true late-success still
	// routes to `handleLateSuccess` — but it eliminates noisy
	// benign-race log lines.
	ExpiryGracePeriod time.Duration

	// MaxAge is the labeling/alerting threshold. **Does NOT gate the
	// transition** (round-1 P1 contract). Rows with age > MaxAge get
	// the `expired_overaged` outcome label + bump
	// `expiry_max_age_total`, but they ARE still transitioned to
	// `expired` + emit `order.failed` for saga compensation. The
	// `ExpiryMaxAgeExceeded` alert is operator-awareness only; D6
	// cannot safely skip ancient rows because that re-creates the
	// soft-lock problem D6 exists to solve.
	//
	// Saga's "skip + alert" rationale (Redis state unknown) does NOT
	// apply: D6 doesn't touch Redis directly, the saga compensator
	// owns Redis revert idempotently via the
	// `saga:reverted:order:<id>` SETNX guard.
	MaxAge time.Duration

	// BatchSize bounds orders processed per sweep cycle.
	BatchSize int
}

// ErrInvalidExpiryConfig signals a misconfigured Config. Returned via
// errors.Is so callers can react to "any config error" without
// matching specific messages.
var ErrInvalidExpiryConfig = errors.New("invalid expiry config")

// Validate enforces invariants on Config. Called at process startup
// (NOT every sweep) — wiring code MUST surface validation failure as
// a fatal startup error rather than silently running with broken
// tunables.
//
// `ExpiryGracePeriod` validates `>= 0` (NOT `> 0`): 0 is legal tight
// mode. All other duration / count fields require strictly positive
// values.
//
// No cross-field guard: D6 has no analogous `MaxFailedAge >
// StuckThreshold` invariant because MaxAge doesn't gate the
// transition. A `MaxAge < SweepInterval` config would just cause every
// row to be labeled `expired_overaged` immediately — observability
// noise, not correctness corruption.
func (c Config) Validate() error {
	if c.SweepInterval <= 0 {
		return fmt.Errorf("%w: SweepInterval must be > 0 (got %s)", ErrInvalidExpiryConfig, c.SweepInterval)
	}
	if c.ExpiryGracePeriod < 0 {
		return fmt.Errorf("%w: ExpiryGracePeriod must be >= 0 (got %s); 0 is legal tight mode",
			ErrInvalidExpiryConfig, c.ExpiryGracePeriod)
	}
	if c.MaxAge <= 0 {
		return fmt.Errorf("%w: MaxAge must be > 0 (got %s)", ErrInvalidExpiryConfig, c.MaxAge)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("%w: BatchSize must be > 0 (got %d)", ErrInvalidExpiryConfig, c.BatchSize)
	}
	return nil
}
