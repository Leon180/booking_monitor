package domain

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTicketTypeNotFound = errors.New("ticket type not found")

	// ErrTicketTypeNameTaken is returned by TicketTypeRepository.Create
	// when a ticket type with the same `name` already exists for the
	// given `event_id`. Surfaced from postgres via the
	// `uq_ticket_type_name_per_event` unique-constraint violation
	// (PG error code 23505). Mirrors `domain.ErrUserAlreadyBought` —
	// callers branch on `errors.Is(err, ErrTicketTypeNameTaken)` for
	// 409 Conflict mapping at the API boundary, instead of string-
	// matching the raw postgres error.
	ErrTicketTypeNameTaken = errors.New("ticket type name already exists for this event")

	// Invariant violations from NewTicketType.
	ErrInvalidTicketTypeID         = errors.New("ticket_type id must not be the zero UUID")
	ErrInvalidTicketTypeEventID    = errors.New("ticket_type event_id must not be the zero UUID")
	ErrInvalidTicketTypeName       = errors.New("ticket_type name must not be empty")
	ErrInvalidTicketTypePrice      = errors.New("ticket_type price_cents must be positive")
	ErrInvalidTicketTypeCurrency   = errors.New("ticket_type currency must be a 3-letter ISO 4217 code")
	ErrInvalidTicketTypeTotal      = errors.New("ticket_type total_tickets must be positive")
	ErrInvalidTicketTypeAvailable  = errors.New("ticket_type available_tickets must be in [0, total_tickets]")
	ErrInvalidTicketTypeSaleWindow = errors.New("ticket_type sale_ends_at must be after sale_starts_at")
	ErrInvalidTicketTypePerUser    = errors.New("ticket_type per_user_limit must be positive and ≤ 2^31-1")
)

// TicketType is the KKTIX-aligned 票種 aggregate (D4.1). Pricing,
// inventory cap, sale window, per-user purchase limit, and an optional
// physical-area label all live on this entity. An Event is a billboard
// (date + venue + name); the TicketType is what the customer actually
// buys.
//
// Why a new aggregate vs. fields on Event: a single Event can have
// multiple TicketTypes (VIP 早鳥票, 一般票, 學生票 — KKTIX vocabulary).
// D4.1 ships with one default TicketType per Event, but the aggregate
// shape is correct for the eventual D8 multi-票種 expansion. Having the
// aggregate already in place means D8 lands as code-only changes (no
// schema migration, no domain refactor).
//
// Field semantics:
//   - id, eventID: factory-assigned UUIDv7. eventID is the FK; the
//     persistence layer enforces the relation via INSERT-time checks
//     (no DB-level FK — same rationale as orders.event_id, see
//     migration 000014 docstring).
//   - name: human-readable label ("VIP 早鳥票"). Distinct from
//     areaLabel below.
//   - priceCents + currency: the unit price. Snapshotted onto orders
//     at book time (see Order.amountCents).
//   - totalTickets: cap (immutable post-creation in D4.1; D8 may
//     allow operator edits with a separate "rebalance inventory"
//     workflow that adjusts both Postgres and Redis atomically).
//   - availableTickets: live counter. Decrements via DecrementTicket
//     (the booking hot path) and increments via IncrementTicket (saga
//     compensation). Same shape as Event.availableTickets.
//   - saleStartsAt / saleEndsAt: optional sale window. Nil = always
//     on sale. Schema-only in D4.1 — BookTicket does NOT enforce
//     these yet; D8 business-rule work wires the check in.
//   - perUserLimit: optional per-customer purchase cap. Nil = unlimited.
//     Same D4.1-vs-D8 split as the sale window.
//   - areaLabel: optional 「分區」 ("VIP A 區"). Distinct from name —
//     name is the ticket-type label ("VIP 早鳥票"), areaLabel is the
//     physical area annotation. D8's seat layer will reference this
//     for grouping but D4.1 just persists it.
//   - version: optimistic-concurrency counter (mirrors Event.version).
type TicketType struct {
	id               uuid.UUID
	eventID          uuid.UUID
	name             string
	priceCents       int64
	currency         string
	totalTickets     int
	availableTickets int
	saleStartsAt     *time.Time
	saleEndsAt       *time.Time
	perUserLimit     *int
	areaLabel        string
	version          int
}

// NewTicketType constructs a fresh ticket type with the canonical
// "available = total at creation" invariant.
//
// `id` is caller-supplied (typically `uuid.NewV7()` at the admin
// service that owns ticket-type creation), per the project's
// caller-generated-ID convention (coding-style.md §8): aggregate
// identity belongs at the call site that first observes the entity,
// not the factory. This stays symmetric with `NewOrder` /
// `NewReservation`.
//
// saleStartsAt / saleEndsAt / perUserLimit / areaLabel are optional —
// pass nil / "" to omit. The contract for the optional pair:
//   - both nil:                      always on sale
//   - both non-nil:                  end must be strictly after start
//   - starts only (ends == nil):     sale starts at T, no expiry
//   - ends only (starts == nil):     REJECTED — an end without a start
//                                    is ambiguous; D8 enforcement code
//                                    has no canonical "started at" to
//                                    interpret it against.
//
// Currency is lowercased before shape validation (Stripe / KKTIX
// require lowercase ISO 4217); the persisted value is the normalised
// form.
func NewTicketType(
	id, eventID uuid.UUID,
	name string,
	priceCents int64,
	currency string,
	totalTickets int,
	saleStartsAt, saleEndsAt *time.Time,
	perUserLimit *int,
	areaLabel string,
) (TicketType, error) {
	if id == uuid.Nil {
		return TicketType{}, ErrInvalidTicketTypeID
	}
	if eventID == uuid.Nil {
		return TicketType{}, ErrInvalidTicketTypeEventID
	}
	if strings.TrimSpace(name) == "" {
		return TicketType{}, ErrInvalidTicketTypeName
	}
	if priceCents <= 0 {
		return TicketType{}, ErrInvalidTicketTypePrice
	}
	currency = NormalizeCurrency(currency)
	if !isValidCurrencyCode(currency) {
		return TicketType{}, ErrInvalidTicketTypeCurrency
	}
	if totalTickets <= 0 {
		return TicketType{}, ErrInvalidTicketTypeTotal
	}
	// Sale window guards. Reject the half-nil endsAt-only case (see
	// the contract in the doc comment); accept both-nil and starts-only.
	// Both-non-nil requires endsAt strictly after startsAt.
	if saleStartsAt == nil && saleEndsAt != nil {
		return TicketType{}, ErrInvalidTicketTypeSaleWindow
	}
	if saleStartsAt != nil && saleEndsAt != nil && !saleEndsAt.After(*saleStartsAt) {
		return TicketType{}, ErrInvalidTicketTypeSaleWindow
	}
	// perUserLimit constraints: positive AND fits in int32 (the DB
	// column is `INT`; the persistence layer narrows to int32 on write,
	// so a value > MaxInt32 would silently truncate without this guard).
	// Realistic caps are typically ≤ 100, so this is paranoia — the
	// cost is one branch per ticket-type creation (not in the booking
	// hot path).
	if perUserLimit != nil && (*perUserLimit <= 0 || *perUserLimit > math.MaxInt32) {
		return TicketType{}, ErrInvalidTicketTypePerUser
	}
	return TicketType{
		id:               id,
		eventID:          eventID,
		name:             name,
		priceCents:       priceCents,
		currency:         currency,
		totalTickets:     totalTickets,
		availableTickets: totalTickets,
		saleStartsAt:     saleStartsAt,
		saleEndsAt:       saleEndsAt,
		perUserLimit:     perUserLimit,
		areaLabel:        areaLabel,
		version:          0,
	}, nil
}

// ReconstructTicketType rehydrates from a persisted row. Skips
// invariant validation; postgres scan code is the only intended caller.
//
// Currency is defensively re-normalised here even though the factory
// already lowercases on every write path. Direct-SQL inserts (admin
// scripts, manual recovery, integration test fixtures) can bypass the
// factory and store mixed-case values; without this guard a
// `'USD'` row would round-trip un-lowercased into the API response,
// while NewReservation would still snapshot the lowercased form onto
// the order — producing a case-mismatch between the ticket_type
// response and the eventual /pay PaymentIntent. Cheap to do here
// (single strings.ToLower per scan); load-bearing for the
// ticket_type ↔ order currency consistency contract.
func ReconstructTicketType(
	id, eventID uuid.UUID,
	name string,
	priceCents int64,
	currency string,
	totalTickets, availableTickets int,
	saleStartsAt, saleEndsAt *time.Time,
	perUserLimit *int,
	areaLabel string,
	version int,
) TicketType {
	return TicketType{
		id:               id,
		eventID:          eventID,
		name:             name,
		priceCents:       priceCents,
		currency:         NormalizeCurrency(currency),
		totalTickets:     totalTickets,
		availableTickets: availableTickets,
		saleStartsAt:     saleStartsAt,
		saleEndsAt:       saleEndsAt,
		perUserLimit:     perUserLimit,
		areaLabel:        areaLabel,
		version:          version,
	}
}

// Accessors — Wild Workouts pattern (no Get prefix).
func (t TicketType) ID() uuid.UUID            { return t.id }
func (t TicketType) EventID() uuid.UUID       { return t.eventID }
func (t TicketType) Name() string             { return t.name }
func (t TicketType) PriceCents() int64        { return t.priceCents }
func (t TicketType) Currency() string         { return t.currency }
func (t TicketType) TotalTickets() int        { return t.totalTickets }
func (t TicketType) AvailableTickets() int    { return t.availableTickets }
func (t TicketType) SaleStartsAt() *time.Time { return t.saleStartsAt }
func (t TicketType) SaleEndsAt() *time.Time   { return t.saleEndsAt }

// PerUserLimit returns the per-customer purchase cap, or nil for "no
// limit." Callers MUST treat nil and a non-nil zero/negative pointer
// as semantically distinct: nil means "operator did not set a limit"
// (purchase any quantity), whereas a non-nil zero/negative value
// would be a data-integrity bug. NewTicketType rejects the latter via
// `ErrInvalidTicketTypePerUser`; ReconstructTicketType bypasses that
// check by design (persistence rows are trusted), so a malformed DB
// row could theoretically surface here. Production code should branch
// on `if limit := t.PerUserLimit(); limit != nil && quantity > *limit`
// — never on `*limit > 0` alone.
func (t TicketType) PerUserLimit() *int       { return t.perUserLimit }
func (t TicketType) AreaLabel() string        { return t.areaLabel }
func (t TicketType) Version() int             { return t.version }

//go:generate mockgen -source=ticket_type.go -destination=../mocks/ticket_type_repository_mock.go -package=mocks
type TicketTypeRepository interface {
	// Create persists the ticket type. Factory has assigned id +
	// version, so the input is value-in. Returns the persisted row
	// (currently unchanged from input, leaves room for future
	// server-side defaults — symmetric with EventRepository.Create).
	Create(ctx context.Context, t TicketType) (TicketType, error)

	// GetByID is a plain read with no row lock.
	GetByID(ctx context.Context, id uuid.UUID) (TicketType, error)

	// ListByEventID returns all ticket types for a given event,
	// ordered by created_at ASC. D4.1 typically returns one row;
	// D8 expansion will return many.
	ListByEventID(ctx context.Context, eventID uuid.UUID) ([]TicketType, error)

	// Delete removes a ticket type by id. Used as the compensation leg
	// of `event.Service.CreateEvent` when the event + ticket_type
	// transaction commits but the subsequent Redis SetInventory fails
	// — the orphaned ticket_type row must be cleaned up alongside the
	// event row (no FK CASCADE in the schema; D1 chose not to add a
	// cross-aggregate FK to avoid OutboxRelay locking surprises).
	//
	// Returns:
	//   - nil               row deleted (or already absent — idempotent)
	//   - other errors      wrapped DB failure
	//
	// Idempotent: deleting a non-existent id is a no-op (rowsAffected=0
	// is not surfaced as an error). The compensation path may run after
	// a partial earlier compensation already deleted the row.
	Delete(ctx context.Context, id uuid.UUID) error
}
