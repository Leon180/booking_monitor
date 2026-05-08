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
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application/booking"
	bookingsync "booking_monitor/internal/application/booking/sync"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/google/uuid"
)

// ErrCompensateNotEligible is returned by compensateAwaitingOrder
// when the order isn't in `awaiting_payment` state at lock time.
// Callers branch on this so concurrent races (e.g., /confirm-
// succeeded landing between sweeper-SELECT and FOR UPDATE) don't
// log as errors.
var ErrCompensateNotEligible = errors.New("order not awaiting_payment at lock time")

// runServer is the `server` subcommand entry for Stage 1. Loads
// config + wires fx + blocks on app.Run until SIGINT/SIGTERM.
//
// Compared to cmd/booking-cli/server.go this is intentionally tiny:
//   - No Redis client, no Kafka client, no worker module.
//   - No outbox relay, no payment service, no saga consumer.
//   - No recon / saga-watchdog / expiry-sweeper subcommands.
//   - No webhook handler, no test-confirm handler (slice 4).
//   - No idempotency middleware (Stage 4 has it; Stages 1-3 opt out
//     so the comparison surfaces stage-1 sync cost without the
//     idempotency-fingerprint overhead per CP8).
//
// What it does have: bootstrap.CommonModule (DB + log + repos +
// runtime metrics), the Stage 1 sync booking service, and a thin
// HTTP layer with /book, /orders/:id, /events, /livez, /metrics.
// The /pay + mock-confirm endpoints + in-binary expiry sweeper
// land in subsequent slices on this branch.
func runServer(_ *cobra.Command, _ []string) {
	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("Failed to load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),

		// Stage 1's booking.Service: pure synchronous SELECT FOR
		// UPDATE. The sync.NewService constructor returns a
		// *sync.Service; we wrap it in fx.Annotate(..., fx.As(...))
		// so consumers requesting `booking.Service` (the shared
		// interface that Stages 2-4 also implement) resolve to
		// our sync impl.
		fx.Provide(
			fx.Annotate(
				bookingsync.NewService,
				fx.As(new(booking.Service)),
			),
		),

		fx.Invoke(registerHTTPServer),
		fx.Invoke(registerExpirySweeper),
	)
	app.Run()
}

// registerExpirySweeper wires the in-binary expiry sweeper goroutine
// for Stage 1's abandon path. Mirrors Stage 4's D6 expiry sweeper +
// saga compensator combined into one in-process loop — Stage 1 has
// no Kafka, so there's no out-of-process saga consumer; the sweeper
// directly performs the compensation tx via compensateAwaitingOrder.
//
// Cadence + grace period reuse `cfg.Expiry.SweepInterval` and
// `cfg.Expiry.ExpiryGracePeriod` so the existing demo-up env override
// (`EXPIRY_SWEEP_INTERVAL=5s`) propagates without Stage 1 needing its
// own knob.
func registerExpirySweeper(
	lc fx.Lifecycle,
	db *sql.DB,
	cfg *config.Config,
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
						sweepOnce(ctx, db, gracePeriod, logger)
					}
				}
			}()
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			wg.Wait()
			return nil
		},
	})
}

// sweepOnce runs one tick of the Stage 1 expiry sweeper. Selects
// candidate awaiting_payment orders past `reserved_until + grace`,
// then calls compensateAwaitingOrder for each.
//
// Two-phase (SELECT candidates → loop FOR UPDATE per row) instead of
// a single batched UPDATE because the row-lock + inventory revert
// MUST run per-order to keep the per-row idempotency contract: a
// concurrent /confirm-succeeded landing during the sweep races for
// the same row's FOR UPDATE; whichever loses sees status !=
// awaiting_payment and returns ErrCompensateNotEligible (skip, not
// error). A single batched UPDATE would silently lose this race.
func sweepOnce(ctx context.Context, db *sql.DB, gracePeriod time.Duration, logger *mlog.Logger) {
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
			// Shutdown signaled mid-batch; abandon remaining work
			// gracefully (next tick will pick up where we left off).
			return
		}
		if err := compensateAwaitingOrder(ctx, db, id); err != nil {
			if errors.Is(err, ErrCompensateNotEligible) {
				// Race with /confirm-succeeded between our SELECT and
				// FOR UPDATE — expected, debug-level rather than error.
				continue
			}
			logger.Error(ctx, "stage1 sweeper compensate failed",
				tag.OrderID(id), tag.Error(err))
		}
	}
}

// registerHTTPServer wires the HTTP layer for Stage 1. Routes are
// inlined here rather than going through internal/infrastructure/api
// because Stage 1 only has a subset of the endpoints + we don't want
// to depend on payment/event subpackage initialization.
//
// fx.Shutdowner is injected so a ListenAndServe failure after OnStart
// returns (port-already-in-use during a benchmark restart, EADDRINUSE,
// etc.) escalates to fx.Shutdown(ExitCode(1)) instead of being logged
// + swallowed. Without this, the comparison harness would see
// connection-refused errors against a running fx app that has no
// HTTP listener — failing late as benchmark errors instead of fast
// at startup. Mirrors cmd/booking-cli/server.go's startHTTPServer
// pattern.
func registerHTTPServer(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	cfg *config.Config,
	db *sql.DB,
	bookingService booking.Service,
	logger *mlog.Logger,
) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	// /livez — liveness probe; matches Stage 4's k8s-probe semantics
	// (no dependency check; just "process is up").
	router.GET("/livez", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// /metrics — Prometheus default registry; bootstrap.CommonModule
	// wires the Go runtime + DB pool collectors into this registry.
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// /api/v1 routes
	v1 := router.Group(apiV1Prefix)
	v1.POST("/book", handleBook(bookingService, logger))
	v1.GET("/orders/:id", handleGetOrder(bookingService, logger))
	v1.POST("/orders/:id/pay", handlePayIntent(db, logger))
	v1.POST("/events", handleCreateEvent(db, logger))
	// /history not registered — Stage 1 doesn't need it for the
	// comparison harness; can add later if k6 grows a use case.

	// /test routes (root-mounted, NOT under /api/v1) — matches
	// Stage 4's mount point so the existing scripts/k6_two_step_flow.js
	// + browser demo wire-format both work unchanged.
	router.POST("/test/payment/confirm/:id", handleTestConfirm(db, logger))

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
					// Escalate to fx so the process exits non-zero
					// instead of staying up with a dead listener.
					// k8s / docker compose restart policy then cycles
					// the container; benchmark orchestration sees a
					// fast startup failure instead of late connection
					// errors against a phantom server.
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

// handleBook is the Stage 1 /api/v1/book handler. Mirrors the
// Stage 4 dto contract:
//
//	202 Accepted with {order_id, status:"reserved", reserved_until,
//	                   expires_in_seconds, links.{self,pay}}
//
// All four stages return the same shape so the existing
// scripts/k6_two_step_flow.js runs unmodified.
func handleBook(svc booking.Service, logger *mlog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req dto.BookingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		order, err := svc.BookTicket(c.Request.Context(), req.UserID, req.TicketTypeID, req.Quantity)
		if err != nil {
			status, msg := mapBookingError(err)
			c.JSON(status, gin.H{"error": msg})
			return
		}

		expiresIn := int(time.Until(order.ReservedUntil()).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
		c.JSON(http.StatusAccepted, dto.BookingAcceptedResponse{
			OrderID:          order.ID(),
			Status:           dto.BookingStatusReserved,
			Message:          "reservation created; complete payment via links.pay before reserved_until",
			ReservedUntil:    order.ReservedUntil(),
			ExpiresInSeconds: expiresIn,
			Links: dto.BookingLinks{
				Self: fmt.Sprintf("%s/orders/%s", apiV1Prefix, order.ID()),
				Pay:  fmt.Sprintf("%s/orders/%s/pay", apiV1Prefix, order.ID()),
			},
		})
		_ = logger // reserved for structured logging in slice 4
	}
}

// handleGetOrder is the Stage 1 /api/v1/orders/:id handler.
// Returns the persisted order or 404 if not found.
func handleGetOrder(svc booking.Service, logger *mlog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := uuid.Parse(idStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}

		order, err := svc.GetOrder(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, domain.ErrOrderNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get order"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"order_id":          order.ID(),
			"status":            string(order.Status()),
			"amount_cents":      order.AmountCents(),
			"currency":          order.Currency(),
			"payment_intent_id": order.PaymentIntentID(),
			"reserved_until":    order.ReservedUntil(),
		})
		_ = logger
	}
}

// handleCreateEvent is the Stage 1 /api/v1/events handler. Inserts
// an event row + a single default ticket_type row directly via SQL,
// PG-only (no Redis hot-cache hydration like the full event service
// does) — Stage 1's whole architectural point is no Redis.
//
// k6 setup creates one event per run via this endpoint. Returns the
// same wire shape as Stage 4's response so the existing
// scripts/k6_two_step_flow.js setup() function works unmodified.
func handleCreateEvent(db *sql.DB, logger *mlog.Logger) gin.HandlerFunc {
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

		// Note: Stage 4's POST /events writes to events with
		// available_tickets seeded BUT post-D4.1 that column is
		// frozen — only ticket_type.available_tickets is the SoT.
		// We initialize events.available_tickets to total_tickets
		// for schema compatibility but Stage 1's BookTicket never
		// reads it.
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
		_ = logger
	}
}

// handlePayIntent is the Stage 1 /api/v1/orders/:id/pay handler.
// Generates a fake `pi_stage1_<uuid>` intent id and persists it on
// the order row via UPDATE WHERE payment_intent_id IS NULL — atomic
// idempotency: a repeat /pay returns the SAME intent id (matches
// Stage 4's gateway-side idempotency behavior).
//
// Status guards mirror Stage 4 (per Codex round-2 P2.1, the mock-
// payment contract must be apples-to-apples):
//   - 404 if order doesn't exist
//   - 409 if status != awaiting_payment (already terminal)
//   - 409 if reserved_until elapsed (TTL ran out before /pay)
//
// The "client_secret" return is a stub literal — Stage 1 has no
// real gateway, but the wire shape stays identical so the existing
// k6 script + browser demo treat all four stages the same.
func handlePayIntent(db *sql.DB, logger *mlog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		orderID, err := uuid.Parse(idStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}

		// Single-row read first: status guards + read existing
		// intent_id (so a re-pay is idempotent without taking a
		// row lock).
		var (
			status        string
			currentIntent sql.NullString
			amountCents   int64
			currency      string
			reservedUntil time.Time
		)
		err = db.QueryRowContext(c.Request.Context(), `
			SELECT status, payment_intent_id, amount_cents, currency, reserved_until
			  FROM orders
			 WHERE id = $1`,
			orderID).Scan(&status, &currentIntent, &amountCents, &currency, &reservedUntil)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read order"})
			return
		}
		if status != string(domain.OrderStatusAwaitingPayment) {
			c.JSON(http.StatusConflict, gin.H{"error": "order not in awaiting_payment state: " + status})
			return
		}
		if !time.Now().Before(reservedUntil) {
			c.JSON(http.StatusConflict, gin.H{"error": "reservation expired"})
			return
		}

		// Idempotent return: if the order already has an intent,
		// reuse it. Stage 4's gateway is idempotent on order_id;
		// matching that contract here.
		if currentIntent.Valid && currentIntent.String != "" {
			c.JSON(http.StatusOK, gin.H{
				"order_id":          orderID,
				"payment_intent_id": currentIntent.String,
				"client_secret":     "stub_secret_stage1_" + currentIntent.String,
				"amount_cents":      amountCents,
				"currency":          currency,
			})
			return
		}

		// Generate + persist a fresh intent. Filter on
		//   payment_intent_id IS NULL  — concurrent /pay can't double-write
		//   status = 'awaiting_payment' — terminal states reject
		//   reserved_until > NOW()     — atomic TTL guard (Codex slice-4 P2)
		//
		// The reserved_until guard MUST live in the UPDATE itself, not
		// only in the eligibility read above. Without it, a race window
		// exists where the read sees TTL valid but the write happens
		// after expiry — the UPDATE's loose predicate would still match
		// (status hasn't flipped to expired yet because nothing has run
		// the expiry sweeper) and Stage 1 would silently persist an
		// intent for an expired reservation. Stage 4's SetPaymentIntentID
		// has the same predicate; matching it here keeps the comparison
		// benchmark from measuring a looser payment contract in Stage 1.
		intentSuffix, err := uuid.NewV7()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "mint intent id"})
			return
		}
		intentID := "pi_stage1_" + intentSuffix.String()
		res, err := db.ExecContext(c.Request.Context(), `
			UPDATE orders
			   SET payment_intent_id = $1
			 WHERE id = $2
			   AND payment_intent_id IS NULL
			   AND status = 'awaiting_payment'
			   AND reserved_until > NOW()`,
			intentID, orderID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "persist intent"})
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// RowsAffected=0 disambiguation. Three causes:
			//   (a) concurrent /pay won — row now has an intent; return it idempotently
			//   (b) reserved_until elapsed between read and write — return 409 expired
			//   (c) status changed terminal (paid/compensated/expired/payment_failed)
			//       between read and write — return 409 not eligible
			// Re-read all three fields to figure out which case we're in.
			var (
				reReadStatus    string
				reReadIntent    sql.NullString
				reReadReservedAt time.Time
			)
			err = db.QueryRowContext(c.Request.Context(), `
				SELECT status, payment_intent_id, reserved_until
				  FROM orders
				 WHERE id = $1`,
				orderID).Scan(&reReadStatus, &reReadIntent, &reReadReservedAt)
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "re-read order after race"})
				return
			}
			if reReadStatus != string(domain.OrderStatusAwaitingPayment) {
				c.JSON(http.StatusConflict, gin.H{"error": "order not in awaiting_payment state: " + reReadStatus})
				return
			}
			if !time.Now().Before(reReadReservedAt) {
				c.JSON(http.StatusConflict, gin.H{"error": "reservation expired"})
				return
			}
			// Status is awaiting_payment + reserved_until still future —
			// the only remaining cause is (a) concurrent /pay won.
			if reReadIntent.Valid && reReadIntent.String != "" {
				intentID = reReadIntent.String
			} else {
				// Defensive: shouldn't be reachable. Our UPDATE only
				// matches payment_intent_id IS NULL, so if the row is
				// awaiting_payment + reserved_until > NOW() and intent
				// is still NULL, the UPDATE should have matched. Surface
				// as 500 so a regression in this shape is not silent.
				c.JSON(http.StatusInternalServerError, gin.H{"error": "race fallback: unexpected null intent on eligible row"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"order_id":          orderID,
			"payment_intent_id": intentID,
			"client_secret":     "stub_secret_stage1_" + intentID,
			"amount_cents":      amountCents,
			"currency":          currency,
		})
		_ = logger
	}
}

// handleTestConfirm is the Stage 1 /test/payment/confirm/:id handler.
// Mirrors Stage 4's mock-confirm contract:
//
//   - ?outcome=succeeded → race-aware UPDATE to status='paid'
//     guarded by `status='awaiting_payment' AND payment_intent_id
//     IS NOT NULL AND reserved_until > NOW()`. If RowsAffected=0
//     the order isn't eligible (already terminal / no /pay /
//     expired); returns 409 with no state change. Same predicate
//     shape as Stage 4's MarkPaid SQL.
//   - ?outcome=failed → in-binary compensation transaction:
//     BEGIN; SELECT FOR UPDATE event_ticket_types; UPDATE
//     available_tickets += quantity; UPDATE orders SET
//     status='compensated'; COMMIT. Same shape the slice-5 expiry
//     sweeper will use; extracting to a helper when slice 5 lands.
//
// Critical guard (Codex round-2 P2.1): both branches require
// payment_intent_id IS NOT NULL — orders that skipped /pay are
// rejected. Without this, a client could call /confirm directly
// without first calling /pay; Stage 4 rejects that, so Stage 1 must
// too for apples-to-apples.
func handleTestConfirm(db *sql.DB, logger *mlog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		orderID, err := uuid.Parse(idStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}
		outcome := c.Query("outcome")
		if outcome != "succeeded" && outcome != "failed" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "outcome must be 'succeeded' or 'failed'"})
			return
		}

		if outcome == "succeeded" {
			res, err := db.ExecContext(c.Request.Context(), `
				UPDATE orders
				   SET status = 'paid'
				 WHERE id = $1
				   AND status = 'awaiting_payment'
				   AND payment_intent_id IS NOT NULL
				   AND reserved_until > NOW()`,
				orderID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "mark paid"})
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "order not eligible for paid (already terminal / no /pay / expired)"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "paid"})
			return
		}

		// outcome == "failed" — pre-check intent contract (Codex P2.1:
		// /confirm requires /pay-first; sweeper has no such contract
		// because abandoned-without-/pay is a valid abandon path),
		// then call the shared compensation helper.
		var hasIntent sql.NullString
		err = db.QueryRowContext(c.Request.Context(),
			"SELECT payment_intent_id FROM orders WHERE id = $1",
			orderID).Scan(&hasIntent)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read order"})
			return
		}
		if !hasIntent.Valid || hasIntent.String == "" {
			c.JSON(http.StatusConflict, gin.H{"error": "order has no payment_intent — must call /pay before confirm"})
			return
		}

		err = compensateAwaitingOrder(c.Request.Context(), db, orderID)
		switch {
		case err == nil:
			c.JSON(http.StatusOK, gin.H{"status": "compensated"})
		case errors.Is(err, ErrCompensateNotEligible):
			c.JSON(http.StatusConflict, gin.H{"error": "order not in awaiting_payment state"})
		default:
			logger.Error(c.Request.Context(), "stage1 /confirm-failed compensation",
				tag.OrderID(orderID), tag.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "compensation tx failed"})
		}
	}
}

// compensateAwaitingOrder runs the Stage 1 abandon-path compensation
// transaction:
//
//	BEGIN
//	  SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
//	  if status != 'awaiting_payment' → ErrCompensateNotEligible (rolled back)
//	  UPDATE event_ticket_types SET available_tickets += quantity, version += 1
//	  UPDATE orders SET status='compensated' WHERE status='awaiting_payment'
//	COMMIT
//
// Used by both call sites that abandon a Pattern A reservation in
// Stage 1:
//
//   - /test/payment/confirm/:id?outcome=failed (handler-side; the
//     handler does the additional payment_intent_id IS NOT NULL
//     check before calling here, since /confirm has the
//     "must-call-/pay-first" contract)
//   - the in-binary expiry sweeper goroutine (calls this directly;
//     no /pay-first requirement — abandon-without-/pay is a valid
//     TTL-expired path)
//
// The SELECT FOR UPDATE on `orders` serializes concurrent attempts
// to compensate the same order (e.g., /confirm-failed AND the
// sweeper firing in the same window). The second to acquire the
// lock will see status='compensated' and return ErrCompensateNotEligible.
func compensateAwaitingOrder(ctx context.Context, db *sql.DB, orderID uuid.UUID) error {
	tx, err := db.BeginTx(ctx, nil)
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
		return ErrCompensateNotEligible
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

// mapBookingError translates Stage 1 service errors into HTTP
// status + message. Mirrors the existing api/booking error mapping
// so the comparison harness sees identical 400/404/409/500 codes
// across all four stages — the apples-to-apples contract guarantee.
func mapBookingError(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrTicketTypeNotFound):
		return http.StatusNotFound, "ticket_type not found"
	case errors.Is(err, domain.ErrSoldOut):
		return http.StatusConflict, "sold out"
	case errors.Is(err, domain.ErrUserAlreadyBought):
		return http.StatusConflict, "user has already booked this event"
	case errors.Is(err, domain.ErrInvalidUserID),
		errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrInvalidOrderTicketTypeID):
		return http.StatusBadRequest, "invalid request parameters"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}
