//go:build integration

package pgintegration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/booking/synclua"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for D12 Stage 2's Redis-Lua-atomic-deduct +
// sync-PG-INSERT booking service (booking/synclua) against a
// real postgres:15-alpine + redis:7-alpine pair.
//
// Coverage owned here that the unit tests can't pin (per
// `service_test.go`'s package docstring + the plan §3 decision
// to keep miniredis out of unit tests):
//
//   - Real Lua semantics: deduct_sync.lua's DECRBY + HMGET +
//     amount_cents math; revert.lua's `saga:reverted:` SETNX guard;
//     metadata_missing branch returning the qty back atomically.
//   - PG INSERT side: happy path persisting the order row + 23505
//     duplicate-active-order mapping triggering revert.lua.
//   - Concurrent contention through Lua's single-thread serialization
//     point — the architectural baseline Stage 2 brings vs Stage 1's
//     row-lock serialization.
//   - Compensator path: revert.lua + SQL UPDATEs landing the order
//     in 'compensated' status with both Redis AND PG inventory
//     reflecting the revert.
//
// All tests boot their own Postgres + Redis pair (no shared harness
// across tests) — same pattern as sync_booking_test.go. Container
// boot is ~3-5s per test; total suite ~30-40s on a healthy laptop.

// ────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────

// stage2Harness wires Postgres + Redis + a synclua.Service ready
// for BookTicket. Returns the harness pair (for direct
// inspection) + the service + the inventoryRepo (for compensator
// wiring).
func stage2Harness(t *testing.T) (*pgintegration.Harness, *pgintegration.RedisHarness, *synclua.Service, domain.InventoryRepository) {
	t.Helper()
	ctx := context.Background()
	pgH := pgintegration.StartPostgres(ctx, t)
	redisH := pgintegration.StartRedis(ctx, t)

	cfg := &config.Config{
		Booking: config.BookingConfig{ReservationWindow: 15 * time.Minute},
		Redis:   config.RedisConfig{InventoryTTL: 24 * time.Hour},
	}
	orderRepo := postgres.NewPostgresOrderRepository(pgH.DB)
	ttRepo := postgres.NewPostgresTicketTypeRepository(pgH.DB)
	invRepo := cache.NewRedisInventoryRepository(redisH.Client, cfg)
	deducter := cache.NewRedisSyncDeducter(redisH.Client)

	svc := synclua.NewService(pgH.DB, deducter, orderRepo, ttRepo, invRepo, cfg)
	return pgH, redisH, svc, invRepo
}

// seedTicketTypeForStage2 inserts an event + ticket_type into
// Postgres AND hydrates the matching Redis runtime keys
// (`ticket_type_meta:{id}` HSET + `ticket_type_qty:{id}` SETNX) —
// mirroring what the Stage 2 /events handler does. Returns the
// (eventID, ticketTypeID) pair.
//
// Like Stage 1's seedTicketType, the parent event row's
// `available_tickets` is intentionally seeded to a divergent value
// (× 2) — Stage 2's hot path MUST NOT read it; the Redis qty is
// the SoT. Divergence here turns a regression into a tripwire.
func seedTicketTypeForStage2(t *testing.T, pgH *pgintegration.Harness, redisH *pgintegration.RedisHarness, availableTickets int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID := uuid.New()
	pgH.SeedEvent(t, eventID.String(), "Stage2 Test Event", availableTickets*2)

	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	const stmt = `
		INSERT INTO event_ticket_types (
			id, event_id, name, price_cents, currency,
			total_tickets, available_tickets, version
		) VALUES ($1::uuid, $2::uuid, 'GA', 2000, 'usd', $3, $3, 0)`
	_, err = pgH.DB.Exec(stmt, ttID.String(), eventID.String(), availableTickets)
	require.NoError(t, err, "seed event_ticket_types")

	// Hydrate Redis. Use HSET + SET to mirror what
	// inventoryRepo.SetTicketTypeRuntime does, without depending on
	// the repo for setup (so a regression in the repo can't
	// silently make a test pass for the wrong reason).
	ctx := context.Background()
	require.NoError(t, redisH.Client.HSet(ctx, "ticket_type_meta:"+ttID.String(),
		"event_id", eventID.String(),
		"price_cents", "2000",
		"currency", "usd",
	).Err())
	require.NoError(t, redisH.Client.Set(ctx, "ticket_type_qty:"+ttID.String(),
		availableTickets, 24*time.Hour).Err())

	return eventID, ttID
}

// redisQtyNow returns the Redis qty for a ticket_type. -1 indicates
// the key was absent, distinguishing it from a legitimate 0
// (sold-out steady state).
func redisQtyNow(t *testing.T, redisH *pgintegration.RedisHarness, ttID uuid.UUID) int {
	t.Helper()
	val, err := redisH.Client.Get(context.Background(), "ticket_type_qty:"+ttID.String()).Result()
	if errors.Is(err, redis.Nil) {
		return -1
	}
	require.NoError(t, err)
	n, err := strconv.Atoi(val)
	require.NoError(t, err)
	return n
}

// pgAvailableTicketsNow returns the current available_tickets count
// for a ticket_type. Stage 2 leaves this column UNCHANGED on the
// booking hot path; the compensator updates it via the SQL revert
// step.
func pgAvailableTicketsNow(t *testing.T, pgH *pgintegration.Harness, ttID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pgH.DB.QueryRow(
		"SELECT available_tickets FROM event_ticket_types WHERE id = $1::uuid",
		ttID.String(),
	).Scan(&n))
	return n
}

// stage2OrderCount returns the orders rows for a ticket_type — for
// rollback / non-leak assertions.
func stage2OrderCount(t *testing.T, pgH *pgintegration.Harness, ttID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pgH.DB.QueryRow(
		"SELECT COUNT(*) FROM orders WHERE ticket_type_id = $1::uuid",
		ttID.String(),
	).Scan(&n))
	return n
}

// ────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────

// TestSyncLuaBooking_HappyPath — single booking decrements Redis
// qty (NOT PG event_ticket_types.available_tickets, which Stage 2
// leaves alone on the hot path) + persists the order row with the
// price snapshot frozen.
func TestSyncLuaBooking_HappyPath(t *testing.T) {
	pgH, redisH, svc, _ := stage2Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage2(t, pgH, redisH, 10)
	require.Equal(t, 10, redisQtyNow(t, redisH, ttID))
	require.Equal(t, 10, pgAvailableTicketsNow(t, pgH, ttID))

	order, err := svc.BookTicket(ctx, 42, ttID, 1)
	require.NoError(t, err)

	// Redis qty decremented by exactly 1.
	assert.Equal(t, 9, redisQtyNow(t, redisH, ttID),
		"Stage 2's hot path decrements Redis qty (the SoT), not PG")

	// PG event_ticket_types.available_tickets UNCHANGED — Stage 2
	// hot path doesn't touch this column. Pinning this in case a
	// regression accidentally reverts Stage 2 to Stage 1's pattern.
	assert.Equal(t, 10, pgAvailableTicketsNow(t, pgH, ttID),
		"Stage 2 must NOT update event_ticket_types.available_tickets on the booking hot path")

	// Exactly one order row persisted.
	assert.Equal(t, 1, stage2OrderCount(t, pgH, ttID))

	// Domain-level invariants.
	assert.Equal(t, domain.OrderStatusAwaitingPayment, order.Status())
	assert.Equal(t, 42, order.UserID())
	assert.Equal(t, ttID, order.TicketTypeID())
	assert.Equal(t, 1, order.Quantity())
	assert.Equal(t, int64(2000), order.AmountCents())
	assert.Equal(t, "usd", order.Currency())
	assert.True(t, order.ReservedUntil().After(time.Now()))
}

// TestSyncLuaBooking_SoldOut — Lua's DECRBY-then-INCRBY revert path
// for negative results. Sold-out returns ErrSoldOut, qty stays at 0.
func TestSyncLuaBooking_SoldOut(t *testing.T) {
	pgH, redisH, svc, _ := stage2Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage2(t, pgH, redisH, 1)

	// Consume the single ticket.
	_, err := svc.BookTicket(ctx, 1, ttID, 1)
	require.NoError(t, err)
	require.Equal(t, 0, redisQtyNow(t, redisH, ttID))

	// Second booking trips the negative branch in Lua → revert
	// returns to 0 → returns sold_out → service returns ErrSoldOut.
	_, err = svc.BookTicket(ctx, 2, ttID, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrSoldOut),
		"sold-out should return domain.ErrSoldOut sentinel; got %v", err)

	// Lua's atomic INCRBY-on-negative restored qty to 0; no leak.
	assert.Equal(t, 0, redisQtyNow(t, redisH, ttID),
		"Lua sold-out branch must INCRBY-restore qty atomically — no leak")
	assert.Equal(t, 1, stage2OrderCount(t, pgH, ttID),
		"sold-out must NOT leave a partial order row")
}

// TestSyncLuaBooking_MetadataMissingRepairs — the
// `metadata_missing` Lua return triggers the cold-fill repair path:
// service loads ticket_type from PG, populates ticket_type_meta:{id},
// retries deduct, succeeds. Pins the round-trip recovery shape that
// makes Redis FLUSHALL / metadata-eviction non-fatal.
func TestSyncLuaBooking_MetadataMissingRepairs(t *testing.T) {
	pgH, redisH, svc, _ := stage2Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage2(t, pgH, redisH, 5)

	// Wipe ONLY the metadata key, leaving qty intact — the
	// `metadata_missing` regime the repair path is designed for.
	require.NoError(t, redisH.Client.Del(ctx, "ticket_type_meta:"+ttID.String()).Err())

	order, err := svc.BookTicket(ctx, 99, ttID, 1)
	require.NoError(t, err, "repair-then-retry must succeed end-to-end")

	// Order persisted with the expected price snapshot — proves
	// repair populated the metadata correctly (PG → Redis HSET).
	assert.Equal(t, int64(2000), order.AmountCents())
	assert.Equal(t, "usd", order.Currency())

	// Metadata key now repopulated.
	exists, err := redisH.Client.Exists(ctx, "ticket_type_meta:"+ttID.String()).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists,
		"repair must HSET ticket_type_meta after PG load")

	// Redis qty decremented exactly once (the Lua's INCRBY-on-
	// metadata_missing path restored qty BEFORE the retry; the
	// retry's DECRBY took it down by one). No double-decrement.
	assert.Equal(t, 4, redisQtyNow(t, redisH, ttID),
		"repair-retry must net out to single qty decrement, not double")
}

// TestSyncLuaBooking_DuplicateActiveOrderRevertsRedis — the 23505
// duplicate-active-order trip MUST trigger revert.lua so Redis qty
// is restored. Without this, every duplicate would silently consume
// inventory in Redis, leaking tickets relative to Stage 4 (where
// the worker's idempotency key catches duplicates before any
// effect lands).
//
// This is the PR-D12.2 plan §risks #3 case — revert ordering on
// 23505. Pinning it here is critical to the comparison-harness
// contract (Stage 2 must not over-deduct vs Stage 4).
func TestSyncLuaBooking_DuplicateActiveOrderRevertsRedis(t *testing.T) {
	pgH, redisH, svc, _ := stage2Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage2(t, pgH, redisH, 5)
	require.Equal(t, 5, redisQtyNow(t, redisH, ttID))

	// First booking: succeeds.
	_, err := svc.BookTicket(ctx, 99, ttID, 1)
	require.NoError(t, err)
	assert.Equal(t, 4, redisQtyNow(t, redisH, ttID))
	assert.Equal(t, 1, stage2OrderCount(t, pgH, ttID))

	// Second booking by SAME user: PG INSERT trips uq_orders_user_event;
	// service maps to ErrUserAlreadyBought + runs revert.lua.
	_, err = svc.BookTicket(ctx, 99, ttID, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUserAlreadyBought),
		"duplicate-active-order MUST return domain.ErrUserAlreadyBought; got %v", err)

	// CRITICAL: Redis qty restored. The duplicate's Lua DECRBY took
	// qty 4→3; revert.lua INCRBY-ed it back to 4.
	assert.Equal(t, 4, redisQtyNow(t, redisH, ttID),
		"duplicate-active-order must trigger revert.lua so Redis qty is restored — without this, every 23505 leaks a ticket")

	// PG order count stays at 1.
	assert.Equal(t, 1, stage2OrderCount(t, pgH, ttID))
}

// TestSyncLuaBooking_ConcurrentContention — N goroutines book the
// same ticket_type with Redis qty=K (K < N). Lua's single-thread
// serialization point ensures exactly K succeed, exactly (N-K) get
// ErrSoldOut, qty ends at 0, exactly K orders persist.
//
// This is the architectural counterpart of Stage 1's row-lock
// contention test — same observable behavior, different
// serialization mechanism.
func TestSyncLuaBooking_ConcurrentContention(t *testing.T) {
	pgH, redisH, svc, _ := stage2Harness(t)
	ctx := context.Background()

	const (
		stock    = 5
		attempts = 20
	)
	_, ttID := seedTicketTypeForStage2(t, pgH, redisH, stock)

	var (
		wg          sync.WaitGroup
		successes   int
		soldOutErrs int
		otherErrs   int
		mu          sync.Mutex
	)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(uid int) {
			defer wg.Done()
			_, err := svc.BookTicket(ctx, uid, ttID, 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, domain.ErrSoldOut):
				soldOutErrs++
			default:
				otherErrs++
				t.Logf("unexpected concurrent error: %v", err)
			}
		}(i + 1)
	}
	wg.Wait()

	assert.Equal(t, stock, successes,
		"exactly stock-many bookings should succeed under contention")
	assert.Equal(t, attempts-stock, soldOutErrs,
		"the rest should fail with ErrSoldOut")
	assert.Equal(t, 0, otherErrs)
	assert.Equal(t, 0, redisQtyNow(t, redisH, ttID),
		"all stock consumed in Redis")
	assert.Equal(t, stock, stage2OrderCount(t, pgH, ttID),
		"exactly stock-many order rows persisted")
}

// TestSyncLuaBooking_AbandonCompensator — full abandon path:
// book → wait past TTL → stage2Compensator.Compensate → assert
// (a) order status='compensated', (b) Redis qty restored,
// (c) PG event_ticket_types.available_tickets bumped back up,
// (d) saga:reverted:order:<id> SETNX guard armed (idempotency
// proof for any future re-fire on the same orderID).
//
// This is the comparison-harness's "abandon" leg per the plan
// §risks #2 + the existing k6_two_step_flow.js's 20% abandon
// path. The compensator is wired here directly rather than going
// through HandleTestConfirm so we can assert the post-state cleanly
// without HTTP serialization noise.
func TestSyncLuaBooking_AbandonCompensator(t *testing.T) {
	pgH, redisH, svc, invRepo := stage2Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage2(t, pgH, redisH, 10)

	order, err := svc.BookTicket(ctx, 42, ttID, 1)
	require.NoError(t, err)
	require.Equal(t, 9, redisQtyNow(t, redisH, ttID))

	// Run the compensator. Mirror the cmd/booking-cli-stage2 wiring:
	// stage2Compensator depends on (db, inventoryRepo).
	compensator := newStage2CompensatorTest(pgH.DB, invRepo)
	require.NoError(t, compensator.Compensate(ctx, order.ID()))

	// (a) order status='compensated'.
	var status string
	require.NoError(t, pgH.DB.QueryRow(
		"SELECT status FROM orders WHERE id = $1::uuid", order.ID().String(),
	).Scan(&status))
	assert.Equal(t, "compensated", status)

	// (b) Redis qty restored: was 9, +1 = 10.
	assert.Equal(t, 10, redisQtyNow(t, redisH, ttID),
		"compensator must INCRBY Redis qty back via revert.lua")

	// (c) PG event_ticket_types.available_tickets UNCHANGED at the
	// seeded 10. The forward path didn't decrement it (Redis is the
	// SoT on Stage 2's hot path); the compensator MUST mirror that
	// asymmetry — incrementing the PG column on compensation while
	// the forward path leaves it alone would inflate the column to
	// 11 every abandon, drifting upward forever. PG admin readers
	// see the seeded `total_tickets` value; Redis qty is the only
	// authoritative live count.
	assert.Equal(t, 10, pgAvailableTicketsNow(t, pgH, ttID),
		"compensator MUST NOT update event_ticket_types.available_tickets — symmetric with the forward path which doesn't decrement it")

	// (d) SETNX idempotency guard armed. The next-sweep retry sees
	// this and short-circuits the Redis revert; if a second
	// Compensate fires, the qty doesn't double-increment.
	exists, err := redisH.Client.Exists(ctx, "saga:reverted:order:"+order.ID().String()).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists,
		"revert.lua must arm saga:reverted:order:<id> SETNX guard — without this a re-fire double-reverts")

	// Idempotency in action: re-call Compensate. Should return
	// ErrCompensateNotEligible (status no longer 'awaiting_payment')
	// — the SETNX guard is a defense-in-depth in case status check
	// races, but the status check is the primary guard.
	err = compensator.Compensate(ctx, order.ID())
	require.Error(t, err, "second Compensate should fail not-eligible — order is already compensated")

	// Redis qty UNCHANGED at 10 (no double-revert).
	assert.Equal(t, 10, redisQtyNow(t, redisH, ttID),
		"second Compensate must NOT double-revert Redis qty")
}

// stage2CompensatorTest mirrors cmd/booking-cli-stage2's
// stage2Compensator without importing the cmd package. The cmd
// package is `package main` and not import-able from tests, so we
// duplicate the type here. Kept in lockstep with the cmd version
// — if either diverges, slice-4 coverage of the abandon path is
// pinning the wrong semantics. The risk is documented; a future
// extraction to a shared `cmd/internal/stage2compensator/` package
// would close the duplication, but that's out of scope for D12.2.
type stage2CompensatorTest struct {
	db            *sql.DB
	inventoryRepo domain.InventoryRepository
}

func newStage2CompensatorTest(db *sql.DB, inventoryRepo domain.InventoryRepository) *stage2CompensatorTest {
	return &stage2CompensatorTest{db: db, inventoryRepo: inventoryRepo}
}

func (s *stage2CompensatorTest) Compensate(ctx context.Context, orderID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		ttID   uuid.UUID
		qty    int
		status string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT ticket_type_id, quantity, status
		  FROM orders
		 WHERE id = $1
		   FOR UPDATE`,
		orderID).Scan(&ttID, &qty, &status)
	if err != nil {
		return fmt.Errorf("lock order: %w", err)
	}
	if status != string(domain.OrderStatusAwaitingPayment) {
		return errors.New("not eligible: status=" + status)
	}

	if err = s.inventoryRepo.RevertInventory(ctx, ttID, qty, "order:"+orderID.String()); err != nil {
		return fmt.Errorf("redis revert: %w", err)
	}

	// NOTE: must stay symmetric with cmd/booking-cli-stage2's
	// stage2Compensator — no event_ticket_types UPDATE here.
	// The forward path doesn't decrement it; incrementing on
	// compensation would inflate the PG column every abandon
	// (Codex round-1 P1).
	if _, err = tx.ExecContext(ctx, `
		UPDATE orders
		   SET status = 'compensated'
		 WHERE id = $1
		   AND status = 'awaiting_payment'`,
		orderID); err != nil {
		return fmt.Errorf("mark compensated: %w", err)
	}

	return tx.Commit()
}

// Compile-time assertion: synclua.NewService returns a value
// assignable to booking.Service.
var _ booking.Service = (*synclua.Service)(nil)
