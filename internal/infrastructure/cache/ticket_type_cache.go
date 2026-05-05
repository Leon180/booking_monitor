package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// cacheLabelTicketType is the `cache` label value reported on the
// hit/miss counters. Lifted to a const so the two Inc sites can't
// drift, and kept HERE (decorator file) rather than in the underlying
// repo so the storage layer stays observability-unaware.
const cacheLabelTicketType = "ticket_type"

// ticketTypeCacheKeyPrefix is the canonical Redis key namespace for
// the ticket_type read-through cache. Distinct from the inventory
// (`event:{id}:qty`) and idempotency (`idempotency:{key}`) namespaces
// so a Redis FLUSHALL diagnosis can target one cache without
// accidentally evicting the others.
const ticketTypeCacheKeyPrefix = "ticket_type:"

func ticketTypeCacheKey(id uuid.UUID) string {
	return ticketTypeCacheKeyPrefix + id.String()
}

// ticketTypeCacheDecorator wraps a domain.TicketTypeRepository with a
// Redis read-through cache on the `GetByID` hot path.
//
// Why GetByID specifically. `BookingService.BookTicket` calls
// `ticketTypeRepo.GetByID` on every booking attempt to resolve the
// price snapshot. D4.1 introduced this round-trip; the post-D4.1
// benchmark showed ~−40% RPS / +95% p95 on the sold-out fast path
// because every 409 still pays the lookup cost. The fields BookTicket
// actually reads (eventID, priceCents, currency, totalTickets) are
// IMMUTABLE post-creation, so a TTL cache is correctness-safe — no
// invalidation on `Decrement`/`Increment` needed (those mutate
// `available_tickets` + `version`, neither of which BookTicket reads).
//
// What's deliberately NOT cached:
//
//   - `ListByEventID` — admin-side path; cardinality is low but the
//     return-shape (slice) makes invalidation harder, and there's no
//     hot-path latency justifying the complexity. Pass-through.
//   - `SumAvailableByEventID` — RehydrateInventory + InventoryDriftDetector
//     both call this; both want a FRESH read because they're checking
//     for drift. Caching would defeat the point.
//   - `Decrement`/`Increment` — these are tx-bound writes; caching
//     would race against the UoW. Pass-through.
//   - `Create` / `Delete` — write paths. Pass-through (Delete also
//     proactively invalidates the cache entry — see Delete doc below).
//   - Negative cache (NotFound). A brand-new ticket_type Created and
//     immediately GetByID'd would be invisible for TTL minutes if we
//     cached the not-found result. The cost (one extra PG round-trip
//     per missing-id booking attempt) is bounded by the API's 4xx
//     rate which is tiny for legitimate traffic.
//
// Wired via fx.Decorate in `cache.Module` (`redis.go`) so consumers
// ask for `domain.TicketTypeRepository` and transparently get the
// cached one — no opt-in burden at call sites.
//
// Layered ABOVE the existing tracing decorator (`fx.Decorate` stacks
// outermost-last). On a cache hit, no Postgres span fires — that's
// the desired observability shape: a hit IS a no-DB-call event, and
// the per-cache hit/miss Prometheus counter is the operator signal.
// A cache miss falls through to the tracing decorator → postgres span
// fires as normal.
type ticketTypeCacheDecorator struct {
	inner  domain.TicketTypeRepository
	client *redis.Client
	ttl    time.Duration
	logger *mlog.Logger
}

// NewTicketTypeCacheDecorator returns the decorator. Same input/output
// interface so fx.Decorate transparently swaps it in over the postgres
// + tracing stack.
//
// `ttl` MUST be > 0 — a zero TTL would create entries that never
// expire, accumulating in Redis indefinitely. Caller (typically fx
// from `cfg.Redis.TicketTypeTTL`) enforces this invariant; we
// defensively log + bypass cache on a non-positive TTL rather than
// panic at startup, matching the codebase's "fail-soft on operational
// misconfiguration, fail-loud on bugs" stance.
func NewTicketTypeCacheDecorator(
	inner domain.TicketTypeRepository,
	client *redis.Client,
	ttl time.Duration,
	logger *mlog.Logger,
) domain.TicketTypeRepository {
	if ttl <= 0 {
		logger.Warn(context.Background(),
			"ticket_type cache TTL is non-positive; decorator will pass-through to postgres on every call",
			mlog.String("ttl", ttl.String()))
	}
	return &ticketTypeCacheDecorator{
		inner:  inner,
		client: client,
		ttl:    ttl,
		logger: logger.With(mlog.String("component", "ticket_type_cache")),
	}
}

// ticketTypeCacheEntry is the JSON wire shape persisted to Redis.
// Distinct from `domain.TicketType` (unexported fields, no JSON tags
// per coding-style.md §7) and from `postgres/ticketTypeRow` (sql.Null*
// types not friendly to JSON). Owning the cache shape locally keeps
// the cache layer dependency-free of the storage package.
//
// Field selection — IMMUTABLE-only.
// `available_tickets` is DELIBERATELY OMITTED. The decorator's contract
// is "cache only fields BookTicket reads, all of which are immutable
// post-creation". Caching a mutable counter would require invalidating
// on Decrement / Increment — that's an entire layer of complexity for
// a field no current consumer reads from a cached aggregate. To
// prevent a future consumer (e.g. a D8 admin endpoint that builds a
// ticket-type detail page from `GetByID`) from accidentally serving
// a stale `available_tickets`, `toDomain` reconstructs with `0` as a
// CONSERVATIVE-SAFE SENTINEL: a caller mistakenly branching on
// `tt.AvailableTickets() > 0` reads "sold out" rather than "definitely
// has stock". Fresh values come from `SumAvailableByEventID`
// (uncached) or a direct Postgres query.
//
// Versioning. `_v` is bumped when the wire shape changes
// incompatibly (added field is fine; renamed/typed-changed is not).
// Old entries fail to unmarshal → caller treats as miss → re-fills
// with the new shape. No explicit migration needed.
type ticketTypeCacheEntry struct {
	V            int        `json:"_v"`
	ID           uuid.UUID  `json:"id"`
	EventID      uuid.UUID  `json:"event_id"`
	Name         string     `json:"name"`
	PriceCents   int64      `json:"price_cents"`
	Currency     string     `json:"currency"`
	TotalTickets int        `json:"total_tickets"`
	// AvailableTickets is intentionally omitted — see struct doc.
	SaleStartsAt *time.Time `json:"sale_starts_at,omitempty"`
	SaleEndsAt   *time.Time `json:"sale_ends_at,omitempty"`
	PerUserLimit *int       `json:"per_user_limit,omitempty"`
	AreaLabel    string     `json:"area_label,omitempty"`
	Version      int        `json:"version"`
}

const ticketTypeCacheEntryVersion = 1

func ticketTypeCacheEntryFromDomain(t domain.TicketType) ticketTypeCacheEntry {
	return ticketTypeCacheEntry{
		V:            ticketTypeCacheEntryVersion,
		ID:           t.ID(),
		EventID:      t.EventID(),
		Name:         t.Name(),
		PriceCents:   t.PriceCents(),
		Currency:     t.Currency(),
		TotalTickets: t.TotalTickets(),
		SaleStartsAt: t.SaleStartsAt(),
		SaleEndsAt:   t.SaleEndsAt(),
		PerUserLimit: t.PerUserLimit(),
		AreaLabel:    t.AreaLabel(),
		Version:      t.Version(),
	}
}

// toDomain rehydrates the cached entry. AvailableTickets is set to 0
// — the conservative-safe sentinel documented on the struct. Callers
// that NEED a fresh availability count must NOT use this aggregate's
// `AvailableTickets()`; they should call `SumAvailableByEventID` or
// query Postgres directly.
func (e ticketTypeCacheEntry) toDomain() domain.TicketType {
	return domain.ReconstructTicketType(
		e.ID, e.EventID, e.Name,
		e.PriceCents, e.Currency,
		e.TotalTickets, 0, // AvailableTickets sentinel — see struct doc.
		e.SaleStartsAt, e.SaleEndsAt,
		e.PerUserLimit, e.AreaLabel,
		e.Version,
	)
}

// GetByID is the cache hot path. Try Redis → on miss, call inner →
// write-through with TTL → return.
//
// Failure modes (fail-soft on cache layer):
//
//   - Redis GET returns redis.Nil: organic miss → bump cache_misses,
//     fall through to postgres, write-through.
//   - Cached payload unmarshal error / version mismatch: corrupt or
//     stale-schema entry → bump cache_misses (operationally a miss
//     from the caller's perspective), fall through, write-through
//     overwrites with the current shape.
//   - Redis GET fails for a non-Nil reason (network, AUTH, OOM): bump
//     cache_errors_total{op="get"} and DO NOT bump cache_misses. The
//     distinct counter is the load-bearing signal during a Redis
//     outage — masking it as a miss-rate spike would hide the actual
//     cause from dashboards.
//   - Redis SET error after a successful inner call: bump
//     cache_errors_total{op="set"}, log Warn, return the postgres
//     result. Next caller re-queries postgres; no correctness impact.
//   - Marshal error: bump cache_errors_total{op="marshal"}, log Warn,
//     return without caching. Theoretical for a fixed-shape DTO.
//
// The contract is: the cache is a perf optimisation, NEVER a
// correctness requirement. Callers see the same `(domain.TicketType,
// error)` shape as the underlying repo — including
// `ErrTicketTypeNotFound` for missing ids.
func (d *ticketTypeCacheDecorator) GetByID(ctx context.Context, id uuid.UUID) (domain.TicketType, error) {
	if d.ttl <= 0 {
		return d.inner.GetByID(ctx, id)
	}

	key := ticketTypeCacheKey(id)
	raw, err := d.client.Get(ctx, key).Result()
	switch {
	case err == nil:
		var entry ticketTypeCacheEntry
		if unmarshalErr := json.Unmarshal([]byte(raw), &entry); unmarshalErr == nil && entry.V == ticketTypeCacheEntryVersion {
			observability.CacheHitsTotal.WithLabelValues(cacheLabelTicketType).Inc()
			return entry.toDomain(), nil
		}
		// Either malformed JSON or unsupported version. Log + treat
		// as miss (operationally a miss from the caller's perspective);
		// the write-through below will overwrite with a fresh entry in
		// the current shape.
		d.logger.Warn(ctx, "ticket_type cache entry unparseable; treating as miss",
			tag.TicketTypeID(id))
		observability.CacheMissesTotal.WithLabelValues(cacheLabelTicketType).Inc()
	case errors.Is(err, redis.Nil):
		observability.CacheMissesTotal.WithLabelValues(cacheLabelTicketType).Inc()
	default:
		// Real Redis failure (network, AUTH, OOM). Bump the dedicated
		// error counter, NOT cache_misses — the distinct signal is what
		// distinguishes "Redis is down" from "cache is cold" on the
		// operator dashboard. The booking still works because we fall
		// through to postgres.
		observability.CacheErrorsTotal.WithLabelValues(cacheLabelTicketType, "get").Inc()
		d.logger.Warn(ctx, "ticket_type cache GET failed; falling through to postgres",
			tag.TicketTypeID(id), tag.Error(err))
	}

	tt, getErr := d.inner.GetByID(ctx, id)
	if getErr != nil {
		// NotFound + other inner errors are NOT cached. See class
		// doc-comment "What's deliberately NOT cached" for the
		// not-found rationale; other errors (DB outage) shouldn't be
		// memoised either — the next caller should retry against the
		// inner repo.
		return tt, getErr
	}

	entry := ticketTypeCacheEntryFromDomain(tt)
	payload, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		// Theoretical for the fixed-shape DTO, but a future field
		// addition could trip it (e.g. an unmarshalable type). Log +
		// return the result without caching — the next call repays the
		// PG round-trip but correctness holds. Logged at Warn (parity
		// with the other fail-soft cache paths); operationally
		// equivalent to a SET failure — both leave the cache cold,
		// both are self-healing on the next miss. The dedicated
		// `cache_errors_total{op="marshal"}` counter is the alertable
		// signal.
		observability.CacheErrorsTotal.WithLabelValues(cacheLabelTicketType, "marshal").Inc()
		d.logger.Warn(ctx, "ticket_type cache marshal failed; cache write skipped",
			tag.TicketTypeID(id), tag.Error(marshalErr))
		return tt, nil
	}
	// Detached ctx for the SET — the cache write is a fire-and-forget
	// side effect of the SUCCESSFUL inner GetByID, NOT something the
	// caller is waiting on. Inheriting the request ctx here would mean
	// any near-deadline request that just finished its PG read would
	// fail the cache SET on context.Canceled / DeadlineExceeded, even
	// though the booking itself succeeded. That false-positive would
	// pollute `cache_errors_total{op="set"}` under load, masking real
	// Redis incidents. Budget mirrors how the worker's handleFailure
	// detaches its compensation ctx (see redis_queue.go::handleFailure).
	// 1s is generous for a Redis SET — actual writes are sub-ms.
	bgCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if setErr := d.client.Set(bgCtx, key, payload, d.ttl).Err(); setErr != nil {
		observability.CacheErrorsTotal.WithLabelValues(cacheLabelTicketType, "set").Inc()
		d.logger.Warn(ctx, "ticket_type cache SET failed; entry not persisted",
			tag.TicketTypeID(id), tag.Error(setErr))
		// Fall through — the inner result is still valid.
	}
	return tt, nil
}

// Create passes through. The cache fills lazily on the first GetByID;
// pre-warming on Create would race with the surrounding UoW (Create
// runs inside a tx; another goroutine reading the cache pre-commit
// would see a row that hasn't actually been persisted yet if the tx
// rolls back).
func (d *ticketTypeCacheDecorator) Create(ctx context.Context, t domain.TicketType) (domain.TicketType, error) {
	return d.inner.Create(ctx, t)
}

// ListByEventID passes through. See class doc for why this isn't cached.
func (d *ticketTypeCacheDecorator) ListByEventID(ctx context.Context, eventID uuid.UUID) ([]domain.TicketType, error) {
	return d.inner.ListByEventID(ctx, eventID)
}

// Delete passes through to the inner repo, then proactively
// invalidates the cache entry. Strictly speaking not required for
// correctness in D4.1: Delete is only called from
// `event.Service.CreateEvent`'s rollback path when SetInventory
// fails, and in that window no booking can target this ticket_type
// (the event row is being rolled back too, and the booking handler
// looks up via ticket_type_id which would still resolve to a row
// briefly before Delete fires). But the small race exists in theory,
// and an explicit Del is cheap insurance for D8 when admins gain
// independent ticket-type lifecycle.
//
// Cache invalidation failure is logged but NOT surfaced — the inner
// Delete already succeeded and the entry will expire via TTL within
// `cfg.Redis.TicketTypeTTL`. Returning the cache error would let a
// transient Redis outage block legitimate ticket-type deletions.
func (d *ticketTypeCacheDecorator) Delete(ctx context.Context, id uuid.UUID) error {
	if err := d.inner.Delete(ctx, id); err != nil {
		return err
	}
	if d.ttl <= 0 {
		return nil
	}
	if delErr := d.client.Del(ctx, ticketTypeCacheKey(id)).Err(); delErr != nil {
		d.logger.Warn(ctx, "ticket_type cache invalidate-on-delete failed; entry will expire via TTL",
			tag.TicketTypeID(id), tag.Error(delErr))
	}
	return nil
}

// DecrementTicket passes through. The cache entry only stores immutable
// fields BookTicket reads — `available_tickets` does drift after a
// Decrement but BookTicket doesn't read that field, so cache staleness
// is a no-op for the hot path. Admin paths that DO want fresh
// `available_tickets` should call SumAvailableByEventID (uncached) or
// query Postgres directly.
func (d *ticketTypeCacheDecorator) DecrementTicket(ctx context.Context, id uuid.UUID, quantity int) error {
	return d.inner.DecrementTicket(ctx, id, quantity)
}

// IncrementTicket passes through; same reasoning as DecrementTicket.
func (d *ticketTypeCacheDecorator) IncrementTicket(ctx context.Context, id uuid.UUID, quantity int) error {
	return d.inner.IncrementTicket(ctx, id, quantity)
}

// SumAvailableByEventID passes through. RehydrateInventory + drift
// detector both want a FRESH per-sweep value; caching would defeat
// the purpose.
func (d *ticketTypeCacheDecorator) SumAvailableByEventID(ctx context.Context, eventID uuid.UUID) (int, error) {
	return d.inner.SumAvailableByEventID(ctx, eventID)
}

// Compile-time assertion: ticketTypeCacheDecorator implements every
// method on domain.TicketTypeRepository. Adding a new method to the
// port without updating this decorator becomes a build error.
var _ domain.TicketTypeRepository = (*ticketTypeCacheDecorator)(nil)
