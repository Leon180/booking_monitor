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
	"booking_monitor/internal/application/booking/synclua"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/stagehttp"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runServer is the `server` subcommand entry for Stage 2. Mirrors
// Stage 1's runServer but adds the Redis client + Stage 2's
// inventory repo + sync-lua deducter to the fx graph. All other
// providers (booking.Service, Compensator, /events handler,
// expiry sweeper) follow the same contract — only their
// implementations differ.
func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),

		// Redis primitives. cache.NewRedisClient pings Redis on
		// construction, so a missing / misconfigured PASSWORD fails
		// fast at startup rather than at first /book — same fail-fast
		// posture as Stage 1's bootstrap.NewDB.
		fx.Provide(cache.NewRedisClient),
		fx.Provide(cache.NewRedisInventoryRepository),

		// Stage 2's deducter. Provided as the synclua.Deducter
		// interface so synclua.NewService resolves to it via
		// type-injection without an explicit binding tag at the
		// constructor site.
		fx.Provide(
			fx.Annotate(
				cache.NewRedisSyncDeducter,
				fx.As(new(synclua.Deducter)),
			),
		),

		// Stage 2's booking.Service: synclua. fx.Annotate(...,
		// fx.As(new(booking.Service))) advertises the narrow
		// booking.Service interface so the stagehttp handlers
		// resolve to our impl via the same shape Stage 1 uses.
		fx.Provide(
			fx.Annotate(
				synclua.NewService,
				fx.As(new(booking.Service)),
			),
		),

		// Stage 2's stagehttp.Compensator: revert.lua against the
		// Redis ticket_type_qty key + UPDATE orders status only.
		// Symmetric with the forward path: Stage 2 does NOT touch
		// `event_ticket_types.available_tickets` on either side, so
		// the PG column stays at its seeded `total_tickets` value
		// and Redis qty is the only authoritative live count.
		fx.Provide(
			fx.Annotate(
				newStage2Compensator,
				fx.As(new(stagehttp.Compensator)),
			),
		),

		fx.Invoke(registerHTTPServer),
		fx.Invoke(registerExpirySweeper),
	)
	app.Run()
}

// registerHTTPServer wires the HTTP layer for Stage 2. Same
// stagehttp shared handlers as Stage 1, only the /events handler
// differs (Stage 2 hydrates Redis after the PG insert) and the
// PaymentIntent prefix changes to `pi_stage2_` so logs / metrics
// can attribute by stage.
func registerHTTPServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	cfg *config.Config,
	db *sql.DB,
	inventoryRepo domain.InventoryRepository,
	bookingService booking.Service,
	compensator stagehttp.Compensator,
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
	v1.POST("/orders/:id/pay", stagehttp.HandlePayIntent(db, "pi_stage2_"))
	v1.POST("/events", handleCreateEvent(db, inventoryRepo, logger))

	router.POST("/test/payment/confirm/:id", stagehttp.HandleTestConfirm(db, compensator))

	addr := ":" + cfg.Server.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				logger.Info(context.Background(), "stage2 HTTP server starting",
					mlog.String("addr", addr))
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error(context.Background(), "stage2 HTTP server failed — escalating fx.Shutdown",
						tag.Error(err))
					_ = shutdowner.Shutdown(fx.ExitCode(1))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		},
	})
}

// handleCreateEvent is the Stage 2 /api/v1/events handler.
//
// Two-step shape with compensation:
//
//  1. PG transaction inserts events row + a single default
//     ticket_type row (same SQL as Stage 1 for byte-identical
//     schema rollout).
//  2. AFTER the PG commit, hydrate Redis runtime keys via
//     `inventoryRepo.SetTicketTypeRuntime` (HSET ticket_type_meta
//     + SET ticket_type_qty). If hydrate fails, run
//     `compensateDanglingEvent` against a detached 5s context and
//     return 500 to the client.
//
// Why compensation (NOT best-effort hydrate): without the
// `ticket_type_qty:{id}` key, the first /book request DECRBYs a
// non-existent key, gets -count, hits the Lua sold_out branch,
// and INCRBY-restores qty to 0 — the metadata_missing repair path
// never runs because the qty check fires first. The event would
// be permanently un-bookable until manual rehydrate. The
// production event service (`internal/application/event/
// service.go`) treats this as request failure with PG
// compensation for the same reason; Stage 2 mirrors the pattern.
//
// Order matters: PG-first then Redis. If we wrote Redis first and
// PG failed, the orphan Redis state would silently let bookings
// trade against an event that doesn't exist in PG.
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

		// PG committed — hydrate Redis runtime state. Build a
		// domain.TicketType via ReconstructTicketType (skip
		// invariant validation: the values just round-tripped
		// through the binding tags + the PG insert).
		tt := domain.ReconstructTicketType(
			ttID, eventID, "GA",
			r.PriceCents, r.Currency,
			r.TotalTickets, r.TotalTickets,
			nil, nil, nil, "",
			0,
		)
		if err := inventoryRepo.SetTicketTypeRuntime(c.Request.Context(), tt); err != nil {
			// Detached 5s ctx so a cancelled request ctx (HTTP
			// client timeout was the cause of the Redis failure)
			// doesn't fail the cleanup with context.Canceled.
			// Mirrors event.service's compensateDanglingEvent
			// budget convention.
			compensateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if compErr := compensateDanglingEvent(compensateCtx, db, eventID, ttID); compErr != nil {
				// Both Redis hydrate AND compensation failed —
				// dangling event + ticket_type rows in PG with no
				// Redis state. Surface BOTH errors so an operator
				// querying logs by `redis_error=` finds them.
				logger.Error(c.Request.Context(), "stage2 COMPENSATION FAILED — dangling event rows",
					tag.EventID(eventID),
					tag.TicketTypeID(ttID),
					mlog.NamedError("redis_error", err),
					mlog.NamedError("compensation_error", compErr))
				c.JSON(http.StatusInternalServerError, gin.H{"error": "redis hydrate + compensation both failed"})
				return
			}
			logger.Warn(c.Request.Context(), "stage2 compensated dangling event after Redis hydrate failure",
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
// post-commit Redis hydrate (`SetTicketTypeRuntime`) fails — the
// goal is to leave PG clean so a retry of /events can re-Create
// without colliding with the orphan rows.
//
// FK ordering: event_ticket_types.event_id → events.id, so the
// ticket_type DELETE must run first. Both DELETEs are idempotent
// (no-op on missing row) so a partial earlier compensation can
// re-run safely. ctx is detached from the request ctx by the
// caller — see handleCreateEvent's call site for why.
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

// stage2Compensator is the abandon-path Compensator for Stage 2.
// The compensation tx is symmetric with Stage 2's forward path:
//
//	BEGIN
//	  SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
//	  if status != 'awaiting_payment' → ErrCompensateNotEligible (rollback)
//	  revert.lua against ticket_type_qty:{ttID}     ← only Stage 2 has this step
//	  UPDATE orders SET status='compensated' WHERE status='awaiting_payment'
//	COMMIT
//
// CRITICAL: this compensator does NOT update
// `event_ticket_types.available_tickets`. Stage 2's forward path
// also doesn't update it (Redis is the inventory SoT on the hot
// path), so an asymmetric compensator that incremented the PG
// column would inflate it relative to the seeded total — turning
// every abandon into a permanent +qty drift on the PG side. PG
// admin / drift-detector readers see the seeded `total_tickets`
// minus nothing on the booking side and minus nothing on the
// compensation side; the snapshot stays consistent at the seeded
// value, and Redis qty is the only authoritative live count.
//
// Idempotency: revert.lua's `saga:reverted:order:<id>` SETNX guard
// short-circuits the second call, so a crash + retry between
// revert.lua and COMMIT is safe — the next sweep sees SETNX
// already set, no-ops the Redis revert, and the orders UPDATE
// runs normally to land the final 'compensated' state. Loud
// over-revert beats silent under-revert (see revert.lua header
// comment for the appendfsync rationale).
type stage2Compensator struct {
	db            *sql.DB
	inventoryRepo domain.InventoryRepository
}

func newStage2Compensator(db *sql.DB, inventoryRepo domain.InventoryRepository) *stage2Compensator {
	return &stage2Compensator{db: db, inventoryRepo: inventoryRepo}
}

func (s *stage2Compensator) Compensate(ctx context.Context, orderID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// `ticket_type_id` is `UUID NULL` per the schema (migration 000012
	// added it nullable). Scanning into a bare `uuid.UUID` would fail
	// hard on a legacy NULL row with "converting NULL to uuid.UUID is
	// unsupported" — that error does NOT match sql.ErrNoRows, so the
	// sweeper would log spurious hard errors. Use uuid.NullUUID to
	// handle legacy rows cleanly. Round-3 agent review found this in
	// Stage 3; Stage 2 inherited the same pattern and is fixed here.
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
		// Sweeper-vs-confirm race: the row may be deleted between
		// the sweeper's SELECT and this FOR UPDATE. Treat as not-
		// eligible so the sweeper continues silently.
		return stagehttp.ErrCompensateNotEligible
	}
	if err != nil {
		return fmt.Errorf("lock order: %w", err)
	}
	if status != string(domain.OrderStatusAwaitingPayment) {
		return stagehttp.ErrCompensateNotEligible
	}
	if !ttID.Valid {
		// Legacy pre-D4.1 row with NULL ticket_type_id — Redis SoT
		// is keyed by ticket_type_id, can't reverse-route. Skip
		// silently (operator-review territory, not sweeper-eligible).
		return stagehttp.ErrCompensateNotEligible
	}

	// revert.lua FIRST (within the tx scope so a Redis failure
	// rolls back the orders UPDATE and the next sweep retries the
	// whole compensation). The SETNX guard makes the retry idempotent.
	if err = s.inventoryRepo.RevertInventory(ctx, ttID.UUID, qty, "order:"+orderID.String()); err != nil {
		return fmt.Errorf("redis revert: %w", err)
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

// Compile-time assertion: *stage2Compensator satisfies the
// stagehttp.Compensator interface.
var _ stagehttp.Compensator = (*stage2Compensator)(nil)

// registerExpirySweeper wires the in-binary expiry sweeper for
// Stage 2's abandon path. The goroutine + shutdown semantics are
// identical to Stage 1's; the only difference is the injected
// Compensator (now stage2Compensator) which adds the revert.lua
// step. fx's type system bridges the difference automatically.
func registerExpirySweeper(
	lc fx.Lifecycle,
	db *sql.DB,
	cfg *config.Config,
	compensator stagehttp.Compensator,
	logger *mlog.Logger,
) {
	sweepInterval := cfg.Expiry.SweepInterval
	gracePeriod := cfg.Expiry.ExpiryGracePeriod

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			wg.Add(1)
			go func() {
				defer wg.Done()
				logger.Info(ctx, "stage2 expiry sweeper starting",
					mlog.Duration("sweep_interval", sweepInterval),
					mlog.Duration("grace_period", gracePeriod))
				ticker := time.NewTicker(sweepInterval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						logger.Info(context.Background(), "stage2 expiry sweeper stopping")
						return
					case <-ticker.C:
						sweepOnce(ctx, db, gracePeriod, compensator, logger)
					}
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
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

// sweepOnce — same shape as Stage 1's. The compensator does the
// Stage-2-specific work (revert.lua); this loop just feeds it
// candidate orderIDs.
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
		logger.Error(ctx, "stage2 sweeper find candidates", tag.Error(err))
		return
	}
	defer func() { _ = rows.Close() }()

	var orderIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			logger.Error(ctx, "stage2 sweeper scan candidate", tag.Error(err))
			continue
		}
		orderIDs = append(orderIDs, id)
	}
	if err := rows.Err(); err != nil {
		logger.Error(ctx, "stage2 sweeper rows iter", tag.Error(err))
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
			logger.Error(ctx, "stage2 sweeper compensate failed",
				tag.OrderID(id), tag.Error(err))
		}
	}
}
