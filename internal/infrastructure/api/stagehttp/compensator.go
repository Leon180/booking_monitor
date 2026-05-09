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
// 1-3. Each stage's binary supplies its own implementation,
// reflecting the stage's forward-path SoT discipline:
//
//   - Stage 1: PG-only — BEGIN; SELECT FOR UPDATE order;
//     UPDATE event_ticket_types += qty; UPDATE orders SET
//     status='compensated'; COMMIT. Symmetric with Stage 1's
//     SELECT-FOR-UPDATE forward path that decremented PG inventory.
//   - Stage 2: revert.lua + UPDATE orders status only — NO
//     event_ticket_types update. Symmetric with Stage 2's forward
//     path (Redis Lua deduct + sync INSERT, no PG inventory column
//     decrement). Codex round-1 P1 in PR #106 enforced this — an
//     asymmetric compensator that incremented the PG column would
//     drift it +qty per abandon. Order: revert.lua INSIDE the tx,
//     before UPDATE orders, so a Redis failure rolls back the SQL.
//     PR-D12.3 H1 added the `uuid.NullUUID` scan +
//     `sql.ErrNoRows → ErrCompensateNotEligible` guard to handle
//     legacy NULL `ticket_type_id` rows + sweeper-vs-confirm races.
//   - Stage 3: revert.lua + UPDATE event_ticket_types += qty +
//     UPDATE orders status. Stage 3's worker DECREMENTS the PG
//     column inside its UoW (symmetric with Stage 4's worker post-
//     D7), so the compensator MUST increment it back. Order:
//     revert.lua INSIDE the tx, before the UPDATE statements
//     (matches Stage 2's pattern, NOT Stage 4's saga compensator
//     which does PG-first-Redis-after). Same `uuid.NullUUID` +
//     `sql.ErrNoRows` guards as Stage 2; ALSO has the
//     RowsAffected check on `UPDATE event_ticket_types` to catch
//     orphaned ticket_type rows (PG-leak guard from PR-D12.3 H2).
//
// The PG-symmetry rule (compensator's UPDATE event_ticket_types
// behavior must match the stage's forward-path PG behavior) is the
// single most load-bearing invariant across stages 1-3 — drift
// either direction is a permanent inventory leak.
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
