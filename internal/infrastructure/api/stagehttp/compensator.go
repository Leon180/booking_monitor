// Package stagehttp houses the HTTP layer shared across D12 Stages
// 1-3 of the comparison harness (per docs/d12/README.md). All three
// stages preserve the same Pattern A two-step API contract — book →
// /pay → confirm-{succeeded|failed} → terminal — so scripts/k6_two_
// step_flow.js runs unmodified across them. What differs across
// stages is the underlying booking.Service implementation (the
// success-path seam) and the Compensator implementation (the
// failure-path seam); the HTTP route handlers themselves are
// shared in this package.
//
// Stage 4 is NOT a consumer of this package — Stage 4 has its own
// production handler at internal/infrastructure/api/booking/ with
// the full middleware chain (idempotency-key fingerprinting,
// distributed tracing, full payment.Service + event.Service
// integration). The stage 1-3 handlers here are deliberately
// simpler — they're benchmarking baselines, not the canonical
// production shape.
package stagehttp

import (
	"booking_monitor/internal/application/expiry"
)

// Compensator is the failure-path architectural seam across Stages 1-3
// and Stage 5. Defined in internal/application/expiry so Stage 5
// (production path) can use it without importing a "stage HTTP" package.
// This alias keeps Stage 1-3 binaries compile-compatible — all existing
// implementations and call sites continue to work unchanged.
//
// Stage-by-stage PG-symmetry rule: the compensator's UPDATE
// event_ticket_types behaviour MUST match the stage's forward-path PG
// behaviour. See expiry.Compensator doc for the full contract.
type Compensator = expiry.Compensator

// ErrCompensateNotEligible re-exports expiry.ErrCompensateNotEligible
// so Stage 1-3 binaries and stagehttp handlers that already import
// stagehttp need not be updated. It is the same sentinel pointer —
// errors.Is checks against either name are equivalent.
var ErrCompensateNotEligible = expiry.ErrCompensateNotEligible
