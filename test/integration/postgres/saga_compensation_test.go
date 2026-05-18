//go:build integration

package pgintegration_test

import (
	"context"
	"testing"

	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for postgresSagaCompensationRepository against a real
// postgres:15-alpine container with all migrations applied.
//
// What these tests cover that unit tests cannot:
//   - ON CONFLICT DO NOTHING idempotency behaviour at the SQL level
//   - redis_reverted_at IS NOT NULL vs NULL scan semantics
//   - sql.ErrNoRows → (false, nil) for an absent row
//   - UPDATE SET redis_reverted_at = NOW() round-trip

func TestSagaCompensation_RecordAndRevert(t *testing.T) {
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewSagaCompensationRepository(h.DB)
	ctx := context.Background()

	// seed a parent order row so the FK is satisfied
	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	eventID := uuid.New()
	h.SeedEvent(t, eventID.String(), "Test Event", 100)
	h.SeedOrder(t, orderID.String(), eventID.String(), "failed")

	compensationID := "order:" + orderID.String()

	// 1. Before any write — WasRedisReverted returns (false, nil)
	done, err := repo.WasRedisReverted(ctx, compensationID)
	require.NoError(t, err)
	assert.False(t, done, "absent row should return false, not error")

	// 2. RecordCompletion inserts the row (rowsAffected=1)
	n, err := repo.RecordCompletion(ctx, compensationID, orderID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// 3. After insert — redis_reverted_at IS NULL → WasRedisReverted returns false
	done, err = repo.WasRedisReverted(ctx, compensationID)
	require.NoError(t, err)
	assert.False(t, done, "redis_reverted_at is NULL; should return false")

	// 4. MarkRedisReverted sets redis_reverted_at
	err = repo.MarkRedisReverted(ctx, compensationID)
	require.NoError(t, err)

	// 5. After MarkRedisReverted — WasRedisReverted returns true
	done, err = repo.WasRedisReverted(ctx, compensationID)
	require.NoError(t, err)
	assert.True(t, done, "redis_reverted_at is set; should return true")
}

func TestSagaCompensation_RecordCompletion_Idempotent(t *testing.T) {
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewSagaCompensationRepository(h.DB)
	ctx := context.Background()

	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	eventID := uuid.New()
	h.SeedEvent(t, eventID.String(), "Test Event", 100)
	h.SeedOrder(t, orderID.String(), eventID.String(), "failed")

	compensationID := "order:" + orderID.String()

	// First call — inserts, returns 1
	n, err := repo.RecordCompletion(ctx, compensationID, orderID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// Second call — ON CONFLICT DO NOTHING → no error, rowsAffected=0
	n, err = repo.RecordCompletion(ctx, compensationID, orderID)
	require.NoError(t, err, "second call must not return an error (ON CONFLICT DO NOTHING)")
	assert.EqualValues(t, 0, n, "second call should return rowsAffected=0")
}

func TestSagaCompensation_WasRedisReverted_RowNotFound(t *testing.T) {
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewSagaCompensationRepository(h.DB)
	ctx := context.Background()

	// Row was never inserted — must return (false, nil), not an error.
	done, err := repo.WasRedisReverted(ctx, "order:00000000-0000-0000-0000-000000000000")
	require.NoError(t, err, "absent row must not propagate sql.ErrNoRows")
	assert.False(t, done)
}
