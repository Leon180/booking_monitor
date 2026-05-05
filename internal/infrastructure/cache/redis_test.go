package cache

import (
	"context"
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisInventoryRepository_DeductInventory_LargePriceSnapshotStaysExact(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	repo := NewRedisInventoryRepository(rdb, testConfig("worker-1"))
	ctx := context.Background()

	eventID := uuid.New()
	ticketTypeID := uuid.New()
	const priceCents int64 = 922337203685477580
	const quantity = 10
	const expectedAmount int64 = 9223372036854775800

	ticketType := domain.ReconstructTicketType(
		ticketTypeID,
		eventID,
		"VIP",
		priceCents,
		"usd",
		100,
		100,
		nil,
		nil,
		nil,
		"",
		0,
	)
	require.NoError(t, repo.SetTicketTypeRuntime(ctx, ticketType))

	orderID := uuid.New()
	result, err := repo.DeductInventory(ctx, orderID, ticketTypeID, 7, quantity, time.Now().Add(15*time.Minute))
	require.NoError(t, err)
	require.True(t, result.Accepted)
	assert.Equal(t, eventID, result.EventID)
	assert.Equal(t, expectedAmount, result.AmountCents)
	assert.Equal(t, "usd", result.Currency)

	msgs, err := rdb.XRange(ctx, streamKey, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "9223372036854775800", msgs[0].Values[fieldAmountCents])
}
