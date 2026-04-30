package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrOrderNotFound = errors.New("order not found")

	// Invariant violations from NewOrder. Caller-actionable errors —
	// each maps to a malformed-input case the worker should DLQ.
	ErrInvalidOrderID  = errors.New("order id must not be the zero UUID")
	ErrInvalidUserID   = errors.New("order user_id must be positive")
	ErrInvalidEventID  = errors.New("order event_id must not be the zero UUID")
	ErrInvalidQuantity = errors.New("order quantity must be positive")

	// ErrInvalidTransition is returned by Order.MarkCharging /
	// MarkConfirmed / MarkFailed / MarkCompensated (and their
	// repository-side counterparts) when the source state doesn't
	// permit the requested transition. The legal transitions form a
	// directed acyclic graph (terminal node = no outgoing edges):
	//
	//   Pending  ──MarkCharging───→ Charging                              (A4)
	//   Pending  ──MarkConfirmed──→ Confirmed   (terminal, transitional)
	//   Pending  ──MarkFailed─────→ Failed                  (transitional)
	//   Charging ──MarkConfirmed──→ Confirmed   (terminal)                (A4)
	//   Charging ──MarkFailed─────→ Failed                                (A4)
	//   Failed   ──MarkCompensated→ Compensated (terminal)
	//
	// "Transitional" edges (Pending→Confirmed, Pending→Failed) exist
	// during the A4 migration window so in-flight Pending orders
	// queued before deploy can still resolve via the old code path.
	// They will be removed in a follow-up PR after the cutover trigger
	// fires (zero Pending→terminal transitions for 5+ minutes per
	// `order_status_history`); see PROJECT_SPEC §A4 for the trigger.
	//
	// Any other transition (e.g. Confirmed→Failed, Charging→Compensated)
	// is illegal and must surface as ErrInvalidTransition rather than
	// silently overwrite the row. Callers `errors.Is` against this
	// sentinel to differentiate "concurrent compensation race" (where
	// the same transition fires twice — idempotent: caller can
	// re-load + check Status) from "real bug" (where logic produced
	// a transition that should never happen).
	ErrInvalidTransition = errors.New("invalid order status transition")
)

// IsMalformedOrderInput reports whether err originated from a NewOrder
// invariant violation (zero UUID order_id/event_id, zero/negative
// user_id, or non-positive quantity).
//
// The booking pipeline uses this to route deterministic-failure
// messages straight to the DLQ instead of cycling the per-message
// retry budget. A UserID=0 message will return ErrInvalidUserID on
// every redelivery — retrying it 3× wastes ~600ms of backoff and 2
// pointless tx-open attempts before the inevitable DLQ write. The
// classifier short-circuits to first-attempt DLQ.
//
// Distinct from "transient" errors (DB conn lost, sold-out conflict,
// duplicate-purchase race) where redelivery has a chance of producing
// a different outcome — those still go through the retry budget.
//
// Lives in domain because the predicate is a property of the error
// itself; the queue/infrastructure layer that consumes it is just
// reading what the domain already exposes.
func IsMalformedOrderInput(err error) bool {
	return errors.Is(err, ErrInvalidOrderID) ||
		errors.Is(err, ErrInvalidUserID) ||
		errors.Is(err, ErrInvalidEventID) ||
		errors.Is(err, ErrInvalidQuantity)
}

type OrderStatus string

const (
	OrderStatusConfirmed   OrderStatus = "confirmed"
	OrderStatusPending     OrderStatus = "pending"
	OrderStatusCharging    OrderStatus = "charging" // A4 — intent log between Pending and Confirmed/Failed
	OrderStatusFailed      OrderStatus = "failed"
	OrderStatusCompensated OrderStatus = "compensated"
)

// IsValid reports whether s is one of the recognised order-status values.
// Used at API boundaries (e.g., GET /api/v1/history?status=...) to
// reject typo'd or out-of-vocabulary filter values up front instead of
// letting the SQL layer silently return zero rows. Defense-in-depth
// against future refactors that might let an unvalidated string flow
// into a code path beyond a parameterised SELECT.
//
// MUST stay in sync with the OrderStatus constant block above. Adding
// a new status without listing it here makes API filter requests for
// the new value silently no-op (StatusFilter() returns nil → repo gets
// no filter → all-orders response). Go offers no compile-time
// exhaustiveness check for typed strings; a future `exhaustive` linter
// addition (zero code change since `default: return false` already
// satisfies `default-signifies-exhaustive: true`) would close the
// gap automatically.
func (s OrderStatus) IsValid() bool {
	switch s {
	case OrderStatusConfirmed, OrderStatusPending, OrderStatusCharging,
		OrderStatusFailed, OrderStatusCompensated:
		return true
	}
	return false
}

// StuckCharging is the row shape returned by
// `OrderRepository.FindStuckCharging` — the reconciler's input
// vocabulary. Lives in domain because the reconciler is an
// application-layer service that depends on this domain port and the
// type is a pure value (no JSON / DB tags). Carrying just (id, age)
// avoids the cost of materializing full Orders that the reconciler
// won't read most fields of.
//
// `Age` is the duration the row has been in Charging at query time,
// derived from `NOW() - updated_at` in SQL. Used by the reconciler
// to apply the max-age give-up policy.
type StuckCharging struct {
	ID  uuid.UUID
	Age time.Duration
}

// StuckFailed is the row shape returned by
// `OrderRepository.FindStuckFailed` — the saga watchdog's input
// vocabulary (A5). Structurally identical to StuckCharging but kept
// as a distinct type so the watchdog and reconciler can't accidentally
// pass each other's results: a row that's stuck-Charging requires a
// gateway probe; a row that's stuck-Failed requires a compensator
// re-drive. Conflating them via a shared type would invite the wrong
// resolution path on a future refactor.
//
// The query is the same shape as FindStuckCharging — `WHERE status =
// 'failed' AND updated_at < NOW() - threshold ORDER BY updated_at ASC
// LIMIT batch` — so both reuse the partial index from migration 000011
// (widened from charging+pending to charging+pending+failed).
type StuckFailed struct {
	ID  uuid.UUID
	Age time.Duration
}

// Order is the domain aggregate. All fields are unexported; reads
// happen through accessor methods (Wild Workouts pattern, no Get
// prefix), writes happen through the NewOrder factory or the
// immutable WithStatus transition. Construction outside this package
// is impossible — callers cannot bypass the factory's invariants.
//
// Field types:
//   - id, eventID: uuid.UUID. Both are factory-generated (NewV7) or
//     received from boundaries; never DB-assigned. UUIDv7 is
//     time-prefixed so B-tree indexes still cluster (see
//     memory/uuid_v7_research.md for benchmark).
//   - userID: int. STAYS int because users are an external concept
//     (this service does not own the users table).
//   - createdAt: factory-assigned, NOT DB-assigned. The UUIDv7 already
//     encodes ms-precision creation time; we keep CreatedAt as a
//     full time.Time for human-friendly display via DTOs and for
//     business logic that compares times directly.
type Order struct {
	id        uuid.UUID
	eventID   uuid.UUID
	userID    int
	quantity  int
	status    OrderStatus
	createdAt time.Time
}

// NewOrder constructs a fresh pending order. The caller supplies the
// id (typically `uuid.NewV7()` at the API boundary so the response can
// echo it back to the client and the worker uses the same id verbatim
// across PEL retries — a worker-side `uuid.NewV7()` would generate a
// fresh id per redelivery, defeating idempotency-by-id at the DB
// layer).
//
// Validates invariants at the domain boundary, then assigns a
// time.Now() createdAt. The returned Order is fully complete — no
// repository "fills in" anything.
func NewOrder(id uuid.UUID, userID int, eventID uuid.UUID, quantity int) (Order, error) {
	if id == uuid.Nil {
		return Order{}, ErrInvalidOrderID
	}
	if userID <= 0 {
		return Order{}, ErrInvalidUserID
	}
	if eventID == uuid.Nil {
		return Order{}, ErrInvalidEventID
	}
	if quantity <= 0 {
		return Order{}, ErrInvalidQuantity
	}
	return Order{
		id:        id,
		userID:    userID,
		eventID:   eventID,
		quantity:  quantity,
		status:    OrderStatusPending,
		createdAt: time.Now(),
	}, nil
}

// ReconstructOrder rehydrates an Order from a persisted row. Skips
// the invariant validation in NewOrder because the row was already
// validated at insert time. Use ONLY from repository row-scanning
// code, never to "create" a new order. Future refactor: move into
// internal/infrastructure/persistence/postgres so the visibility
// matches the contract; for now the comment-only contract holds
// because all postgres scan code is the only caller.
func ReconstructOrder(id uuid.UUID, userID int, eventID uuid.UUID, quantity int, status OrderStatus, createdAt time.Time) Order {
	return Order{
		id:        id,
		userID:    userID,
		eventID:   eventID,
		quantity:  quantity,
		status:    status,
		createdAt: createdAt,
	}
}

// MarkCharging transitions Pending → Charging and returns the new
// Order value. Immutable: the receiver is unmodified. The
// Charging state is the "intent log" recorded by the payment service
// before calling the gateway, so a separate recon process can
// resolve stuck-mid-flight orders by querying the gateway. See
// PROJECT_SPEC §A4 for the design.
func (o Order) MarkCharging() (Order, error) {
	if o.status != OrderStatusPending {
		return Order{}, fmt.Errorf("cannot start charging from %s: %w", o.status, ErrInvalidTransition)
	}
	o.status = OrderStatusCharging
	return o, nil
}

// MarkConfirmed transitions Pending → Confirmed OR Charging →
// Confirmed and returns the new Order value. Immutable: the receiver
// is unmodified.
//
// Pending→Confirmed is the TRANSITIONAL edge during the A4 migration
// window. The new code path always goes Pending→Charging→Confirmed
// (the payment service writes Charging before calling the gateway).
// The Pending→Confirmed edge stays available so in-flight Pending
// orders queued before the deploy can still resolve via the old
// code path. A follow-up PR will tighten this to Charging-only after
// the cutover trigger fires.
//
// Returns ErrInvalidTransition when the receiver is in any other
// state (Confirmed/Failed/Compensated). Caller should `errors.Is`
// against ErrInvalidTransition AND check the current status — if the
// row is already in the target state, the transition is idempotent
// success (someone else got here first).
func (o Order) MarkConfirmed() (Order, error) {
	if o.status != OrderStatusPending && o.status != OrderStatusCharging {
		return Order{}, fmt.Errorf("cannot confirm from %s: %w", o.status, ErrInvalidTransition)
	}
	o.status = OrderStatusConfirmed
	return o, nil
}

// MarkFailed transitions Pending → Failed OR Charging → Failed (saga
// path entry). Same transitional-edge rationale as MarkConfirmed.
func (o Order) MarkFailed() (Order, error) {
	if o.status != OrderStatusPending && o.status != OrderStatusCharging {
		return Order{}, fmt.Errorf("cannot fail from %s: %w", o.status, ErrInvalidTransition)
	}
	o.status = OrderStatusFailed
	return o, nil
}

// MarkCompensated transitions Failed → Compensated (saga completion).
// Returns ErrInvalidTransition for any other source state.
func (o Order) MarkCompensated() (Order, error) {
	if o.status != OrderStatusFailed {
		return Order{}, fmt.Errorf("cannot compensate from %s: %w", o.status, ErrInvalidTransition)
	}
	o.status = OrderStatusCompensated
	return o, nil
}

// Accessors — read-only views on the unexported fields. Wild Workouts
// pattern (no "Get" prefix), aligned with Go stdlib (time.Time.Hour()
// etc.).
func (o Order) ID() uuid.UUID        { return o.id }
func (o Order) EventID() uuid.UUID   { return o.eventID }
func (o Order) UserID() int          { return o.userID }
func (o Order) Quantity() int        { return o.quantity }
func (o Order) Status() OrderStatus  { return o.status }
func (o Order) CreatedAt() time.Time { return o.createdAt }

//go:generate mockgen -source=order.go -destination=../mocks/order_repository_mock.go -package=mocks
type OrderRepository interface {
	// Create persists the order and returns it back unchanged. The
	// caller's input already has its UUID + CreatedAt set by the
	// factory, so the repo no longer "fills in" anything — Create's
	// signature still returns (Order, error) for API consistency
	// with prior code that needed the populated value.
	Create(ctx context.Context, order Order) (Order, error)

	// ListOrders returns a page of orders by value. Empty result is
	// a nil slice (not an error).
	ListOrders(ctx context.Context, limit, offset int, status *OrderStatus) ([]Order, int, error)

	// GetByID returns the order by id. ErrOrderNotFound when no row
	// matches; any other error is wrapped with the query context.
	GetByID(ctx context.Context, id uuid.UUID) (Order, error)

	// FindStuckCharging returns orders that have been in the Charging
	// state for at least `minAge`, up to `limit` rows. Used by the
	// reconciler subcommand to identify stuck-mid-flight payments
	// (worker crashed, gateway timeout, kafka rebalance ate the
	// in-flight call).
	//
	// Returns (id, age) pairs rather than full Orders because the
	// reconciler doesn't need the rest of the entity — it just calls
	// gateway.GetStatus(id) and resolves the row by id. Pulling the
	// full row would force a JOIN with order_status_history or a
	// follow-up GetByID per stuck order — neither pays off.
	FindStuckCharging(ctx context.Context, minAge time.Duration, limit int) ([]StuckCharging, error)

	// FindStuckFailed returns orders that have been in the Failed state
	// for at least `minAge`, up to `limit` rows. Used by the saga
	// watchdog subcommand (A5) to identify orders whose compensation
	// path stalled — typically because the saga consumer crashed
	// after MarkFailed but before MarkCompensated, or because the DLQ
	// route swallowed an event.
	//
	// Same row shape (id, age) as FindStuckCharging for the same
	// reason — the watchdog re-drives the existing compensator,
	// which only needs the id to fetch + idempotency-check the order.
	FindStuckFailed(ctx context.Context, minAge time.Duration, limit int) ([]StuckFailed, error)

	// MarkCharging / MarkConfirmed / MarkFailed / MarkCompensated are
	// the typed state-transition methods that replace the previous
	// `UpdateStatus(id, status)` escape hatch. Each enforces the
	// source-state predicate atomically in SQL (UPDATE ... WHERE
	// status = '<expected>') so a concurrent compensation race
	// can't silently overwrite the wrong row. Returns:
	//   - nil                 transition succeeded
	//   - ErrOrderNotFound    no row with that id exists
	//   - ErrInvalidTransition row exists but its current status
	//                         doesn't permit this transition
	//                         (e.g. MarkConfirmed on a Failed order)
	//   - other errors        wrapped DB failure
	//
	// MarkConfirmed and MarkFailed accept source ∈ {Pending, Charging}
	// during the A4 migration window — the new code path always goes
	// through Charging, but legacy in-flight Pending messages still
	// resolve via the old path. A follow-up PR tightens to
	// Charging-only after the cutover trigger fires.
	//
	// The legal transition graph is documented on ErrInvalidTransition.
	MarkCharging(ctx context.Context, id uuid.UUID) error
	MarkConfirmed(ctx context.Context, id uuid.UUID) error
	MarkFailed(ctx context.Context, id uuid.UUID) error
	MarkCompensated(ctx context.Context, id uuid.UUID) error
}
