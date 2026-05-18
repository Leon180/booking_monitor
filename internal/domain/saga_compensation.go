package domain

import (
	"context"

	"github.com/google/uuid"
)

// SagaCompensationRepository persists the two-phase completion state of
// a saga compensation: first the DB-side record (written atomically with
// MarkCompensated inside a UoW), then the Redis-revert flag
// (written best-effort after a successful revert.lua call).
//
// This is the PG-based idempotency guard that survives Redis crashes:
// when a redelivered order.failed event finds wasAlreadyCompensated=true,
// we check redis_reverted_at before deciding whether to call revert.lua
// again. revert.lua's own EXISTS guard remains as defense-in-depth.
//
//go:generate mockgen -source=saga_compensation.go -destination=../mocks/saga_compensation_repository_mock.go -package=mocks
type SagaCompensationRepository interface {
	// RecordCompletion inserts (compensation_id, order_id) atomically.
	// ON CONFLICT DO NOTHING — safe for Kafka redelivery.
	// Must be called inside a UoW alongside MarkCompensated.
	// Returns (rowsAffected, error); caller logs Warn when rowsAffected==0.
	RecordCompletion(ctx context.Context, compensationID string, orderID uuid.UUID) (int64, error)

	// WasRedisReverted returns true when redis_reverted_at IS NOT NULL.
	// Returns (false, nil) when the row doesn't exist — treated as
	// "pending" so the caller proceeds to run RevertInventory.
	WasRedisReverted(ctx context.Context, compensationID string) (bool, error)

	// MarkRedisReverted sets redis_reverted_at = NOW(). Best-effort;
	// failure is logged + metered but does not fail the compensation.
	MarkRedisReverted(ctx context.Context, compensationID string) error
}
