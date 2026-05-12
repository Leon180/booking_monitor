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

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/stagehttp"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
	"booking_monitor/internal/infrastructure/messaging"
	"booking_monitor/internal/infrastructure/observability"
)

// runServer is the `server` subcommand entry for Stage 5. Mirrors
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

		// cache.BaseModule provides Redis client + OrderQueue +
		// idempotency repository + observability collectors WITHOUT
		// the standard InventoryRepository — Stage 5 provides its
		// own inventory below, aliased to both InventoryRepository
		// and Stage5InventoryRepository so exactly one
		// *redisInventoryRepository is constructed and both consumers
		// (handleCreateEvent's domain.InventoryRepository injection
		// and the booking service's domain.Stage5InventoryRepository
		// injection) see the SAME instance. Using cache.Module here
		// would create a second instance via NewRedisInventoryRepository
		// — same *redis.Client under the hood, but wasted allocation
		// and a confusing graph.
		cache.BaseModule,

		// Single inventory provider aliased to both interfaces. fx.As
		// (multi-call form, fx v1.20+) registers the same constructor
		// result under both interface keys — no second instance.
		// NewStage5RedisInventoryRepository returns the Stage5
		// (DeductInventoryNoStream-exposing) view; that view embeds
		// InventoryRepository so the alias is safe.
		fx.Provide(
			fx.Annotate(
				cache.NewStage5RedisInventoryRepository,
				fx.As(new(domain.InventoryRepository)),
				fx.As(new(domain.Stage5InventoryRepository)),
			),
		),

		// Stage5Metrics — prometheus-backed counter for
		// stage5_kafka_publish_failures_total. The application layer
		// holds the booking.Stage5Metrics interface; this provider
		// injects the prometheus adapter from infrastructure.
		fx.Provide(observability.NewStage5Metrics),

		// IntakePublisher — Kafka producer for `booking.intake.v5`
		// with acks=all. Stage 5's BookingService calls this after
		// the Lua deduct succeeds; on Kafka publish failure the
		// service calls RevertInventory before returning the error.
		//
		// Provided as TWO keys (one constructor, one instance):
		//   1. *messaging.IntakePublisher (concrete) — used by
		//      installServer's OnStop hook to call Close() for clean
		//      Kafka writer shutdown.
		//   2. booking.IntakePublisher (interface) — used by the
		//      booking service so it never imports messaging /
		//      segmentio/kafka-go.
		// fx.As would drop the concrete type, so the second provider
		// is a thin pass-through that aliases the same pointer.
		fx.Provide(newIntakePublisher),
		fx.Provide(func(p *messaging.IntakePublisher) booking.IntakePublisher { return p }),

		// Stage 5 booking.Service — Lua deduct (no XADD) + Kafka
		// publish (acks=all) + revert.lua on publish failure.
		// Replaces Stages 2-4's booking.NewService.
		fx.Provide(booking.NewKafkaIntakeService),

		// Worker fx wiring — Stage 5 worker reads from Kafka
		// (`booking.intake.v5`), NOT Redis Stream. The actual Kafka
		// consumer impl arrives in Session 2; until then this
		// provider is omitted and the binary won't drain bookings
		// from Kafka (smoke verifies hot path only).
		//
		// fx.Provide(...) — Session 2.

		// Stage 5's stagehttp.Compensator: revert.lua + UPDATE
		// event_ticket_types += qty + UPDATE orders. Symmetric with
		// the worker's UoW that decrements event_ticket_types.
		fx.Provide(
			fx.Annotate(
				newStage5Compensator,
				fx.As(new(stagehttp.Compensator)),
			),
		),

		fx.Invoke(installServer),
	)
	app.Run()
}

// newIntakePublisher wires the messaging.IntakePublisher with the
// MessagingConfig pulled from the global config. Returns the concrete
// type so the fx graph can also access Close() for OnStop; the public
// alias as booking.IntakePublisher happens in the fx.Annotate call
// above so the BookingService receives the interface.
//
// WriteTimeout is read from cfg.Kafka.WriteTimeout (env
// KAFKA_WRITE_TIMEOUT, default 5s) rather than hardcoded so ops can
// tune the durability-gate latency vs availability tradeoff without
// rebuilding — the same dial the rest of the Kafka surface uses.
func newIntakePublisher(cfg *config.Config) *messaging.IntakePublisher {
	return messaging.NewIntakePublisher(messaging.MessagingConfig{
		Brokers:      cfg.Kafka.Brokers,
		WriteTimeout: cfg.Kafka.WriteTimeout,
	})
}

// installServer wires the HTTP layer + worker + expiry sweeper
// under a SINGLE shared `runCtx` so the OnStop chain coordinates
// all three concurrent goroutines. This is the Stage 4 pattern
// (cmd/booking-cli/server.go's `installServer` + `startBackground
// Runners` + `shutdownAll`) — Stage 5 inherits it because it has
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
	intakePublisher *messaging.IntakePublisher,
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
	v1.POST("/orders/:id/pay", stagehttp.HandlePayIntent(db, "pi_stage5_"))
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
			// Session 1 scaffolding warning: no Kafka consumer is
			// wired yet. /book deducts inventory + publishes to
			// `booking.intake.v5` successfully, but nothing drains
			// the topic — bookings accumulate in Kafka and orders
			// never land in PG. Smoke verifies the hot path
			// (acks=all latency, revert.lua on publish fail) only;
			// the consumer + drift reconciler arrive in Session 2.
			// Error-level log (not Warn) so a misconfigured prod
			// deploy is obvious in the first scrape.
			logger.Error(context.Background(),
				"stage5 SESSION 1 SCAFFOLDING — no Kafka consumer wired; bookings will accumulate in topic with no PG persistence")

			// HTTP server in its own goroutine; failure escalates
			// via shutdowner so a port-collision after OnStart returns
			// kills the process (k8s restart on the next probe).
			go func() {
				logger.Info(context.Background(), "stage5 HTTP server starting",
					mlog.String("addr", addr))
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error(context.Background(), "stage5 HTTP server failed — escalating fx.Shutdown",
						tag.Error(err))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()

			// Worker — Stage 5's Kafka consumer (reads `booking.intake.v5`
			// and INSERTs to PG). Wired in Session 2; not present in
			// Session 1 scaffolding. Bookings accepted in Session 1
			// pile up in Kafka with no consumer, which is fine for
			// hot-path smoke verification (the consumer's job is
			// strictly PG persistence + ACKing the offset).
			//
			// go func() { workerSvc.Start(runCtx) ... }  ← Session 2

			// Expiry sweeper — same shape as Stages 1+2 sweepers.
			wg.Add(1)
			go func() {
				defer wg.Done()
				logger.Info(runCtx, "stage5 expiry sweeper starting",
					mlog.Duration("sweep_interval", sweepInterval),
					mlog.Duration("grace_period", gracePeriod))
				ticker := time.NewTicker(sweepInterval)
				defer ticker.Stop()
				for {
					select {
					case <-runCtx.Done():
						logger.Info(context.Background(), "stage5 expiry sweeper stopping")
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
				logger.Error(stopCtx, "stage5 HTTP shutdown error", tag.Error(err))
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
				// 4. Close the Kafka writer AFTER the HTTP server +
				//    background goroutines have drained — guarantees
				//    no in-flight Publish call races with Close().
				//    The writer's own bounded-close (10s ceiling
				//    inside IntakePublisher.Close) keeps OnStop
				//    bounded even if the broker is unreachable.
				//    Log + continue on error rather than failing
				//    OnStop, since by this point the data plane is
				//    already drained.
				if err := intakePublisher.Close(); err != nil {
					logger.Error(stopCtx, "stage5 IntakePublisher.Close error", tag.Error(err))
				}
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

// handleCreateEvent is the Stage 5 /api/v1/events handler. Same
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
				logger.Error(c.Request.Context(), "stage5 COMPENSATION FAILED — dangling event rows",
					tag.EventID(eventID),
					tag.TicketTypeID(ttID),
					mlog.NamedError("redis_error", err),
					mlog.NamedError("compensation_error", compErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "redis hydrate + compensation both failed"})
				return
			}
			logger.Warn(c.Request.Context(), "stage5 compensated dangling event after Redis hydrate failure",
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

// stage5Compensator is the abandon-path Compensator for Stage 5.
// Symmetric with the worker's UoW: forward decrements PG, backward
// increments PG. Mirrors Stage 4's saga-compensator semantics but
// runs in-process (Stage 5 has no Kafka, no out-of-process saga).
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
// PG-symmetry: Stage 5's worker DECREMENTS event_ticket_types
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
type stage5Compensator struct {
	db            *sql.DB
	inventoryRepo domain.InventoryRepository
}

func newStage5Compensator(db *sql.DB, inventoryRepo domain.InventoryRepository) *stage5Compensator {
	return &stage5Compensator{db: db, inventoryRepo: inventoryRepo}
}

func (s *stage5Compensator) Compensate(ctx context.Context, orderID uuid.UUID) error {
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

// Compile-time assertion: *stage5Compensator satisfies the
// stagehttp.Compensator interface.
var _ stagehttp.Compensator = (*stage5Compensator)(nil)

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
		logger.Error(ctx, "stage5 sweeper find candidates", tag.Error(err))
		return
	}
	defer func() { _ = rows.Close() }()

	var orderIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			logger.Error(ctx, "stage5 sweeper scan candidate", tag.Error(err))
			continue
		}
		orderIDs = append(orderIDs, id)
	}
	if err := rows.Err(); err != nil {
		logger.Error(ctx, "stage5 sweeper rows iter", tag.Error(err))
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
			logger.Error(ctx, "stage5 sweeper compensate failed",
				tag.OrderID(id), tag.Error(err))
		}
	}
}
