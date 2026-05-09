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

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/stagehttp"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for D12 Stage 3's Redis-Lua + orders:stream +
// async-worker booking pipeline against real postgres:15-alpine +
// redis:7-alpine + a worker goroutine.
//
// What this suite owns that no other test layer can:
//
//   - Async worker timing — `/book` returns 202 with an order_id;
//     the row appears in PG only after the worker drains the stream
//     message. Tests use a polling-with-deadline pattern (NOT a
//     fixed sleep) to mirror real client behavior.
//   - **PG-symmetry rule (load-bearing)**: Stage 3's worker UoW
//     DECREMENTS event_ticket_types.available_tickets; the
//     compensator MUST INCREMENT it back. Tested in BOTH directions:
//     AbandonCompensator (compensator increments) +
//     WorkerHandleFailure23505 (UoW rollback restores; no compensator
//     increment because there's no committed decrement).
//   - sql.ErrNoRows + orphaned-ticket_type guards in the compensator
//     (the new defensive paths from Slice 1 agent review).
//
// All tests boot their own PG + Redis + worker goroutine. Container
// boot is ~3-5s per test; total suite ~40-50s on a healthy laptop.

// ────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────

// stage3Harness wires Postgres + Redis + a running worker goroutine
// + a Stage-4-vintage booking.Service + a Stage-3 compensator. The
// worker goroutine runs in the background; tests poll for the
// async INSERT to land. Cleanup tears down the worker via ctx
// cancel + waitgroup-wait, mirroring Stage 3's cmd binary's
// shutdown semantics.
type stage3HarnessHandle struct {
	pgH         *pgintegration.Harness
	redisH      *pgintegration.RedisHarness
	bookingSvc  booking.Service
	compensator *stage3CompensatorTest
}

func stage3Harness(t *testing.T) *stage3HarnessHandle {
	t.Helper()
	ctx := context.Background()
	pgH := pgintegration.StartPostgres(ctx, t)
	redisH := pgintegration.StartRedis(ctx, t)

	cfg := &config.Config{
		Booking: config.BookingConfig{ReservationWindow: 15 * time.Minute},
		Redis:   config.RedisConfig{InventoryTTL: 24 * time.Hour, MaxConsecutiveReadErrors: 30, DLQRetention: 7 * 24 * time.Hour},
		Worker: config.WorkerConfig{
			StreamReadCount:     10,
			StreamBlockTimeout:  100 * time.Millisecond,
			MaxRetries:          3,
			RetryBaseDelay:      50 * time.Millisecond,
			FailureTimeout:      5 * time.Second,
			PendingBlockTimeout: 100 * time.Millisecond,
			ReadErrorBackoff:    1 * time.Second,
		},
		App: config.AppConfig{WorkerID: "stage3-test-worker"},
	}
	logger := mlog.NewNop()

	orderRepo := postgres.NewPostgresOrderRepository(pgH.DB)
	eventRepo := postgres.NewPostgresEventRepository(pgH.DB)
	ttRepo := postgres.NewPostgresTicketTypeRepository(pgH.DB)
	outboxRepo := postgres.NewPostgresOutboxRepository(pgH.DB)
	uow := postgres.NewPostgresUnitOfWork(pgH.DB, orderRepo, eventRepo, outboxRepo, ttRepo, logger, application.NoopDBMetrics())

	invRepo := cache.NewRedisInventoryRepository(redisH.Client, cfg)
	queue := cache.NewRedisOrderQueue(redisH.Client, invRepo, logger, cfg, worker.NoopQueueMetrics(), worker.DefaultRetryPolicy())

	bookingSvc := booking.NewService(orderRepo, ttRepo, invRepo, cfg)

	base := worker.NewOrderMessageProcessor(uow, logger)
	processor := worker.NewMessageProcessorMetricsDecorator(base, &nopWorkerMetrics{})
	workerSvc := worker.NewService(queue, processor, logger)

	// EnsureGroup before launching the goroutine so XADD always
	// writes to a consumer-group-aware stream. Note: this guards
	// against the "consumer group doesn't exist" race only — it
	// does NOT guarantee the worker's XReadGroup loop has reached
	// its first poll by the time /book fires. That second window
	// is closed by the 5s polling deadline in waitForOrderStatus
	// (the worker's first XReadGroup picks up any messages already
	// in the stream).
	require.NoError(t, queue.EnsureGroup(ctx), "EnsureGroup must succeed before workerSvc.Start")

	runCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = workerSvc.Start(runCtx)
	}()

	t.Cleanup(func() {
		cancel()
		// Bound the wait so a stuck worker doesn't hang test cleanup.
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Logf("worker goroutine did not exit within 10s of cancel — leaking goroutine")
		}
	})

	compensator := newStage3CompensatorTest(pgH.DB, invRepo)
	return &stage3HarnessHandle{
		pgH:         pgH,
		redisH:      redisH,
		bookingSvc:  bookingSvc,
		compensator: compensator,
	}
}

// nopWorkerMetrics — minimal impl of worker.Metrics. The
// QueueMetrics interface already has a public `worker.NoopQueueMetrics()`
// helper; worker.Metrics doesn't, so we declare a tiny struct here.
type nopWorkerMetrics struct{}

func (*nopWorkerMetrics) RecordOrderOutcome(_ string)              {}
func (*nopWorkerMetrics) RecordProcessingDuration(_ time.Duration) {}
func (*nopWorkerMetrics) RecordInventoryConflict()                 {}

// stage3CompensatorTest mirrors cmd/booking-cli-stage3's
// stage3Compensator without importing the cmd package (cmd is
// `package main` and not import-able from test packages). Kept in
// lockstep with the cmd version — a future extraction to a shared
// `cmd/internal/stage3compensate/` package would close the
// duplication, but that's out of scope for D12.3.
//
// CRITICAL invariant: the SQL shape MUST match the cmd version
// (NullUUID scan + sql.ErrNoRows guard + ttID.Valid check + revert.lua-
// inside-tx + RowsAffected check on UPDATE event_ticket_types). A
// test helper that drifted from the cmd version would silently
// pin the wrong semantics. Slice 1 agent reviews caught this class
// of bug in the cmd version; the same fixes apply here.
type stage3CompensatorTest struct {
	db            *sql.DB
	inventoryRepo domain.InventoryRepository
}

func newStage3CompensatorTest(db *sql.DB, inventoryRepo domain.InventoryRepository) *stage3CompensatorTest {
	return &stage3CompensatorTest{db: db, inventoryRepo: inventoryRepo}
}

func (s *stage3CompensatorTest) Compensate(ctx context.Context, orderID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		ttID   uuid.NullUUID
		qty    int
		status string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT ticket_type_id, quantity, status
		  FROM orders
		 WHERE id = $1
		   FOR UPDATE`,
		orderID).Scan(&ttID, &qty, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return stagehttp.ErrCompensateNotEligible
	}
	if err != nil {
		return fmt.Errorf("lock order: %w", err)
	}
	if status != string(domain.OrderStatusAwaitingPayment) {
		return stagehttp.ErrCompensateNotEligible
	}
	if !ttID.Valid {
		return stagehttp.ErrCompensateNotEligible
	}

	if err = s.inventoryRepo.RevertInventory(ctx, ttID.UUID, qty, "order:"+orderID.String()); err != nil {
		return fmt.Errorf("redis revert: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE event_ticket_types
		   SET available_tickets = available_tickets + $1,
		       version = version + 1
		 WHERE id = $2`,
		qty, ttID.UUID)
	if err != nil {
		return fmt.Errorf("revert pg inventory: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revert pg inventory rows-affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("revert pg inventory: no event_ticket_types row for id=%s — orphaned order, manual review needed", ttID.UUID)
	}

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

// seedTicketTypeForStage3 mirrors seedTicketTypeForStage2 — Stage 3
// uses the same Redis hot-path key shape as Stage 2 (the deduct
// scripts read the same `ticket_type_qty:{id}` + metadata keys).
// Divergent seeding (events.available_tickets = ticket_type stock
// × 2) keeps the regression tripwire active.
func seedTicketTypeForStage3(t *testing.T, pgH *pgintegration.Harness, redisH *pgintegration.RedisHarness, availableTickets int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID := uuid.New()
	pgH.SeedEvent(t, eventID.String(), "Stage3 Test Event", availableTickets*2)

	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	const stmt = `
		INSERT INTO event_ticket_types (
			id, event_id, name, price_cents, currency,
			total_tickets, available_tickets, version
		) VALUES ($1::uuid, $2::uuid, 'GA', 2000, 'usd', $3, $3, 0)`
	_, err = pgH.DB.Exec(stmt, ttID.String(), eventID.String(), availableTickets)
	require.NoError(t, err, "seed event_ticket_types")

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

// waitForOrderStatus polls the orders table until the row exists
// AND has the expected status, OR the deadline expires. This is
// the canonical "wait for async worker" pattern — fixed sleeps
// would either over-wait (slowing the suite) or under-wait
// (flaky on slow CI).
func waitForOrderStatus(t *testing.T, db *sql.DB, orderID uuid.UUID, want string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		var status string
		err := db.QueryRow(
			"SELECT status FROM orders WHERE id = $1::uuid",
			orderID.String(),
		).Scan(&status)
		if err == nil && status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waitForOrderStatus: order=%s never reached status=%q within %s", orderID, want, deadline)
}

// waitForOrderAbsent gives the worker `settle` time to attempt the
// 23505 path (DecrementTicket → Order.Create → 23505 → UoW
// rollback). At the end, asserts the row is absent. Used by tests
// that exercise the worker's failure path.
func waitForOrderAbsent(t *testing.T, db *sql.DB, orderID uuid.UUID, settle time.Duration) {
	t.Helper()
	time.Sleep(settle)
	var n int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM orders WHERE id = $1::uuid",
		orderID.String(),
	).Scan(&n))
	require.Equal(t, 0, n, "order=%s must NOT appear in PG (worker UoW should have rolled back)", orderID)
}

// waitForRedisQty polls Redis until `ticket_type_qty:{id}` reaches
// `want`, OR the deadline expires. Used after waitForOrderAbsent
// to assert the worker's handleFailure → revert.lua path completed
// (Redis is outside the UoW's ACID boundary; the revert is async
// relative to the SQL rollback). Without polling this would race
// with Redis round-trip latency on slow CI runners.
func waitForRedisQty(t *testing.T, redisH *pgintegration.RedisHarness, ttID uuid.UUID, want int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if redisQtyForStage3(t, redisH, ttID) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := redisQtyForStage3(t, redisH, ttID)
	t.Fatalf("waitForRedisQty: ticket_type_qty:%s never reached %d within %s; got %d", ttID, want, deadline, got)
}

func redisQtyForStage3(t *testing.T, redisH *pgintegration.RedisHarness, ttID uuid.UUID) int {
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

func pgAvailableTicketsForStage3(t *testing.T, pgH *pgintegration.Harness, ttID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pgH.DB.QueryRow(
		"SELECT available_tickets FROM event_ticket_types WHERE id = $1::uuid",
		ttID.String(),
	).Scan(&n))
	return n
}

// ────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────

// TestStage3Booking_HappyPath_AsyncWorkerInsertsOrder — the full
// async flow: BookTicket returns immediately with an order_id
// (Lua DECRBY + XADD); the worker async-INSERTs the row + decrements
// PG. Both Redis qty AND PG event_ticket_types decrement by 1. Pins
// Stage 3's hot-path SoT discipline (different from Stage 2 which
// leaves PG untouched).
func TestStage3Booking_HappyPath_AsyncWorkerInsertsOrder(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage3(t, h.pgH, h.redisH, 10)
	require.Equal(t, 10, redisQtyForStage3(t, h.redisH, ttID))
	require.Equal(t, 10, pgAvailableTicketsForStage3(t, h.pgH, ttID))

	order, err := h.bookingSvc.BookTicket(ctx, 42, ttID, 1)
	require.NoError(t, err)

	// Redis qty decremented synchronously (Lua atomic).
	assert.Equal(t, 9, redisQtyForStage3(t, h.redisH, ttID),
		"Redis qty decremented synchronously by Lua DECRBY")

	// PG decrements happen asynchronously via the worker. Poll until
	// the order lands.
	waitForOrderStatus(t, h.pgH.DB, order.ID(), "awaiting_payment", 5*time.Second)

	// PG column ALSO decremented — symmetric with Stage 4 worker UoW
	// (DecrementTicket inside the same tx as Order.Create). This is
	// what differs from Stage 2 (no PG decrement) and pins the
	// SoT discipline rule.
	assert.Equal(t, 9, pgAvailableTicketsForStage3(t, h.pgH, ttID),
		"Stage 3's worker UoW must DECREMENT event_ticket_types — symmetric with the compensator's INCREMENT")

	// Order persisted with the right shape.
	assert.Equal(t, domain.OrderStatusAwaitingPayment, order.Status())
	assert.Equal(t, ttID, order.TicketTypeID())
	assert.Equal(t, int64(2000), order.AmountCents())
	assert.Equal(t, "usd", order.Currency())
}

// TestStage3Booking_SoldOut — Lua atomic DECRBY-then-INCRBY revert.
// Same shape as Stage 2's sold-out test; Stage 3 reuses Stage 4's
// BookingService verbatim.
func TestStage3Booking_SoldOut(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage3(t, h.pgH, h.redisH, 1)

	order1, err := h.bookingSvc.BookTicket(ctx, 1, ttID, 1)
	require.NoError(t, err)
	require.Equal(t, 0, redisQtyForStage3(t, h.redisH, ttID))
	waitForOrderStatus(t, h.pgH.DB, order1.ID(), "awaiting_payment", 5*time.Second)

	_, err = h.bookingSvc.BookTicket(ctx, 2, ttID, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrSoldOut),
		"sold-out should return domain.ErrSoldOut sentinel; got %v", err)

	assert.Equal(t, 0, redisQtyForStage3(t, h.redisH, ttID),
		"Lua sold-out branch must INCRBY-restore qty atomically")
}

// TestStage3Booking_AbandonCompensator_PGSymmetry — the load-bearing
// PG-symmetry assertion. Worker decrements PG → compensator
// increments PG back → final state matches the seeded value
// exactly. A regression in EITHER direction (asymmetric compensator
// that didn't increment, OR a worker that didn't decrement) trips
// this test.
func TestStage3Booking_AbandonCompensator_PGSymmetry(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage3(t, h.pgH, h.redisH, 10)

	order, err := h.bookingSvc.BookTicket(ctx, 42, ttID, 1)
	require.NoError(t, err)
	waitForOrderStatus(t, h.pgH.DB, order.ID(), "awaiting_payment", 5*time.Second)

	// Pre-compensate state: both Redis and PG decremented to 9.
	require.Equal(t, 9, redisQtyForStage3(t, h.redisH, ttID))
	require.Equal(t, 9, pgAvailableTicketsForStage3(t, h.pgH, ttID))

	// Run the compensator (simulates /test/payment/confirm/:id?outcome=failed
	// or the in-binary expiry sweeper firing).
	require.NoError(t, h.compensator.Compensate(ctx, order.ID()))

	// PG column INCREMENTED back to seeded value (symmetric).
	assert.Equal(t, 10, pgAvailableTicketsForStage3(t, h.pgH, ttID),
		"compensator MUST increment event_ticket_types back to seeded value — symmetric with the worker's UoW decrement. Asymmetric drifts PG -qty per abandon.")

	// Redis qty INCREMENTED back via revert.lua.
	assert.Equal(t, 10, redisQtyForStage3(t, h.redisH, ttID),
		"compensator INCRBY's Redis qty back via revert.lua")

	// Order status='compensated'.
	var status string
	require.NoError(t, h.pgH.DB.QueryRow(
		"SELECT status FROM orders WHERE id = $1::uuid", order.ID().String(),
	).Scan(&status))
	assert.Equal(t, "compensated", status)

	// SETNX idempotency guard armed — a re-fire of Compensate
	// returns ErrCompensateNotEligible (status moved past
	// awaiting_payment) without double-incrementing.
	err = h.compensator.Compensate(ctx, order.ID())
	require.Error(t, err)
	assert.True(t, errors.Is(err, stagehttp.ErrCompensateNotEligible),
		"second Compensate must return ErrCompensateNotEligible (status no longer awaiting_payment)")

	// Redis qty UNCHANGED at 10 (no double-revert).
	assert.Equal(t, 10, redisQtyForStage3(t, h.redisH, ttID),
		"second Compensate must NOT double-revert Redis qty")
	// PG column UNCHANGED at 10 (no double-increment).
	assert.Equal(t, 10, pgAvailableTicketsForStage3(t, h.pgH, ttID),
		"second Compensate must NOT double-increment PG column")
}

// TestStage3Booking_WorkerHandleFailure23505_PGUnchanged — the
// INVERSE symmetry assertion. When the worker UoW hits 23505
// (uq_orders_user_event), the UoW rolls back. PG column was
// decremented inside the same tx → rollback restores it. Redis
// qty was already DECRBY'd by Lua → handleFailure runs revert.lua.
//
// CRITICAL: the compensator's IncrementTicket rule does NOT apply
// here — the worker's handleFailure must NOT call IncrementTicket
// because the UoW rollback already undid the decrement
// atomically. This test pins that scope distinction.
func TestStage3Booking_WorkerHandleFailure23505_PGUnchanged(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage3(t, h.pgH, h.redisH, 5)

	// First booking: succeeds.
	order1, err := h.bookingSvc.BookTicket(ctx, 99, ttID, 1)
	require.NoError(t, err)
	waitForOrderStatus(t, h.pgH.DB, order1.ID(), "awaiting_payment", 5*time.Second)
	require.Equal(t, 4, redisQtyForStage3(t, h.redisH, ttID))
	require.Equal(t, 4, pgAvailableTicketsForStage3(t, h.pgH, ttID))

	// Second booking by SAME user: BookingService DeductInventory
	// Lua decrements Redis to 3 + XADDs. Worker picks up message,
	// opens UoW, DecrementTicket succeeds (PG → 3), Order.Create
	// returns ErrUserAlreadyBought (uq_orders_user_event), UoW
	// rolls back (PG → 4). handleFailure runs revert.lua (Redis → 4).
	order2, err := h.bookingSvc.BookTicket(ctx, 99, ttID, 1)
	require.NoError(t, err, "BookingService returns 202 even though worker will reject — async contract")

	// Worker burns retry budget (ErrUserAlreadyBought is misclassified
	// as transient — known issue per Slice 1 plan §risk #5; functionally
	// correct, just slower). After ~3 retries × 50ms backoff + final
	// handleFailure, the message lands in DLQ. Total worst-case ~1s
	// in warm Docker, but handleFailure has a 5s budget and Redis
	// round-trip latency on slow CI runners may stretch it.
	waitForOrderAbsent(t, h.pgH.DB, order2.ID(), 2*time.Second)

	// Critical symmetry assertion: the UoW rollback restored PG, and
	// handleFailure's revert.lua restored Redis. Redis revert may
	// lag the PG rollback by a Redis round-trip — poll to avoid
	// asserting against a state that's still settling.
	waitForRedisQty(t, h.redisH, ttID, 4, 3*time.Second)
	assert.Equal(t, 4, pgAvailableTicketsForStage3(t, h.pgH, ttID),
		"23505 → UoW rollback restores PG; handleFailure must NOT additionally call IncrementTicket. Asymmetric +qty would inflate PG per duplicate.")
	assert.Equal(t, 4, redisQtyForStage3(t, h.redisH, ttID),
		"handleFailure must run revert.lua to restore Redis qty after the UoW rollback (Redis is outside ACID; the Lua DECRBY committed)")
}

// TestStage3Booking_CompensatorAcceptsErrNoRows — sweeper-vs-worker-
// PEL race: compensator called against an order_id that doesn't
// exist returns ErrCompensateNotEligible (silent skip), NOT a hard
// error. Without this guard the sweeper logs spurious errors per
// race window. Pins Slice 1 silent-failure-hunter HIGH #6 fix.
func TestStage3Booking_CompensatorAcceptsErrNoRows(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	missingOrderID, err := uuid.NewV7()
	require.NoError(t, err)

	err = h.compensator.Compensate(ctx, missingOrderID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, stagehttp.ErrCompensateNotEligible),
		"missing order must return ErrCompensateNotEligible (sweeper silent-skip), NOT a hard sql.ErrNoRows leak. Got: %v", err)
}

// TestStage3Booking_CompensatorOrphanedTicketType_PGLeakGuard —
// the H2 fix from Slice 1: if the order's ticket_type_id doesn't
// match any event_ticket_types row, the UPDATE rows-affected=0.
// Without the RowsAffected check, the compensator would commit
// successfully (orders.status='compensated', Redis qty restored
// via revert.lua), but PG inventory would stay short → permanent
// leak. The check turns this into a hard error so the sweeper
// retries (revert.lua's SETNX guard short-circuits the Redis
// re-INCRBY) and ops sees the error log.
//
// Setup: book successfully, then DELETE the event_ticket_types row
// out from under the order before calling Compensate.
func TestStage3Booking_CompensatorOrphanedTicketType_PGLeakGuard(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketTypeForStage3(t, h.pgH, h.redisH, 5)

	order, err := h.bookingSvc.BookTicket(ctx, 1, ttID, 1)
	require.NoError(t, err)
	waitForOrderStatus(t, h.pgH.DB, order.ID(), "awaiting_payment", 5*time.Second)

	// Orphan the order: delete event_ticket_types out from under it.
	// Schema reality (deploy/postgres/migrations/000014): there is
	// NO FK from orders.ticket_type_id to event_ticket_types.id, so
	// this DELETE succeeds without referential checks AND the
	// orders.ticket_type_id column retains the original UUID value
	// (it does NOT become NULL). When Compensate runs, ttID.Valid
	// is true (UUID is still there), revert.lua INCRBYs Redis qty,
	// then UPDATE event_ticket_types matches zero rows — the
	// RowsAffected check fires.
	//
	// FORWARD-COMPAT NOTE: if a future migration adds an FK with
	// ON DELETE SET NULL, this DELETE would set ticket_type_id to
	// NULL, and the compensator's `!ttID.Valid` guard would fire
	// instead — returning ErrCompensateNotEligible. The assertion
	// below pins the rows-affected-zero path explicitly so the
	// test fails loudly on schema drift rather than silently
	// pinning a different code path.
	_, err = h.pgH.DB.Exec(
		"DELETE FROM event_ticket_types WHERE id = $1::uuid", ttID.String(),
	)
	require.NoError(t, err, "orphan the ticket_type for test setup")

	// Compensate must fail loudly (not silently swallow the leak).
	err = h.compensator.Compensate(ctx, order.ID())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no event_ticket_types row",
		"orphaned-ticket_type case must surface a clear error mentioning the missing row, NOT silently commit")
	assert.NotErrorIs(t, err, stagehttp.ErrCompensateNotEligible,
		"this is a real PG-leak risk, NOT a 'not eligible' sweeper-silent-skip")
}

// TestStage3Booking_ConcurrentContention — N goroutines book the
// same ticket_type with stock=K (K < N). Lua's single-thread
// serialization point ensures exactly K succeed, exactly (N-K)
// get ErrSoldOut. The async worker INSERTs all K orders.
func TestStage3Booking_ConcurrentContention(t *testing.T) {
	h := stage3Harness(t)
	ctx := context.Background()

	const (
		stock    = 5
		attempts = 20
	)
	_, ttID := seedTicketTypeForStage3(t, h.pgH, h.redisH, stock)

	var (
		wg          sync.WaitGroup
		successes   int
		soldOutErrs int
		otherErrs   int
		mu          sync.Mutex
		successIDs  []uuid.UUID
	)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(uid int) {
			defer wg.Done()
			order, err := h.bookingSvc.BookTicket(ctx, uid, ttID, 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
				successIDs = append(successIDs, order.ID())
			case errors.Is(err, domain.ErrSoldOut):
				soldOutErrs++
			default:
				otherErrs++
				t.Logf("unexpected concurrent error: %v", err)
			}
		}(i + 1)
	}
	wg.Wait()

	// Distinct user_ids per goroutine — guarantees that
	// uq_orders_user_event never trips. If a future refactor changes
	// `i + 1` to a constant, the worker's async 23505 path would
	// kick in instead of clean Lua serialization, and `successIDs`
	// would never reach `stock` because the worker would reject
	// duplicates → some `waitForOrderStatus` calls below would
	// time out. This explicit length check fails fast with a clear
	// message instead of waiting 5s for each timeout.
	require.Len(t, successIDs, stock,
		"distinct user_ids must produce exactly `stock` successful BookTicket returns; if user_id is constant, worker rejects duplicates and successIDs would be < stock — test the user_id distinct invariant")
	assert.Equal(t, stock, successes,
		"exactly stock-many bookings should succeed under contention")
	assert.Equal(t, attempts-stock, soldOutErrs)
	assert.Equal(t, 0, otherErrs)
	assert.Equal(t, 0, redisQtyForStage3(t, h.redisH, ttID),
		"all stock consumed in Redis")

	// Wait for all worker-async INSERTs to land.
	for _, id := range successIDs {
		waitForOrderStatus(t, h.pgH.DB, id, "awaiting_payment", 5*time.Second)
	}

	// PG column matches Redis (synchronous Lua serialization +
	// per-message UoW serialization through DecrementTicket's WHERE
	// available_tickets >= $1 guard).
	assert.Equal(t, 0, pgAvailableTicketsForStage3(t, h.pgH, ttID),
		"PG event_ticket_types must drain to 0 — symmetric with Redis qty")
}

// Compile-time assertion of `bookingSvc booking.Service` happens
// implicitly at the harness's struct field assignment (line where
// `bookingSvc: bookingSvc` is constructed) — the field type is
// `booking.Service` and the value comes from `booking.NewService`.
// If NewService's return type ever drifts, the harness fails to
// compile here. No separate `var _ = ...` line needed.
