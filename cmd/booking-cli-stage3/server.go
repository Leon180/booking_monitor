package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/stagehttp"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
	"booking_monitor/internal/infrastructure/observability"
)

// runServer is the `server` subcommand entry for Stage 3. Mirrors
// Stage 2's runServer plus the worker provider chain (the
// architectural pivot vs Stage 2: forward-path inventory state lands
// asynchronously via the worker, not synchronously inside /book).
func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),

		// cache.Module provides Redis client + InventoryRepository +
		// OrderQueue + idempotency repository. Stage 3 uses ALL of
		// these (queue is read by the worker; inventory by the booking
		// Service + compensator). Importing the whole module is
		// simpler than cherry-picking; the streams_collector and
		// pool_collector fx.Invoke calls are operationally desirable
		// observability surfaces for Stage 3 anyway.
		cache.Module,

		// booking.Service — Stage 4's existing impl. Hot path:
		// validate → mint orderID → DeductInventory (deduct.lua: DECRBY
		// + HMGET + XADD orders:stream) → return reservation. The
		// metadata-missing repair path lives there too and is reused
		// verbatim. No Stage-3-specific Service.
		fx.Provide(booking.NewService),

		// Worker fx wiring — replicate Stage 4's inline provider
		// closure pattern. The worker package does NOT export an
		// fx.Options bundle; the decorator chain (metrics around
		// orderMessageProcessor) is wired here. Stage 3 uses the
		// SAME chain Stage 4 uses; the only difference is what's
		// downstream of the worker (Stage 4 has Kafka outbox + saga;
		// Stage 3 has neither). `observability.NewWorkerMetrics`
		// supplies the prometheus-backed `worker.Metrics` impl —
		// same provider Stage 4 uses; the package-level prom collectors
		// register once per process so each stage binary gets its own
		// metrics namespace.
		fx.Provide(
			worker.DefaultRetryPolicy,
			observability.NewWorkerMetrics,
			func(
				queue worker.OrderQueue,
				uow application.UnitOfWork,
				metrics worker.Metrics,
				logger *mlog.Logger,
			) worker.Service {
				base := worker.NewOrderMessageProcessor(uow, logger)
				processor := worker.NewMessageProcessorMetricsDecorator(base, metrics)
				return worker.NewService(queue, processor, logger)
			},
		),

		// Stage 3's stagehttp.Compensator: revert.lua + UPDATE
		// event_ticket_types += qty + UPDATE orders. Symmetric with
		// the worker's UoW that decrements event_ticket_types.
		fx.Provide(
			fx.Annotate(
				newStage3Compensator,
				fx.As(new(stagehttp.Compensator)),
			),
		),

		fx.Invoke(installServer),
	)
	app.Run()
}

// installServer wires the HTTP layer + worker + expiry sweeper
// under a SINGLE shared `runCtx` so the OnStop chain coordinates
// all three concurrent goroutines. This is the Stage 4 pattern
// (cmd/booking-cli/server.go's `installServer` + `startBackground
// Runners` + `shutdownAll`) — Stage 3 inherits it because it has
// MORE goroutines than Stages 1+2 and per-goroutine ctx would
// risk uncoordinated shutdown order.
//
// Lifecycle:
//
//   - OnStart: launch HTTP server, worker, sweeper. Each goroutine
//     respects `runCtx.Done()` for clean shutdown.
//   - OnStop: cancel `runCtx` (signals worker + sweeper to drain),
//     `srv.Shutdown(stopCtx)` (HTTP graceful close), wait for the
//     two background goroutines to finish bounded by the fx
//     stop deadline. If they exceed the budget, return
//     `stopCtx.Err()` so fx logs the slow-shutdown.
//
// fx.Shutdowner is injected so a fatal background-goroutine error
// (worker subscribe permanently broken, HTTP listener fails after
// OnStart) escalates to fx.Shutdown(ExitCode(1)) instead of being
// logged + swallowed.
func installServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	cfg *config.Config,
	db *sql.DB,
	inventoryRepo domain.InventoryRepository,
	bookingService booking.Service,
	compensator stagehttp.Compensator,
	workerSvc worker.Service,
	logger *mlog.Logger,
) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/livez", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := router.Group(apiV1Prefix)
	v1.POST("/book", stagehttp.HandleBook(bookingService))
	v1.GET("/orders/:id", stagehttp.HandleGetOrder(bookingService))
	v1.POST("/orders/:id/pay", stagehttp.HandlePayIntent(db, "pi_stage3_"))
	v1.POST("/events", handleCreateEvent(db, inventoryRepo, logger))

	router.POST("/test/payment/confirm/:id", stagehttp.HandleTestConfirm(db, compensator))

	addr := ":" + cfg.Server.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	sweepInterval := cfg.Expiry.SweepInterval
	gracePeriod := cfg.Expiry.ExpiryGracePeriod

	// Single shared runCtx coordinates the worker + sweeper goroutines'
	// shutdown signal. The HTTP server goroutine is NOT in this
	// WaitGroup — it's drained via srv.Shutdown(stopCtx) instead, which
	// blocks until ListenAndServe returns. This split is intentional:
	// the wg only covers the two background goroutines that respond to
	// ctx.Done(); the HTTP server has its own stdlib graceful-close
	// semantics that don't fit the ctx pattern.
	runCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			// HTTP server in its own goroutine; failure escalates
			// via shutdowner so a port-collision after OnStart returns
			// kills the process (k8s restart on the next probe).
			go func() {
				logger.Info(context.Background(), "stage3 HTTP server starting",
					mlog.String("addr", addr))
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error(context.Background(), "stage3 HTTP server failed — escalating fx.Shutdown",
						tag.Error(err))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()

			// Worker — reads orders:stream, INSERTs orders. ctx.Canceled
			// is the clean-shutdown signal; anything else is fatal
			// (queue setup permanently broken) and must escalate.
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := workerSvc.Start(runCtx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Error(context.Background(), "stage3 worker stopped with error — escalating fx.Shutdown",
						tag.Error(err))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()

			// Expiry sweeper — same shape as Stages 1+2 sweepers.
			wg.Add(1)
			go func() {
				defer wg.Done()
				logger.Info(runCtx, "stage3 expiry sweeper starting",
					mlog.Duration("sweep_interval", sweepInterval),
					mlog.Duration("grace_period", gracePeriod))
				ticker := time.NewTicker(sweepInterval)
				defer ticker.Stop()
				for {
					select {
					case <-runCtx.Done():
						logger.Info(context.Background(), "stage3 expiry sweeper stopping")
						return
					case <-ticker.C:
						sweepOnce(runCtx, db, gracePeriod, compensator, logger)
					}
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			// 1. Signal worker + sweeper to stop. Both check
			//    runCtx.Done() in their main loops; cancel is
			//    idempotent.
			cancel()

			// 2. HTTP graceful close. srv.Shutdown drains in-flight
			//    requests bounded by stopCtx; a 5s sub-budget keeps
			//    HTTP shutdown bounded even if fx.OnStop's deadline
			//    is generous.
			shutdownCtx, cancelHTTP := context.WithTimeout(stopCtx, 5*time.Second)
			defer cancelHTTP()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Error(stopCtx, "stage3 HTTP shutdown error", tag.Error(err))
			}

			// 3. Wait for worker + sweeper to drain, bounded by fx
			//    stop deadline. If they exceed it, return ctx.Err()
			//    so fx logs the slow-shutdown — better than hanging
			//    indefinitely on a stuck Redis call.
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

// handleCreateEvent is the Stage 3 /api/v1/events handler. Same
// shape as Stage 2's: PG-first then Redis hydrate, with detached-
// ctx PG compensation on Redis hydrate failure.
//
// Without this compensation pattern, a Redis hydrate failure post-
// PG-commit would leave the event PERMANENTLY un-bookable: the
// first /book DECRBYs a non-existent ticket_type_qty:{id} key, gets
// -count, hits deduct.lua's sold_out branch (INCRBY restores qty
// to 0), and returns sold_out — the metadata_missing repair path
// never fires because the qty check runs first. compensateDangling
// Event clears the orphan PG rows so a retry of /events can
// re-create cleanly. Mirrors internal/application/event/service.go.
func handleCreateEvent(db *sql.DB, inventoryRepo domain.InventoryRepository, logger *mlog.Logger) gin.HandlerFunc {
	type req struct {
		Name         string `json:"name" binding:"required"`
		TotalTickets int    `json:"total_tickets" binding:"required,min=1"`
		PriceCents   int64  `json:"price_cents" binding:"required,min=1"`
		Currency     string `json:"currency" binding:"required,len=3"`
	}
	return func(c *gin.Context) {
		var r req
		if err := c.ShouldBindJSON(&r); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		eventID := uuid.New()
		ttID, err := uuid.NewV7()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "mint ticket_type id"})
			return
		}

		tx, err := db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "begin tx"})
			return
		}
		defer func() { _ = tx.Rollback() }()

		if _, err = tx.ExecContext(c.Request.Context(),
			`INSERT INTO events (id, name, total_tickets, available_tickets, version)
			 VALUES ($1::uuid, $2, $3, $3, 0)`,
			eventID.String(), r.Name, r.TotalTickets); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "insert event: " + err.Error()})
			return
		}
		if _, err = tx.ExecContext(c.Request.Context(),
			`INSERT INTO event_ticket_types
			   (id, event_id, name, price_cents, currency, total_tickets, available_tickets, version)
			 VALUES ($1::uuid, $2::uuid, 'GA', $3, $4, $5, $5, 0)`,
			ttID.String(), eventID.String(), r.PriceCents, r.Currency, r.TotalTickets); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "insert ticket_type: " + err.Error()})
			return
		}
		if err = tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "commit event tx"})
			return
		}

		tt := domain.ReconstructTicketType(
			ttID, eventID, "GA",
			r.PriceCents, r.Currency,
			r.TotalTickets, r.TotalTickets,
			nil, nil, nil, "",
			0,
		)
		if err := inventoryRepo.SetTicketTypeRuntime(c.Request.Context(), tt); err != nil {
			compensateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if compErr := compensateDanglingEvent(compensateCtx, db, eventID, ttID); compErr != nil {
				logger.Error(c.Request.Context(), "stage3 COMPENSATION FAILED — dangling event rows",
					tag.EventID(eventID),
					tag.TicketTypeID(ttID),
					mlog.NamedError("redis_error", err),
					mlog.NamedError("compensation_error", compErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "redis hydrate + compensation both failed"})
				return
			}
			logger.Warn(c.Request.Context(), "stage3 compensated dangling event after Redis hydrate failure",
				tag.EventID(eventID),
				tag.TicketTypeID(ttID),
				mlog.NamedError("redis_error", err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "redis hydrate failed; event creation rolled back"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":            eventID,
			"name":          r.Name,
			"total_tickets": r.TotalTickets,
			"ticket_types": []gin.H{
				{
					"id":                ttID,
					"name":              "GA",
					"price_cents":       r.PriceCents,
					"currency":          r.Currency,
					"total_tickets":     r.TotalTickets,
					"available_tickets": r.TotalTickets,
				},
			},
		})
	}
}

// compensateDanglingEvent deletes the event + its default
// ticket_type in a single PG transaction. Called when /events'
// post-commit Redis hydrate fails — leaves PG clean so a retry
// of /events can re-Create without colliding with the orphan rows.
//
// This function is duplicated verbatim from cmd/booking-cli-stage2's
// equivalent because cmd packages are `package main` and not
// import-able from each other; a future scope-expansion PR could
// extract to a shared cmd/internal/stagecompensate/ package.
//
// FK ordering: event_ticket_types.event_id → events.id, so the
// ticket_type DELETE must run first. Both DELETEs are idempotent
// (no-op on missing row), so a partial earlier compensation can
// re-run safely.
func compensateDanglingEvent(ctx context.Context, db *sql.DB, eventID, ttID uuid.UUID) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin compensation tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM event_ticket_types WHERE id = $1::uuid",
		ttID.String()); err != nil {
		return fmt.Errorf("delete ticket_type %s: %w", ttID, err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM events WHERE id = $1::uuid",
		eventID.String()); err != nil {
		return fmt.Errorf("delete event %s: %w", eventID, err)
	}
	return tx.Commit()
}

// stage3Compensator is the abandon-path Compensator for Stage 3.
// Symmetric with the worker's UoW: forward decrements PG, backward
// increments PG. Mirrors Stage 4's saga-compensator semantics but
// runs in-process (Stage 3 has no Kafka, no out-of-process saga).
//
// Compensation tx (revert.lua INSIDE the tx, before the SQL UPDATEs
// — the same pattern PR #106 settled on for Stage 2; NOT Stage 4's
// saga-compensator pattern of PG-first-Redis-after):
//
//	BEGIN
//	  SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
//	  if sql.ErrNoRows → ErrCompensateNotEligible (silent skip)
//	  if status != 'awaiting_payment' → ErrCompensateNotEligible
//	  EVAL revert.lua against ticket_type_qty:{ttID}
//	     (idempotent via saga:reverted:order:<id> SETNX)
//	  UPDATE event_ticket_types SET available_tickets += quantity, version += 1
//	  UPDATE orders SET status='compensated' WHERE id=$1 AND status='awaiting_payment'
//	COMMIT
//
// Recovery semantics for the revert-inside-tx ordering:
//
//   - revert.lua succeeds, UPDATE event_ticket_types fails: tx rolls
//     back. Redis qty IS incremented (Redis is outside ACID). SETNX
//     guard `saga:reverted:order:<id>` IS set. Next sweep tick:
//     revert.lua sees SETNX set → no-ops the Redis revert (correct,
//     don't double-INCRBY). The UPDATE retries; once it succeeds,
//     final state lands consistent. Self-healing under retry.
//   - revert.lua fails: tx rolls back. Both Redis and PG unchanged.
//     Next sweep retries the whole compensation.
//
// PG-symmetry: Stage 3's worker DECREMENTS event_ticket_types
// inside its UoW (message_processor.go's
// `repos.TicketType.DecrementTicket`). The compensator MUST
// increment back here. This is OPPOSITE to Stage 2's compensator
// (which leaves the column alone because Stage 2 has no worker
// decrementing it).
//
// Important: this rule applies ONLY to this compensator (the
// out-of-band path: sweeper + HandleTestConfirm). The worker's
// internal `handleFailure` in redis_queue.go does NOT need to
// IncrementTicket because its UoW rollback already undid the
// decrement atomically.
//
// Idempotency: revert.lua's SETNX guard short-circuits second
// calls; UPDATE orders' WHERE status='awaiting_payment' filter
// no-ops if the order is already compensated. Safe under
// concurrent /test/payment/confirm/:id?outcome=failed + sweeper
// races.
type stage3Compensator struct {
	db            *sql.DB
	inventoryRepo domain.InventoryRepository
}

func newStage3Compensator(db *sql.DB, inventoryRepo domain.InventoryRepository) *stage3Compensator {
	return &stage3Compensator{db: db, inventoryRepo: inventoryRepo}
}

func (s *stage3Compensator) Compensate(ctx context.Context, orderID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// `ticket_type_id` is `UUID NULL` per the schema (migration 000012
	// added it nullable; 000014 just renamed). Scanning into a bare
	// `uuid.UUID` would fail hard on a legacy NULL row with
	// "converting NULL to uuid.UUID is unsupported" — that error
	// does NOT match sql.ErrNoRows, so the next-line guard would
	// miss it and the sweeper would log spurious hard errors. Use
	// uuid.NullUUID + .Valid check to handle legacy rows cleanly.
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
		// Sweeper-vs-worker-PEL race: the sweeper queries by
		// `status='awaiting_payment' AND reserved_until <= ...` on a
		// previous tick, but by the time this Compensate runs the
		// row may have been cleared (concurrent /confirm-failed,
		// concurrent /confirm-succeeded landing JUST before the
		// sweep, or test cleanup). Treat as not-eligible so the
		// outer sweeper loop continues silently rather than logging
		// a hard error.
		return stagehttp.ErrCompensateNotEligible
	}
	if err != nil {
		return fmt.Errorf("lock order: %w", err)
	}
	if status != string(domain.OrderStatusAwaitingPayment) {
		return stagehttp.ErrCompensateNotEligible
	}
	if !ttID.Valid {
		// Legacy pre-D4.1 row with NULL ticket_type_id. The Redis
		// SoT is keyed by ticket_type_id; without one we can't
		// reverse-route the inventory. Skip silently — these rows
		// are operator-review territory (data migration backlog),
		// not sweeper-eligible.
		return stagehttp.ErrCompensateNotEligible
	}

	// revert.lua FIRST (inside the tx; see type doc for the recovery
	// analysis behind this ordering vs Stage 4's saga compensator).
	if err = s.inventoryRepo.RevertInventory(ctx, ttID.UUID, qty, "order:"+orderID.String()); err != nil {
		return fmt.Errorf("redis revert: %w", err)
	}

	// Symmetric PG increment — required because the worker UoW
	// decremented this column on the forward path. Asymmetric
	// would drift PG -qty per abandon (PG inventory leak).
	//
	// Capture the sql.Result and check RowsAffected: if the UPDATE
	// matched zero rows (orphaned ticket_type_id without a real
	// event_ticket_types row — schema FK should prevent this but
	// defense-in-depth catches data corruption / cross-DB drift),
	// silently no-op'ing the increment would let revert.lua AND
	// MarkCompensated commit while PG inventory stays decremented
	// → permanent PG leak. Hard-fail makes the sweeper retry the
	// next tick (revert.lua's SETNX guard short-circuits Redis
	// re-INCRBY), and the recurring error log gives ops a signal.
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

	// Raw SQL `UPDATE orders SET status='compensated'` — NOT via
	// `repos.Order.MarkCompensated`. The domain state machine's
	// MarkCompensated only accepts `Failed | Expired | PaymentFailed
	// → Compensated` and explicitly REJECTS `AwaitingPayment` →
	// Compensated. The compensator is intentionally crossing that
	// boundary directly: the abandon path is the entry point that
	// completes the `awaiting_payment → compensated` transition
	// without first marking expired or failed. Stages 1+2 use the
	// same raw-SQL pattern for the same reason. The WHERE filter
	// guards against concurrent state changes (pay-then-confirm
	// race) — UPDATE no-ops if status moved.
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

// Compile-time assertion: *stage3Compensator satisfies the
// stagehttp.Compensator interface.
var _ stagehttp.Compensator = (*stage3Compensator)(nil)

// sweepOnce — same shape as Stages 1+2 sweepers. Emits per-row
// `ErrCompensateNotEligible` errors silently (expected under the
// pay-then-confirm + worker PEL races); non-eligible errors are
// Error-logged so an operator sees them.
func sweepOnce(
	ctx context.Context,
	db *sql.DB,
	gracePeriod time.Duration,
	compensator stagehttp.Compensator,
	logger *mlog.Logger,
) {
	graceArg := fmt.Sprintf("%f seconds", gracePeriod.Seconds())
	rows, err := db.QueryContext(ctx, `
		SELECT id
		  FROM orders
		 WHERE status = 'awaiting_payment'
		   AND reserved_until <= NOW() - $1::interval
		 LIMIT 100`,
		graceArg)
	if err != nil {
		logger.Error(ctx, "stage3 sweeper find candidates", tag.Error(err))
		return
	}
	defer func() { _ = rows.Close() }()

	var orderIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			logger.Error(ctx, "stage3 sweeper scan candidate", tag.Error(err))
			continue
		}
		orderIDs = append(orderIDs, id)
	}
	if err := rows.Err(); err != nil {
		logger.Error(ctx, "stage3 sweeper rows iter", tag.Error(err))
	}
	_ = rows.Close()

	for _, id := range orderIDs {
		if ctx.Err() != nil {
			return
		}
		if err := compensator.Compensate(ctx, id); err != nil {
			if errors.Is(err, stagehttp.ErrCompensateNotEligible) {
				continue
			}
			logger.Error(ctx, "stage3 sweeper compensate failed",
				tag.OrderID(id), tag.Error(err))
		}
	}
}
