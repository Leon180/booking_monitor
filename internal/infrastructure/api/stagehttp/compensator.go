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
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrCompensateNotEligible is returned by Compensator.Compensate
// when the order isn't in `awaiting_payment` state at lock time.
// Callers branch on this so concurrent races (e.g. /confirm-
// succeeded landing between sweeper SELECT and FOR UPDATE) don't
// log as errors.
//
// Defined here (rather than in each stage's cmd binary) so the
// HandleTestConfirm handler can sentinel-check it consistently
// across stages.
var ErrCompensateNotEligible = errors.New("order not awaiting_payment at lock time")

// Compensator is the failure-path architectural seam across Stages
// 1-3. Each stage's binary supplies its own implementation:
//
//   - Stage 1: SQL-only — BEGIN; SELECT FOR UPDATE event_ticket_types;
//     UPDATE available_tickets += qty; UPDATE orders SET
//     status='compensated'; COMMIT.
//   - Stage 2: Redis revert (INCRBY) + the same SQL compensation tx.
//     Order matters — Redis revert must happen before the SQL UPDATE
//     so a /confirm-failed → crash before SQL commit doesn't leave
//     soft-locked Redis inventory.
//   - Stage 3: emit `order.failed` to `orders:stream` (or directly
//     INCRBY Redis + SQL update inline; depends on D12.3 design).
//
// Implementations MUST:
//   - Be idempotent on order_id (sweeper + /confirm-failed can fire
//     for the same row in a tight race).
//   - Return ErrCompensateNotEligible when the order is observed in
//     a non-awaiting_payment state at lock time (caller skips/maps
//     to 409 — NOT a real error).
//   - Respect ctx cancellation so fx-bounded shutdowns don't hang.
type Compensator interface {
	Compensate(ctx context.Context, orderID uuid.UUID) error
}
