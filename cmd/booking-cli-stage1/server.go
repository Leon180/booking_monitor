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
	bookingsync "booking_monitor/internal/application/booking/sync"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/stagehttp"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runServer is the `server` subcommand entry for Stage 1. Loads
// config + wires fx + blocks on app.Run until SIGINT/SIGTERM.
//
// Compared to cmd/booking-cli/server.go this is intentionally tiny:
//   - No Redis client, no Kafka client, no worker module.
//   - No outbox relay, no payment service, no saga consumer.
//   - No recon / saga-watchdog subcommands.
//   - No webhook handler.
//   - No idempotency middleware (Stage 4 has it; Stages 1-3 opt out
//     so the comparison surfaces stage-1 sync cost without the
//     idempotency-fingerprint overhead per CP8).
//
// What it does have: bootstrap.CommonModule (DB + log + repos +
// runtime metrics), the Stage 1 sync booking service, the
// stagehttp HTTP layer (shared with Stages 2-3), the in-binary
// expiry sweeper goroutine for the abandon path, and a thin
// stage1-specific event creation handler (PG-only — no Redis hot-
// cache hydration).
func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),

		// Stage 1's booking.Service: pure synchronous SELECT FOR
		// UPDATE. fx.Annotate(..., fx.As(...)) advertises the narrow
		// booking.Service interface so consumers (the stagehttp
		// handlers) resolve to our sync impl.
		fx.Provide(
			fx.Annotate(
				bookingsync.NewService,
				fx.As(new(booking.Service)),
			),
		),

		// Stage 1's stagehttp.Compensator: SQL-only revert tx (no
		// Redis or Kafka). Same fx.As pattern so HandleTestConfirm
		// + the in-binary expiry sweeper resolve to this impl.
		fx.Provide(
			fx.Annotate(
				newStage1Compensator,
				fx.As(new(stagehttp.Compensator)),
			),
		),

		fx.Invoke(registerHTTPServer),
		fx.Invoke(registerExpirySweeper),
	)
	app.Run()
}

// registerHTTPServer wires the HTTP layer for Stage 1. Routes are
// registered through stagehttp's shared handlers (HandleBook,
// HandleGetOrder, HandlePayIntent, HandleTestConfirm) plus
// stage1-specific handleCreateEvent.
//
// fx.Shutdowner is injected so a ListenAndServe failure after
// OnStart returns (port-already-in-use during a benchmark restart,
// EADDRINUSE, etc.) escalates to fx.Shutdown(ExitCode(1)) instead
// of being logged + swallowed (Codex slice-3 P2 fix).
func registerHTTPServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	cfg *config.Config,
	db *sql.DB,
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
	v1.POST("/orders/:id/pay", stagehttp.HandlePayIntent(db, "pi_stage1_"))
	v1.POST("/events", handleCreateEvent(db))
	// /history not registered — Stage 1 doesn't need it for the
	// comparison harness.

	// /test routes (root-mounted, NOT under /api/v1) — matches
	// Stage 4's mount point so scripts/k6_two_step_flow.js + the
	// browser demo wire-format both work unchanged.
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
				logger.Info(context.Background(), "stage1 HTTP server starting",
					mlog.String("addr", addr))
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error(context.Background(), "stage1 HTTP server failed — escalating fx.Shutdown",
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

// handleCreateEvent is the Stage 1 /api/v1/events handler. Inserts
// an event row + a single default ticket_type row directly via SQL,
// PG-only (no Redis hot-cache hydration like the full event service
// does) — Stage 1's whole architectural point is no Redis. Stages
// 2/3 will provide their own /events handlers since they need to
// hydrate Redis on event create; the handler stays stage-specific
// rather than going into stagehttp.
func handleCreateEvent(db *sql.DB) gin.HandlerFunc {
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

		// Note: events.available_tickets is initialized for schema
		// compatibility but Stage 1's BookTicket never reads it
		// (event_ticket_types is the SoT post-D4.1).
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

// stage1Compensator is the SQL-only Compensator implementation for
// Stage 1. Wraps a *sql.DB and runs the abandon-path compensation
// transaction:
//
//	BEGIN
//	  SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
//	  if status != 'awaiting_payment' → ErrCompensateNotEligible (rolled back)
//	  UPDATE event_ticket_types SET available_tickets += quantity, version += 1
//	  UPDATE orders SET status='compensated' WHERE status='awaiting_payment'
//	COMMIT
//
// Used by both the stagehttp HandleTestConfirm (outcome=failed
// branch) and the in-binary expiry sweeper goroutine. Stage 2's
// Compensator does revert.lua + UPDATE orders only (no PG inventory
// column update — symmetric with its forward path). Stage 3's
// Compensator does revert.lua + UPDATE event_ticket_types += qty +
// UPDATE orders (symmetric with its async-worker forward path that
// decrements event_ticket_types). See stagehttp/compensator.go's
// interface doc for the full per-stage rationale.
type stage1Compensator struct {
	db *sql.DB
}

func newStage1Compensator(db *sql.DB) *stage1Compensator {
	return &stage1Compensator{db: db}
}

func (s *stage1Compensator) Compensate(ctx context.Context, orderID uuid.UUID) error {
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
		return stagehttp.ErrCompensateNotEligible
	}

	if _, err = tx.ExecContext(ctx, `
		UPDATE event_ticket_types
		   SET available_tickets = available_tickets + $1,
		       version = version + 1
		 WHERE id = $2`,
		qty, ttID); err != nil {
		return fmt.Errorf("revert inventory: %w", err)
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

// Compile-time assertion: *stage1Compensator satisfies the
// stagehttp.Compensator interface.
var _ stagehttp.Compensator = (*stage1Compensator)(nil)

// registerExpirySweeper wires the in-binary expiry sweeper goroutine
// for Stage 1's abandon path. Mirrors Stage 4's D6 expiry sweeper +
// saga compensator combined into one in-process loop — Stage 1 has
// no Kafka, so there's no out-of-process saga consumer; the sweeper
// directly calls the per-stage Compensator.
//
// Cadence + grace period reuse cfg.Expiry.SweepInterval and
// cfg.Expiry.ExpiryGracePeriod so the existing demo-up env override
// (EXPIRY_SWEEP_INTERVAL=5s) propagates without Stage 1 needing its
// own knob.
//
// OnStop is bounded by fx's stop deadline (Codex slice-5 P2 fix):
// if a sweepOnce DB call stalls, fx finishes its OnStop chain
// instead of hanging benchmark teardown.
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
				logger.Info(ctx, "stage1 expiry sweeper starting",
					mlog.Duration("sweep_interval", sweepInterval),
					mlog.Duration("grace_period", gracePeriod))
				ticker := time.NewTicker(sweepInterval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						logger.Info(context.Background(), "stage1 expiry sweeper stopping")
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

// sweepOnce runs one tick of the Stage 1 expiry sweeper. Selects
// candidate awaiting_payment orders past `reserved_until + grace`,
// then calls compensator.Compensate for each.
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
		logger.Error(ctx, "stage1 sweeper find candidates", tag.Error(err))
		return
	}
	defer func() { _ = rows.Close() }()

	var orderIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			logger.Error(ctx, "stage1 sweeper scan candidate", tag.Error(err))
			continue
		}
		orderIDs = append(orderIDs, id)
	}
	if err := rows.Err(); err != nil {
		logger.Error(ctx, "stage1 sweeper rows iter", tag.Error(err))
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
			logger.Error(ctx, "stage1 sweeper compensate failed",
				tag.OrderID(id), tag.Error(err))
		}
	}
}
