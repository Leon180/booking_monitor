package expiry

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrCompensateNotEligible is returned by Compensator.Compensate when
// the order is not in awaiting_payment state at lock time. Callers
// branch on this so concurrent races (sweeper + /confirm-failed) don't
// log as errors.
var ErrCompensateNotEligible = errors.New("order not awaiting_payment at lock time")

// Compensator is the application-layer port for synchronous in-process
// compensation: Redis revert + PG status update in a single transaction,
// with no outbox emit. Stage 5 uses this instead of the saga compensator
// pattern because it owns both the inventory deduct (worker UoW) and the
// inventory revert (this compensator) in-process.
//
// Implementations MUST be idempotent on orderID (sweeper + /confirm-
// failed can race for the same row). Returns ErrCompensateNotEligible
// when the order is not in awaiting_payment state at lock time.
type Compensator interface {
	Compensate(ctx context.Context, orderID uuid.UUID) error
}
