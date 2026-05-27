# DDD & Clean Architecture Implementation Review

**Reviewer**: Backend Architect (Claude Opus, project audit 2026-05-27)
**Scope**: Aggregate boundaries, layer discipline, domain event correctness, ACL, bounded contexts
**Go version pinned**: 1.25.0 / toolchain 1.25.10
**Repo state**: `fix/grafana-datasource-uid`, post-v1.0.0 (released 2026-05-10), 100+ PRs shipped
**Files cited**: absolute paths, line numbers, with quoted snippets only where the exact text is load-bearing

---

## TL;DR

The codebase implements DDD + Clean Architecture **substantively well** for a Go monolith. The big-ticket items are right: unexported entity fields with accessor methods + immutable factories + `ReconstructX` for rehydration; explicit state-machine transitions returning new values; price snapshot on `Order` (D4.1); UoW that hands a per-tx `Repositories` bundle and never leaks `*sql.Tx`; domain ports defined in `domain/` and implemented in `infrastructure/postgres/`; clean separation between `application.OrderFailedEvent` (integration event DTO, JSON-tagged) and `domain.OutboxEvent` (the persistence aggregate). The domain layer has **zero imports of infrastructure** — verified by grep. The Stripe SDK never escapes `internal/infrastructure/payment/stripe_gateway.go` — verified by grep.

The notable fidelity gaps are narrow and concrete:

1. **Anemic Saga / Webhook orchestrators** push real domain decisions (when can a `succeeded` event be ignored? what does "late success after expiry" mean?) into `application/payment/webhook_service.go` and `application/saga/compensator.go` as procedural switch-fests. `Order` carries the state-machine transitions but not the cross-cutting "what should we do about this signal" logic.
2. **No Money value object.** `(amountCents int64, currency string)` is duplicated on `Order`, `TicketType`, `PaymentIntent`, `OrderFailedEvent`, the DTOs, and the gateway adapter — six copies of the invariant. The PR-39 backlog item names this; it's not yet acted on.
3. **Booking decorators use a stale `eventID` parameter name** that disagrees with the underlying `Service.BookTicket(ticketTypeID …)` signature (a D4.1 miss). Pure cosmetic bug today because Go is positional, but it's a real maintenance hazard.
4. **`application/booking/service.go` imports `infrastructure/config`** — this is a layer-direction violation. The fix is a 5-line struct param.
5. **Repository interfaces are correctly placed in domain**, but the actual interfaces are **too wide** — `OrderRepository` carries 13 methods including reconciliation-specific queries (`FindStuckCharging`, `FindStuckFailed`, `FindExpiredReservations`, `CountOverdueAfterCutoff`). Vernon would split these into role interfaces.
6. **Bounded-context seam**: the codebase is realistically 3 contexts (Catalog / Booking / Payment) mashed into one module. The current layout doesn't pretend otherwise (subpackages `event/`, `booking/`, `payment/`, `saga/`), but the `application.Repositories` bundle wires all five repos into every UoW closure — that's the only place the seam is visible as friction.

None of these are CRIT. The architecture is **A-/B+ in the Vernon sense, A in the Go-pragmatic sense**. The risk if left alone is gradual drift into the "fat application service + anemic domain" anti-pattern as ticketing rules grow (early-bird windows, per-user limits, multi-section pricing) — the schema is ready for those, the domain isn't.

---

## 1. Layer dependency direction — actual vs claimed

Verified by `grep -rn "booking_monitor/internal/infrastructure" internal/domain/ internal/application/`:

| From → To | Count | Verdict |
|---|---|---|
| `domain/*` → `infrastructure/*` | **0** | Clean |
| `domain/*` → `application/*` | 0 | Clean |
| `application/*` → `domain/*` | many | Expected (DIP target) |
| `application/*` → `infrastructure/*` | **7 hits, all `infrastructure/config`** | Layer-direction violation, MINOR |
| `application/*` → `application/*` (sibling subpackages) | yes (saga/booking/event consume `application.OrderFailedEvent` + `application.UnitOfWork`) | Expected |
| `infrastructure/*` → `domain/*` | many | Expected (adapter target) |
| `infrastructure/*` → `application/*` | yes (postgres/uow.go implements `application.UnitOfWork`, infrastructure/api wires application services) | Expected (adapter pattern) |

The `application → infrastructure/config` edge is the **only structural fidelity break**:

- [internal/application/booking/service.go:11](../../internal/application/booking/service.go) — `"booking_monitor/internal/infrastructure/config"`
- [internal/application/module.go:8](../../internal/application/module.go) — same
- [internal/application/booking/sync/service.go:41](../../internal/application/booking/sync/service.go) — same
- [internal/application/booking/synclua/service.go:46](../../internal/application/booking/synclua/service.go) — same
- [internal/application/booking/service_kafka_intake.go:12](../../internal/application/booking/service_kafka_intake.go) — same

`config.Config` is technically infrastructure (`cleanenv` env parsing lives in that package). The pragmatic fix is **dependency-passed primitives** at the service constructor: `NewService(... reservationWindow time.Duration)` rather than `NewService(... cfg *config.Config)`. The booking service only reads `cfg.Booking.ReservationWindow` (one field) — passing the whole `*config.Config` is convenience-coupling that creates a non-trivial DIP violation. Confirmed: `service.go:81` does exactly `s.reservationWindow = cfg.Booking.ReservationWindow` once at construction.

### Diagram (actual)

```
              ┌─────────────────────────────────┐
              │      internal/domain/           │  no imports outside std + uuid
              └────────────▲────────────────────┘
                           │ (only direction)
              ┌────────────┴────────────────────┐
              │  internal/application/          │  ─── leak ──► infrastructure/config
              │     uow.go (port)               │       (5 files)
              │     order_events.go (DTO)       │
              │     {booking,worker,payment,    │
              │      saga,outbox,recon,event,   │
              │      expiry,admin}/             │
              └────────────▲────────────────────┘
                           │
              ┌────────────┴────────────────────┐
              │  internal/infrastructure/       │
              │    persistence/postgres/        │  implements domain ports
              │    payment/stripe_gateway.go    │  implements domain.PaymentGateway
              │    cache/                       │  implements domain.InventoryRepository
              │    api/                         │  drives application.Service
              │    config/                      │  *** consumed by application — fix ***
              └─────────────────────────────────┘
```

Dotted-line leak is the only Clean-Architecture-letter violation.

---

## 2. Aggregate audit

Per Vernon (Effective Aggregate Design, "Red Book" ch 10) — one transaction modifies one aggregate; referencing other aggregates by ID; invariants enforced inside the aggregate root.

| Aggregate | Where do invariants live? | Cross-aggregate writes in one tx? | Notes |
|---|---|---|---|
| **`Order`** ([domain/order.go](../../internal/domain/order.go)) | Excellent — invariants in `NewOrder`/`NewReservation` factories (lines 304-407); transitions are immutable methods returning new values (`MarkPaid`, `MarkExpired`, etc.); fields all unexported. | **YES, by design** — see worker + saga UoW closures. | Cleanest aggregate of the three. State machine doc-commented exhaustively at line 28-92. Caller-generated UUIDv7 id (line 257-263 doc). |
| **`Event`** ([domain/event.go](../../internal/domain/event.go)) | Solid — `NewEvent` factory at line 57-75; `Deduct` returns new value at line 98-108. But `Event.availableTickets` is **frozen post-D4.1** (deprecated comment line 124-135) — invariant now mostly lives on `TicketType`. | n/a — `Event` no longer participates in the booking hot-path write. | Backlog item: `event_sections` was renamed to `event_ticket_types`; `events.available_tickets` column is dead and pending removal (migration to come). |
| **`TicketType`** ([domain/ticket_type.go](../../internal/domain/ticket_type.go)) | Solid — factory validates 8 invariants at line 126-187; transitions are repo-level (`DecrementTicket` / `IncrementTicket`) at line 286-313 with `WHERE available_tickets >= $quantity` SQL guard. | **YES — booked together with `Order` in the same UoW** ([worker/message_processor.go:84-130](../../internal/application/worker/message_processor.go)). | Strictly Vernon-violating (one tx writes both `orders` and `event_ticket_types`) but the project explicitly accepts this and documents the rationale (next row). |
| **`OutboxEvent`** ([domain/event.go:164-224](../../internal/domain/event.go)) | Factory `NewOrderFailedOutbox` validates + assigns UUIDv7 + UTC time. Aggregate-of-one. | YES — written in the same tx as `Order.MarkPaymentFailed` / `MarkExpired`. **This is the documented exception** (transactional outbox pattern, per Microservices.io). | Correct usage of the exception. |
| **`PaymentIntent`** ([domain/payment.go:144-189](../../internal/domain/payment.go)) | Anemic — public struct with public fields, no factory. | n/a (returned by gateway adapter, not persisted) | This is intentional — it's a return-value DTO, not a persisted aggregate. Marked as such by its docstring. |
| **`SagaCompensation`** ([domain/saga_compensation.go](../../internal/domain/saga_compensation.go)) | Repository-only — no aggregate entity, only a `SagaCompensationRepository` interface. | YES — `RecordCompletion` called inside the saga UoW alongside `MarkCompensated`. | Properly part of the `Order` aggregate's transactional boundary; the repository name is misleading because there's no `SagaCompensation` entity per se, just a persistence trace. |

### The cross-aggregate-tx question

Two flows write multiple aggregates in one tx:

**Worker** ([message_processor.go:84-130](../../internal/application/worker/message_processor.go)) — one UoW writes:
- `TicketType.DecrementTicket` (TicketType aggregate)
- `Order.Create` (Order aggregate)

**Saga compensator** ([saga/compensator.go:168-253](../../internal/application/saga/compensator.go)) — one UoW writes:
- `Order.GetByID` (read)
- `TicketType.IncrementTicket` (TicketType aggregate)
- `Order.MarkCompensated` (Order aggregate)
- `SagaCompletion.RecordCompletion` (Saga aggregate)
- (Outside the tx) `inventoryRepo.RevertInventory` (Redis)

**Event creation** ([event/service.go:134-200](../../internal/application/event/service.go)) — one UoW writes:
- `Event.Create` (Event aggregate)
- `TicketType.Create` (TicketType aggregate)

Vernon would prefer **eventually consistent** cross-aggregate operations via in-process domain events (raise → commit Order → handle → schedule TicketType deduction). The project consciously chose **strong consistency** for these flows because the alternative (inventory leaks under partial commit) is operationally worse than the architectural impurity. **This is a correct trade-off for a flash-sale system** — Vernon explicitly carves out outbox-style exceptions in IDDD ch 8 ("Domain Events"). The codebase even names the cluster correctly: see `application/uow.go:7-21` — `Repositories` is annotated as "an application-level concession" with the Vernon citation.

**Verdict**: Not a violation. The codebase explicitly understands the rule and the exceptions. The exceptions are well-scoped. CR: would prefer to see this annotated in a single `docs/architecture/aggregate-boundaries.md` (currently scattered across `uow.go` doc comments, `architectural_backlog.md` § KKTIX, and `docs/design/ticket_pricing.md` §6).

---

## 3. Anemic-vs-rich model audit

For each entity: methods on entity vs methods that should be / are on service. Asymmetric cases flagged.

### `Order` — moderately rich

**On entity (`order.go`)**:
- `NewOrder`, `NewReservation`, `ReconstructOrder` (factories)
- `MarkCharging`, `MarkConfirmed`, `MarkFailed`, `MarkCompensated`, `MarkAwaitingPayment`, `MarkPaid`, `MarkExpired`, `MarkPaymentFailed` — all are immutable transitions that validate source state
- `HasPriceSnapshot()` — data-integrity predicate (good!)
- Accessors

**On `application/payment/webhook_service.go`** (should arguably be entity methods):
- `isTerminalForWebhook(order.Status())` (line 136-145) — package-level function reading status. Should be `order.IsTerminal() bool` or `order.IsTerminalForWebhook() bool`.
- `isFailureTerminalAfterPay(order.Status())` (line 158-166) — same shape. **Late-success detection** is the kind of cross-cutting domain rule that should live on the entity (and have unit-test coverage at the entity level, not buried in the webhook service tests).
- The "what is a paid order" / "what is a failure-terminal order" classification is replicated in `webhookService.handleSuccess` (line 318+) and `handleLateSuccess` (line 419+) via case switches against bare status strings.

**On `application/booking/service.go`** (correctly application orchestration):
- `BookTicket` flow — UUID mint, ctx validation, reservation TTL compute, Redis call, factory invoke. None of this is domain logic per se; it's application orchestration of multiple ports.

**On `application/saga/compensator.go`** (a bit muddled):
- Lines 173+: `if order.Status() == domain.OrderStatusCompensated { … }` — should be `order.IsCompensated()` or `order.NeedsCompensation() bool`.
- The "resolve which ticket type to credit" 3-path branch (`resolveTicketTypeID`, line 399-440) is genuinely application orchestration (it queries the repo), but the **classification of the three paths (clean / legacy-fallback / unrecoverable)** is a domain invariant that's stuck in a method comment instead of being a typed enum.

**Recommended additions to `Order`** (MAJOR-2 in findings):
- `IsPattern A() bool` / `IsLegacy() bool` for the migration window
- `IsTerminal() bool` returning true for {Paid, Compensated}
- `IsFailureTerminal() bool` returning true for {Expired, PaymentFailed, Compensated}
- `RequiresCompensation() bool` returning true for {Expired, PaymentFailed, Failed}
- `ReservationActive(now time.Time) bool` returning `o.reservedUntil.After(now)` (currently inline at `payment/service.go:115`, `webhook_service.go:324`)

### `Event` — anemic (intentional)

Only `Deduct` transition. Most of what `Event` did pre-D4.1 moved to `TicketType` and `events.available_tickets` is frozen. Not a real anemia complaint — the entity is correctly drifting toward "billboard" status (per [docs/design/ticket_pricing.md §4](../design/ticket_pricing.md)).

### `TicketType` — moderately anemic

Only `NewTicketType` factory. **No transition methods** like `OnSale(now)`, `WithinSaleWindow(now)`, `WithinPerUserLimit(prior int)`. The schema reserves these fields (`saleStartsAt`, `saleEndsAt`, `perUserLimit`) but the domain doesn't expose behavior on them — booking flow can't enforce them yet (per the doc comment at line 80-83: "Schema-only in D4.1 — BookTicket does NOT enforce these yet; D8 business-rule work wires the check in.").

This is **the latent anemia risk**: the project has explicitly deferred the business-rule enforcement to D8, but the natural place for the enforcement (a `tt.AllowsPurchase(now, quantity, priorPurchases)` method) doesn't have a placeholder. When D8 lands, the safe path is `tt.AllowsPurchase(...)` returning `error` — the unsafe path is two more `if tt.SaleStartsAt() != nil && tt.SaleStartsAt().After(now) { ... }` branches in `BookingService.BookTicket`, which is the exact "fat application service" anti-pattern Vernon warns about.

### `PaymentIntent` — anemic by design

Public struct, no factory, no methods. Correct — it's a DTO returned by the gateway, not a persisted aggregate.

### `IdempotencyResult` — anemic by design

Same — boundary DTO.

---

## 4. Domain Events vs Integration Events

Cited Microsoft Learn definition: *"Domain events represent something that has happened in the past within a specific bounded context that domain experts care about… Integration events are boundary artifacts whose purpose is interoperability."* ([Microsoft Learn](https://learn.microsoft.com/en-us/dotnet/architecture/microservices/microservice-ddd-cqrs-patterns/domain-events-design-implementation), citation in §References below).

This project conflates them, but in a **principled way** that the docstrings acknowledge:

- [internal/application/order_events.go:12-20](../../internal/application/order_events.go) — `// OrderFailedEvent + its factory live here, in application, NOT in domain. It is a wire-format DTO published to Kafka…`
- [internal/domain/event.go:148-165](../../internal/domain/event.go) — `OutboxEvent` is the persistence aggregate; its `payload []byte` is opaque.

So the actual structure is:

```
Domain (in-process)              Application (DTO)              Infrastructure (transport)
─────────────────                ─────────────────              ─────────────────────────
domain.OutboxEvent      ─emits→  application.OrderFailedEvent ─json→  kafka.Message "order.failed"
(aggregate, persisted)           (wire schema, JSON-tagged)            (byte payload)
```

What's missing: **there are no actual in-process domain events.** When the `webhookService.handleFailure` decides "payment failed → walk Order to PaymentFailed → emit `order.failed`", that decision is procedural code in the application service, not an event raised by `order.MarkPaymentFailed()` that an in-process handler dispatches.

The trade-off is intentional and reasonable for a single-process Go monolith. The cost is real though: **the application service is the only place where the "what happens when X" knowledge lives**. There's no `Order.RaiseFailureEvent(reason string)` that produces a typed `OrderFailureRaised` value the application then translates to outbox. The current pattern is:

```go
// webhook_service.go:486-502 (paraphrased)
err := uow.Do(ctx, func(repos) error {
    if e := repos.Order.MarkPaymentFailed(ctx, orderID); e != nil { return e }
    failedEvent := application.NewOrderFailedEventFromOrder(order, "payment_failed: "+reason)
    payload, _ := json.Marshal(failedEvent)
    outboxEvent, _ := domain.NewOrderFailedOutbox(payload)
    _, e = repos.Outbox.Create(ctx, outboxEvent)
    return e
})
```

A textbook DDD version would have `Order` raise a domain event during `MarkPaymentFailed`, an in-process handler pull it off the aggregate, and the outbox-bridge handler serialize. That's three more types and one in-process bus for what the current code does in 8 lines.

**Verdict**: NIT. The current pattern works and the names are honest. If the team grows or this monolith becomes a candidate for splitting into Catalog / Booking / Payment services, that's the moment to introduce true domain events. **For a 100-PR monolith run by one person, it would be over-engineering today.** Cited sources agree: [Three Dots Labs "Is Clean Architecture Overengineering?"](https://threedots.tech/episode/is-clean-architecture-overengineering/) explicitly endorses this kind of pragmatic compression.

The version field (`OrderEventVersion = 3` at line 49 of `order_events.go`) is **excellent practice** — it's the explicit schema-versioning contract you want at the integration-event boundary.

---

## 5. Repository / UoW placement & leakage

### Repository interfaces — placement: ✓

All defined in `domain/`, all implemented in `infrastructure/persistence/postgres/`:
- [domain/order.go:610-765](../../internal/domain/order.go) — `OrderRepository` (13 methods)
- [domain/event.go:111-146](../../internal/domain/event.go) — `EventRepository` (7 methods)
- [domain/ticket_type.go:255-327](../../internal/domain/ticket_type.go) — `TicketTypeRepository` (7 methods)
- [domain/event.go:226-230](../../internal/domain/event.go) — `OutboxRepository` (3 methods)
- [domain/idempotency.go:44-76](../../internal/domain/idempotency.go) — `IdempotencyRepository` (2 methods)
- [domain/inventory.go:32-128](../../internal/domain/inventory.go) — `InventoryRepository` (6 methods)
- [domain/saga_compensation.go:20-35](../../internal/domain/saga_compensation.go) — `SagaCompensationRepository` (3 methods)
- [domain/payment.go:209-235](../../internal/domain/payment.go) — `PaymentIntentCreator`, `PaymentStatusReader`, `PaymentGateway` (1+1+composed)

All interface positions correct. DIP discipline is intact.

### Repository interfaces — sizing: ✗ (MAJOR-3)

`OrderRepository` has 13 methods. Per Go idiom ("[Accept interfaces, return structs](../.claude/rules/golang/coding-style.md)") and the cited critique ([dentedlogic.com](https://dev.to/dentedlogic/the-repository-pattern-done-right-consumer-defined-interfaces-in-go-1f14): *"The interface grows and becomes a Pandora's box of hundreds of tangentially related queries"*), this is the wrong shape. Concrete suggestion: split into **role interfaces consumed where used**:

| Role interface | Methods | Consumers |
|---|---|---|
| `OrderWriter` | `Create`, `MarkAwaitingPayment`, `MarkPaid`, `MarkExpired`, `MarkPaymentFailed`, `MarkCompensated`, `SetPaymentIntentID` | worker, payment, saga, webhook, expiry |
| `OrderReader` | `GetByID`, `ListOrders`, `FindByPaymentIntentID` | booking handlers, /pay handler, webhook |
| `OrderStuckFinder` | `FindStuckCharging`, `FindStuckFailed` | recon, saga-watchdog only |
| `OrderExpiryFinder` | `FindExpiredReservations`, `CountOverdueAfterCutoff` | expiry sweeper only |
| `OrderLegacy` (deprecated, removed post-D7) | `MarkCharging`, `MarkConfirmed`, `MarkFailed` | none (legacy path retired) |

The repo struct stays one struct. **The interface is split where it's consumed**, which is the Go convention. The current code forces the saga-watchdog (which needs 2 methods) and the expiry sweeper (which needs 2 methods) to receive a 13-method bundle they can mock-overhead at test time.

Confirmed legacy methods are dead in production hot paths (CLAUDE.md says "legacy edges remain available because Pattern A is shipping across multiple PRs"). The `architectural_backlog.md` already tracks this — § "Legacy `MarkCharging` / `MarkConfirmed` interface methods". The plan is good; ship it.

### UoW — placement: ✓, leakage: ✓

[internal/application/uow.go:55-57](../../internal/application/uow.go):
```go
type UnitOfWork interface {
    Do(ctx context.Context, fn func(repos *Repositories) error) error
}
```

- Interface in `application/` (the consumer side) — correct
- Implementation in `infrastructure/persistence/postgres/uow.go` — correct
- `*sql.Tx` is **never exposed** to the closure. `Repositories.Order`/`Event`/etc. are themselves repo interfaces; the postgres impl `WithTx` clones internally. Excellent.
- Documentation acknowledges Bourgon/Morrison's "atomic repositories" foot-gun explicitly at line 50-54 — fresh bundle per `Do`, never cached.

The `Repositories` bundle is the only minor wart — it bundles **all five** repos even when a flow only needs one or two. The bookings UoW closure only writes Order + TicketType. The saga closure needs Order + TicketType + SagaCompletion + Outbox. The expiry closure needs Order + Outbox. The bundle pattern keeps the closure signature stable across flows at the cost of slightly fatter dependency injection. **Verdict: acceptable trade-off; the `Repositories` struct is shallow (just 5 interface fields), and a unit test's "fail loudly on the unset field" cost is small. NIT.**

### One subtle hot-path bypass — none found

Spot-checked `worker/message_processor.go`, `payment/service.go`, `payment/webhook_service.go`, `saga/compensator.go`, `event/service.go`, `expiry/sweeper.go`. All multi-write flows go through `uow.Do`. The single-write paths (`SetPaymentIntentID` in `payment/service.go:166`) are SQL-level race-safe and don't need a tx — that's the documented decision at line 87-90.

---

## 6. Value Object discipline (Money / Currency / OrderStatus)

### `Currency` — half a value object

[domain/currency.go](../../internal/domain/currency.go) — has `NormalizeCurrency(s)` (lowercase) and `isValidCurrencyCode(s)` (shape check, 3 ASCII letters). But it's **package-private functions on plain `string`**, not a `type Currency string` with a constructor.

This is a missed opportunity:
- `NewReservation(... currency string, ...)` is currently called with raw strings; you can pass `"USDQ"` at the call site and only learn at the factory.
- A real `Currency` type would carry the invariant by construction.

```go
// what's missing
type Currency string  // unexported via package?

func NewCurrency(s string) (Currency, error) { ... }
func (c Currency) String() string { return string(c) }
```

### `Money` — absent

There is no `Money` type. `(amount_cents int64, currency string)` appears as paired fields in **at least seven places**:
- [domain/order.go:289-291](../../internal/domain/order.go)
- [domain/ticket_type.go:92-93](../../internal/domain/ticket_type.go)
- [domain/payment.go:163-168](../../internal/domain/payment.go) — `PaymentIntent.AmountCents` + `PaymentIntent.Currency`
- [application/order_events.go](../../internal/application/order_events.go) — `OrderFailedEvent` (doesn't carry money currently, but the schema in `docs/design/ticket_pricing.md` reserves it)
- [infrastructure/api/dto/response.go:46-47](../../internal/infrastructure/api/dto/response.go)
- [infrastructure/api/dto/response.go:238-239](../../internal/infrastructure/api/dto/response.go)
- [infrastructure/payment/stripe_gateway.go:298-305](../../internal/infrastructure/payment/stripe_gateway.go)

The design rationale at [docs/design/ticket_pricing.md §9](../design/ticket_pricing.md) is explicit:
> 結論:現在用 int64,真的需要 Money 抽象時(multi-currency 不同 minor unit 或 tax/discount 進入 scope),引入 `domain.Money` value object …

The reasoning (Stripe ecosystem alignment, no allocation overhead, deferred until multi-currency lands) is **correct**. But **even today**, a thin `Money` type would carry one invariant the current code spreads across at least four places:
- `NewReservation` checks `amountCents <= 0` (line 384-386)
- `NewTicketType` checks the same (line 144-146)
- `StripeGateway.CreatePaymentIntent` trusts the caller
- DTO marshals raw fields

Concrete recommendation:

```go
// internal/domain/money.go (new, ~20 LOC)
type Money struct {
    cents    int64
    currency Currency
}

func NewMoney(cents int64, currency string) (Money, error) {
    if cents <= 0 { return Money{}, ErrInvalidAmountCents }
    c := NormalizeCurrency(currency)
    if !isValidCurrencyCode(c) { return Money{}, ErrInvalidCurrency }
    return Money{cents: cents, currency: Currency(c)}, nil
}

func (m Money) Cents() int64      { return m.cents }
func (m Money) Currency() Currency { return m.currency }
func (m Money) Multiply(n int) Money { return Money{cents: m.cents * int64(n), currency: m.currency} }
```

`Order` carries `Money` instead of `(amountCents, currency)`. The cost is one type + ~20 LOC. The benefit: every Money creation goes through one validator, and `quantity * priceCents` (the multiplication that happens in `BookingService.BookTicket` via Lua then again in `domain.NewReservation`) gets a typed operation. **MAJOR-4 finding.**

### `OrderStatus` — passable

Typed `type OrderStatus string` with constants and `IsValid()` method ([domain/order.go:125-178](../../internal/domain/order.go)). Good. Missing helpers (`IsTerminal()` etc.) are noted in §3 above.

### `ReservationWindow` — primitive

`time.Duration` everywhere. No `type ReservationWindow time.Duration` with `Validate()` or `Apply(start time.Time)`. Not a finding — `time.Duration` is rich enough in Go.

### `OrderID` — primitive

`uuid.UUID` everywhere, with a `uuid.Nil` zero-value invariant. Doesn't need a typed wrapper — `uuid.UUID` is itself reasonably typed.

---

## 7. Application Service vs Domain Service — Pricing case study

The D4.1 price-snapshot design is the cleanest single example of the "where does this code go" question. Here's the actual placement:

| Concern | Lives in | Verdict |
|---|---|---|
| "How is a price represented?" | `(int64, string)` pair, no `Money` | Should be a domain value object (§6) |
| "When does a price become immutable?" | `domain.NewReservation` line 384-394 | Correct — domain factory |
| "What is the total price of N tickets?" | `Lua deduct script` + `domain.NewReservation` (multiplication implicit via runtime metadata HMGET) | **Split — see below** |
| "Can the merchant edit the price between book and pay?" | Yes, but order snapshot freezes it | Correct domain rule, enforced by snapshot |
| "What currency does Stripe accept?" | `Currency` lowercase shape check + Stripe convention | Domain shape; gateway shape concern |
| "Has the order's reservation expired?" | `payment/service.go:115` `!order.ReservedUntil().After(s.now())` | Should be `order.IsReservationActive(now)` on entity |

**The "what is the total" split**: the Lua script in Redis computes `amount_cents = price_cents × quantity` (verified in `internal/infrastructure/cache/lua/deduct.lua` per `inventory.go:60-100`'s doc-comment chain), then `NewReservation` validates `amountCents > 0` but doesn't recompute. The multiplication lives in Lua because Redis owns the runtime price metadata and the hot path can't afford a Postgres roundtrip. **This is correct application orchestration** — the domain doesn't need to know how Redis got there, only that the resulting Money is valid.

A textbook **`PricingPolicy` domain service** would centralize the "compute total" rule. In this codebase it's a 1-line `cents * qty` in Lua, so a Go-side `PricingPolicy` would be Hexagonal-architecture purity for negligible payoff. NIT.

The **price-snapshot rule itself** is exactly where it belongs (domain factory `NewReservation`). Stripe Checkout / Shopify / Eventbrite all freeze price at order create time (cited at [docs/design/ticket_pricing.md §2](../design/ticket_pricing.md) point 10) — the codebase aligns to the industry SOP.

---

## 8. Bounded Context seam analysis

The project markets itself as "modular monolith — one bounded context." A 100-PR audit suggests it's actually 3 contexts mashed together with neatly drawn subpackage walls:

| Logical context | Today's location | Aggregates owned | External boundaries |
|---|---|---|---|
| **Catalog** | `application/event/`, `application/admin/` (event creation, ticket-type CRUD) | `Event`, `TicketType` | API: `POST /events`, `GET /events/:id`, admin SSE |
| **Booking** | `application/booking/`, `application/worker/`, `application/expiry/` (reservation lifecycle) | `Order` (writes), reads `TicketType` (decrement) and `Event` | API: `POST /book`, `GET /orders/:id`, `GET /history` |
| **Payment** | `application/payment/`, `application/saga/`, `application/recon/`, infrastructure Stripe | `Order` (status writes), `OutboxEvent`, `SagaCompensation`, `PaymentIntent` (external) | API: `POST /orders/:id/pay`, `POST /webhook/payment` |

### Why this matters

The **`application.Repositories` bundle is the seam revealer**. The struct at [application/uow.go:15-21](../../internal/application/uow.go) bundles `Order + Event + Outbox + TicketType + SagaCompletion`. The Catalog context (event creation) writes Event + TicketType. The Booking context writes Order + TicketType. The Payment context writes Order + Outbox + SagaCompletion. **No single flow writes more than two of those at once**, but the bundle hands all five to every closure.

If you were to extract Catalog into its own service tomorrow:
- `EventRepository` + `TicketTypeRepository` move out
- The Booking-context UoW would lose those repos and have to call Catalog over HTTP/gRPC for inventory decrement
- That's exactly the moment the `TicketType.DecrementTicket` write inside the booking UoW becomes architecturally problematic (you've inverted "write across aggregates" into "write across services" — a distributed transaction)

The codebase's current strong-consistency stance is **only safe inside a single bounded context**. The fact that Booking writes TicketType is a context-merging signal — TicketType is a Catalog aggregate that Booking treats as its own (it owns the decrement). Two options:

1. **Acknowledge two contexts merged into one.** Rename `event_ticket_types` and `events` to a single "Catalog" namespace; rename `Order`/`OutboxEvent`/`SagaCompensation` to a "Sales" namespace. Keeping them in one module is fine; calling the inner walls "subpackages, not contexts" is fine. The doc would just admit this.
2. **Treat TicketType inventory as a Booking-owned read model** of Catalog inventory. The Catalog owns the truth (`TicketType` aggregate); the Booking context has a `TicketTypeInventory` projection it updates atomically with `Order`. That's CQRS-style — bigger lift.

**Verdict**: this is the most interesting architectural question in the audit. **Don't refactor today**; surface in `docs/architectural_backlog.md` as a "when do we split" entry. The seam is visible enough that the next architect can see it; the cost of acting today (1k+ LOC of CQRS scaffolding) exceeds the operational risk it would buy down. **MAJOR-5 — documentation only.**

### What about Payment?

Payment **is more cleanly separable**. It writes Order status transitions (which is technically a Booking-context invariant) only via the explicit `MarkPaid` / `MarkPaymentFailed` / `MarkExpired` API on the Order aggregate. The Saga compensator + Webhook handler are the only Payment-context code that touches Order — and they touch it through the aggregate's intended state-machine API, not through arbitrary field writes. **This is the textbook way to do it.** When Payment becomes its own service in some future life, the existing transitions become anti-corruption-layer translations.

---

## 9. ACL audit — Stripe types

Cited posture ([Adrian Bailador / Medium April 2026](https://medium.com/@adrianbailador/anti-corruption-layer-in-net-protecting-your-domain-from-external-apis-2e239532d195)): *"The ACL translates external system responses into domain results, and the domain should never handle payment provider exceptions… contamination — `StripeChargeId` on Order, `PSPCONFIRMED` in the status enum, `MapFromStripeWebhookPayload()` in the service layer — wasn't inevitable."*

This codebase scores **high** here. Grep across `internal/domain/` and `internal/application/` for `stripe\.`:

- **`domain/`**: zero hits.
- **`application/`**: zero hits in payment/service.go, webhook_service.go, port.go, webhook_dto.go.

The Stripe SDK (`github.com/stripe/stripe-go/v82`) is **only imported by**:
- `internal/infrastructure/payment/stripe_gateway.go` (the adapter)
- `internal/infrastructure/payment/stripe_gateway_test.go` (tests)

The adapter's contract surface:

```go
// stripe_gateway.go:144-147 — compile-time interface assertions
var (
    _ domain.PaymentStatusReader  = (*StripeGateway)(nil)
    _ domain.PaymentIntentCreator = (*StripeGateway)(nil)
    _ domain.PaymentGateway       = (*StripeGateway)(nil)
)
```

This is **exactly** the ACL pattern: the adapter translates `stripe.PaymentIntentStatus` → `domain.ChargeStatus` (mapStripeStatusToCharge at line 405-440), `stripe.Error` → `domain.ErrPayment*` (mapStripeError at line 442+), and the domain types never see a Stripe import.

### The webhook DTO is the one subtle case

[application/payment/webhook_dto.go:42-97](../../internal/application/payment/webhook_dto.go) reimplements Stripe's `Event` envelope shape **as our own struct in application/**. The docstring at lines 9-23 explains:

> Why re-implement Stripe's struct rather than import their SDK: The dependency is small so a vendored SDK is overkill for the handful of fields we actually inspect. The mock provider in `internal/infrastructure/payment/` emits the same shape directly without round-tripping through the SDK so unit tests stay deterministic. Switching providers later means swapping the verifier (different header / hash) and the field accessors — keeping our own struct makes the boundary explicit.

This is **the right call**. The DTO lives in `application/payment/` (the consumer side), is JSON-tagged, and the domain never sees it — `WebhookService.HandleWebhook` translates `Envelope` into `domain.Order` transitions via the explicit `MarkPaid` / `MarkPaymentFailed` API. The trade-off is that Stripe-shape vocabulary leaks into the application layer (`PaymentIntentObject`, `PaymentIntentLastError`), but that's still one layer in from domain.

**One mild gap**: `domain.PaymentIntent` carries `Metadata map[string]string` (line 188) — that's a Stripe-shape concept. The doc-comment at line 173-189 even cites Stripe. Strictly, this is a leaked concept; pragmatically, it's so generic that an Adyen/Braintree adapter would map onto the same map shape. NIT.

### Score: A

The Stripe ACL is **the strongest example of architectural fidelity in the codebase.** Even the metric labels (`outcome="declined"`, `"transient"`, `"misconfigured"`, `"invalid"`) are domain-vocabulary, not Stripe-vocabulary — a future provider swap leaves the dashboards intact.

---

## 10. Decorator pattern critique

[internal/application/booking/module.go-equivalent wiring at application/module.go:40-54](../../internal/application/module.go):

```go
fx.Provide(func(...) booking.Service {
    base := booking.NewService(orderRepo, ticketTypeRepo, inventoryRepo, cfg)
    return booking.NewMetricsDecorator(
        booking.NewTracingDecorator(base),
        metrics,
    )
}),
```

This is **textbook GoF decorator over the application port** (`booking.Service` interface). Each decorator wraps a `Service` and returns a `Service`. Call sites (HTTP handler, worker, recon, saga, etc.) inject `booking.Service` — they get the fully-decorated chain transparently, no knowledge that decorators exist.

Verified independently for: `worker/message_processor.go` (has `message_processor_metrics.go` next to it), `saga/compensator.go` (metrics injected directly because compensator's three success paths return `nil` and are indistinguishable from outside, per the doc at line 81-86), `outbox/relay.go` (has `relay_tracing.go`), `payment/service.go` (no decorator visible at this layer — the gateway has metrics on the infrastructure side).

### The latent bug — MINOR-6

[application/booking/service_metrics.go:25](../../internal/application/booking/service_metrics.go) and [application/booking/service_tracing.go:25](../../internal/application/booking/service_tracing.go) both use the parameter name `eventID uuid.UUID`. The actual `Service.BookTicket` signature in [application/booking/service.go:47](../../internal/application/booking/service.go) is `BookTicket(... ticketTypeID uuid.UUID, ...)` — D4.1 (KKTIX ticket-type model) renamed the customer-facing argument but **the decorators were not updated**.

```go
// service.go:47 (canonical)
BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error)

// service_metrics.go:25 (stale)
func (d *metricsDecorator) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (domain.Order, error) {

// service_tracing.go:25-30 (stale + worse: emits `event_id` as span attribute when value is actually ticket_type_id)
func (s *tracingDecorator) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (domain.Order, error) {
    ctx, span := otel.Tracer(tracerName).Start(ctx, "BookTicket", trace.WithAttributes(
        attribute.Int("user_id", userID),
        attribute.String("event_id", eventID.String()),     // ← labels a ticket_type_id as event_id
```

Severity:
- **Compile**: passes (Go is positional, both are `uuid.UUID`)
- **Runtime**: tracing decorator emits OTel attribute key `event_id` with the value of `ticketTypeID.String()` — observability data is wrong. Jaeger queries by `event_id` return the ticket_type's UUID instead of the event's UUID, breaking the "find all bookings for event X" trace lookup pattern.
- **Test surface**: tests probably don't catch this because they pass the same UUID and assert success, not attribute name.

**MINOR-6** — surgical edit, 2 files, ~6 line changes. Should be fixed.

### Decorator-over-domain-port?

The cited critique would prefer decorators over **domain ports** (e.g. `OrderRepository`) rather than **application services** (e.g. `booking.Service`). The codebase does the latter, which is the standard Go interpretation. Verdict: fine.

### Per-subpackage Metrics ports

Each subpackage exposes its own `Metrics` interface (`booking.Metrics`, `recon.Metrics`, `saga.CompensatorMetrics`, `worker.Metrics`, `payment.StripeMetrics`, `payment.WebhookMetrics`). Each has a `NopMetrics` for tests. The Prometheus-backed implementations live in `internal/infrastructure/observability/` and `internal/bootstrap/`. This is **textbook hexagonal architecture** — the application package never imports Prometheus.

---

## Findings (severity: CRIT / MAJOR / MINOR / NIT)

### [MAJOR-1] Anemic Order — late-success/idempotent-replay logic should be on the entity, not in `webhook_service.go`

**Where**: [internal/application/payment/webhook_service.go:130-167](../../internal/application/payment/webhook_service.go) (`isTerminalForWebhook`, `isFailureTerminalAfterPay`), used in `HandleWebhook` lines 238-258
**What**: The two predicate functions interrogate `order.Status()` to decide whether the inbound webhook is a duplicate / late-success / regular handling. They're domain invariants ("an order in {Paid, Expired, PaymentFailed, Compensated} is terminal from the webhook perspective"), but they live as package-private functions next to the application service.
**Why it matters in 2025**: Vaughn Vernon's IDDD ch 10 + Eric Evans's DDD ch 5 both classify this kind of "what does X mean about the entity" as **aggregate behavior, not application orchestration**. Right now you can't unit-test the late-success classification without invoking the full webhook flow with mocked UoW + metrics + repos. With the predicates on `Order`, you'd write `TestOrder_IsTerminal_ForCompensated_ReturnsTrue` in `domain/order_test.go` and have direct coverage. The maintenance pain compounds as more failure modes appear (refunds, disputes, holds).
**Recommendation**:
1. Move `isTerminalForWebhook` → `func (o Order) IsTerminalForWebhook() bool` on the entity.
2. Move `isFailureTerminalAfterPay` → `func (o Order) IsFailureTerminalAfterPay() bool`.
3. Add `func (o Order) IsReservationActive(now time.Time) bool` consolidating the inline `!o.reservedUntil.After(now)` checks at `payment/service.go:115` and `webhook_service.go:324`.
4. Add `func (o Order) RequiresCompensation() bool` returning true for {Expired, PaymentFailed, Failed}.
5. Add `TestOrder_StateMachine_Predicates_*` to `domain/order_test.go`.
**Estimated effort**: 1 small PR, ~80 LOC + tests.
**References**: [Vernon Effective Aggregate Design](https://kalele.io/effective-aggregate-design/) (rich-behavior rule); current code style precedent in `Order.HasPriceSnapshot()` at order.go:605-607.

### [MAJOR-2] No `Money` value object; `(amountCents int64, currency string)` duplicated across 7 locations

**Where**: Listed at §6. Primary sites: [domain/order.go:289-291](../../internal/domain/order.go), [domain/ticket_type.go:92-93](../../internal/domain/ticket_type.go), [domain/payment.go:163-168](../../internal/domain/payment.go).
**What**: Every aggregate that needs to carry money carries the same two-field shape with the same validation duplicated in each factory. There is no `domain.Money` type.
**Why it matters in 2025**: Cited Pretix/Stripe/Shopify all use a Money-equivalent abstraction. Today's specific failure mode: `domain.NewReservation` (order.go:384-394) and `domain.NewTicketType` (ticket_type.go:144-151) repeat the *exact same* `amountCents > 0 && len(currency) == 3 && isASCIILetter` check. A future bugfix that strengthens the check on one site is a 1-line PR; making it consistent across both is 4 PRs (factory + every call site that uses the same shape).
**Recommendation**: Introduce `domain/money.go` (~20 LOC) as in §6. Phase the migration:
- Phase 1: add `domain.Money` + `domain.Currency` types alongside existing primitives. Factories accept both (overloaded constructors).
- Phase 2: switch `Order.amountCents` + `Order.currency` to a `money domain.Money` field. Accessors stay (`o.AmountCents()` is `o.money.Cents()`). Persistence row layer translates Money ↔ (int64, string) at the boundary.
- Phase 3: same for `TicketType`, `PaymentIntent`.
- Phase 4: DTOs map Money → wire fields explicitly.
**Don't do**: introduce Money everywhere in one PR. Too much surface to review.
**References**: [docs/design/ticket_pricing.md §9](../design/ticket_pricing.md) (project's own deferred-decision; this finding is the operational nudge).

### [MAJOR-3] `OrderRepository` is too wide — 13 methods including recon/expiry queries

**Where**: [internal/domain/order.go:610-765](../../internal/domain/order.go)
**What**: The single interface bundles writes, plain reads, reconciliation-only queries (`FindStuckCharging`, `FindStuckFailed`), expiry-only queries (`FindExpiredReservations`, `CountOverdueAfterCutoff`), and one CRUD-style write that's only used by the saga (`SetPaymentIntentID`). Consumers (saga-watchdog, expiry sweeper, recon) receive all 13 methods when they need 2-3.
**Why it matters in 2025**: Cited critique ([dentedlogic 2025 Repository Pattern Done Right](https://dev.to/dentedlogic/the-repository-pattern-done-right-consumer-defined-interfaces-in-go-1f14)) explicitly names this anti-pattern. Pragmatically: `gomock` generates a 13-method mock for tests that need 2, which adds boilerplate per test (you have to mock-block all unused methods). Each new method added is a breaking change for every mock site.
**Recommendation**: Split into role interfaces consumed-where-used (see §5 table). The concrete `postgresOrderRepository` keeps all methods. Each role interface lives in `domain/order.go` next to the entity. Consumers depend on the narrow interface. This is the Go convention — small interfaces defined at the consumer side.
**References**: [Three Dots Labs Repository Pattern in Go](https://threedots.tech/post/repository-pattern-in-go/); the project's own [coding-style.md](../../.claude/rules/common/coding-style.md) ("Keep interfaces small (1-3 methods)").

### [MAJOR-4] `application/booking/service.go` imports `internal/infrastructure/config` (and 4 sibling files do the same)

**Where**: [internal/application/booking/service.go:11](../../internal/application/booking/service.go), [internal/application/module.go:8](../../internal/application/module.go), [internal/application/booking/sync/service.go:41](../../internal/application/booking/sync/service.go), [internal/application/booking/synclua/service.go:46](../../internal/application/booking/synclua/service.go), [internal/application/booking/service_kafka_intake.go:12](../../internal/application/booking/service_kafka_intake.go)
**What**: Application layer imports infrastructure. The booking service uses one field (`cfg.Booking.ReservationWindow`) from a config struct that lives in `infrastructure/config/` because the cleanenv parsing is infrastructure.
**Why it matters in 2025**: Clean Architecture's central rule. Today's pain is minimal (config struct is read-only, the import is harmless); tomorrow's pain is "I want to test the booking service against a fake config" — which currently means wiring `cleanenv` boot in tests.
**Recommendation**: Replace `cfg *config.Config` with primitive params at the constructor:

```go
// booking/service.go
func NewService(
    orderRepo domain.OrderRepository,
    ticketTypeRepo domain.TicketTypeRepository,
    inventoryRepo domain.InventoryRepository,
    reservationWindow time.Duration,  // was: cfg *config.Config
) Service { ... }
```

The fx wiring in `application/module.go` translates `cfg.Booking.ReservationWindow` at provide time. Strip the import from `application/booking/`. **5-line edit per file.** Each subpackage gets its dedicated primitive params (sync/synclua/kafka_intake already each get a different subset of config — that's already evidence this is the right shape).
**References**: [Three Dots Labs Clean Architecture in Go](https://threedots.tech/episode/is-clean-architecture-overengineering/) (boundary discipline trumps DI convenience).

### [MAJOR-5] Bounded-context seam (Catalog / Booking / Payment) is not surfaced in docs

**Where**: Conceptual — see §8.
**What**: The codebase is realistically 3 contexts, structured as 1. The current layout doesn't break anything; it just hides the seam.
**Why it matters in 2025**: When the project becomes "this monolith needs to split" (typically a team-size or scaling-event trigger), the splitter needs to know which writes cross contexts. Today's split decision would be: TicketType inventory is read+written by Booking but the entity belongs to Catalog. That's the **first** thing a splitter has to redesign — Booking either gets its own projection of inventory (CQRS), or Catalog exposes a synchronous "DecrementInventory" API (distributed transactions over HTTP, which is what saga compensation already kind of is).
**Recommendation**: Add `docs/architecture/bounded-contexts.md` documenting:
- The 3 logical contexts (Catalog, Booking, Payment).
- Which aggregates belong to which context.
- Which writes cross contexts today, and why that's safe inside a monolith.
- The trigger conditions that would force a split (e.g., independent deployment cadence, team independence, perf isolation for booking under flash-sale load).
- The migration path (TicketType → eventually-consistent projection in Booking; saga compensator as cross-service ACL).
**Don't refactor today.**
**References**: [Martin Fowler Bounded Context](https://www.martinfowler.com/bliki/BoundedContext.html) (the framing); [Three Dots Labs Wild Workouts](https://github.com/ThreeDotsLabs/wild-workouts-go-ddd-example) (the canonical Go example of when to split).

### [MINOR-6] Booking decorators use stale `eventID` parameter name — OTel attribute `event_id` carries `ticket_type_id` value

**Where**: [internal/application/booking/service_metrics.go:25](../../internal/application/booking/service_metrics.go) lines 25-26; [internal/application/booking/service_tracing.go:25-30](../../internal/application/booking/service_tracing.go)
**What**: D4.1 renamed `BookTicket(eventID ...)` → `BookTicket(ticketTypeID ...)` (D4.1 plan + ticket_pricing.md). The decorators were not updated. The tracing decorator emits OTel attribute key `event_id` with `ticketTypeID.String()` as the value.
**Why it matters in 2025**: Observability data is wrong. Jaeger / Grafana Tempo queries by `event_id` return ticket_type UUIDs instead of event UUIDs. Cross-trace correlation against the `events` table breaks. The metric decorator just renames internally and is harmless, but the tracing decorator pollutes spans permanently.
**Recommendation**:

```go
// service_metrics.go:25 + 26
func (d *metricsDecorator) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error) {
    order, err := d.next.BookTicket(ctx, userID, ticketTypeID, quantity)

// service_tracing.go:25-30
func (s *tracingDecorator) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error) {
    ctx, span := otel.Tracer(tracerName).Start(ctx, "BookTicket", trace.WithAttributes(
        attribute.Int("user_id", userID),
        attribute.String("ticket_type_id", ticketTypeID.String()),    // ← key + value both correct
        attribute.Int("quantity", quantity),
    ))
```

**Estimated effort**: 2 files, 6 line changes.
**References**: D4.1 spec in [CLAUDE.md](../../.claude/CLAUDE.md) ("Booking response contract … D4.1: `ticket_type_id`, NOT `event_id`"); KKTIX commitment in [docs/architectural_backlog.md](../architectural_backlog.md).

### [MINOR-7] `TicketType` aggregate is anemic on sale-window / per-user-limit semantics; D8 expansion risks "fat application service"

**Where**: [internal/domain/ticket_type.go](../../internal/domain/ticket_type.go)
**What**: Schema reserves `saleStartsAt`, `saleEndsAt`, `perUserLimit` on `TicketType` but the aggregate has zero behavioral methods exposing those rules. Today, `BookingService.BookTicket` doesn't enforce them (D8 will). When D8 lands, the safe path is methods on `TicketType`; the unsafe path is conditional branches in `BookingService`.
**Why it matters in 2025**: The whole point of having `saleStartsAt` etc. on the aggregate is so the aggregate owns the "is purchase allowed right now" decision. If the enforcement code goes into `BookingService.BookTicket`, the domain entity is reduced to a data carrier (anemic).
**Recommendation**: Pre-bake the placeholders even before D8 wires them in:

```go
// internal/domain/ticket_type.go
func (t TicketType) AllowsPurchase(now time.Time, quantity int, priorPurchasesByUser int) error {
    if t.saleStartsAt != nil && t.saleStartsAt.After(now) {
        return ErrTicketTypeSaleNotStarted
    }
    if t.saleEndsAt != nil && !t.saleEndsAt.After(now) {
        return ErrTicketTypeSaleEnded
    }
    if t.perUserLimit != nil && priorPurchasesByUser+quantity > *t.perUserLimit {
        return ErrTicketTypePerUserLimitExceeded
    }
    return nil
}
```

Today it's an unused method. When D8 enforces the rules, the call site is `if err := tt.AllowsPurchase(now, qty, prior); err != nil { return err }` — one line, instead of three conditional branches duplicated wherever booking enforcement appears.
**Estimated effort**: ~30 LOC for the method + 3 new sentinels + table-driven test.
**References**: Vernon IDDD ch 6 on "make aggregates rich, not bags of getters."

### [MINOR-8] Saga compensator's 3-path resolution (Clean / Legacy / Unrecoverable) is comment-described, not type-modeled

**Where**: [internal/application/saga/compensator.go:399-440](../../internal/application/saga/compensator.go)
**What**: `resolveTicketTypeID` returns `(uuid.UUID, error)` where `(uuid.Nil, nil)` means "Path C — caller should skip the DB increment but continue Redis revert + MarkCompensated." The contract is in a 30-line comment that the caller has to read carefully.
**Why it matters in 2025**: A future refactor that "cleans up" the `(uuid.Nil, nil)` return to `(uuid.Nil, fmt.Errorf("…"))` or to a panic would silently break the saga's Path C handling (the caller's `if ticketTypeID != uuid.Nil` branch becomes unreachable). The current code reads as "if the function returns nil error, you got a valid UUID" — except for this one case where the magic value `uuid.Nil` means "I successfully decided to skip."
**Recommendation**: Model the result as a typed value:

```go
type resolveTicketTypeResult struct {
    ID    uuid.UUID
    Path  string  // "clean" | "legacy" | "unrecoverable"
}
```

Or — cheaper — a `bool` return:

```go
func (s *compensator) resolveTicketTypeID(...) (uuid.UUID, found bool, err error)
```

The caller branches on `found`, not on `id != uuid.Nil`. **Estimated effort**: ~15 LOC change in compensator + test updates.
**References**: Go idiom on multi-return-value disambiguation (the canonical example is `map[K]V` with `value, ok := m[key]`).

### [NIT-9] `PaymentIntent.Metadata` carries Stripe-shape vocabulary into `domain/`

**Where**: [internal/domain/payment.go:170-189](../../internal/domain/payment.go)
**What**: The `Metadata map[string]string` field is documented as Stripe's `metadata` attribute concept. A real ACL would render this as a domain-typed field (e.g., `OrderRef uuid.UUID` directly).
**Why it matters in 2025**: It doesn't, much. Adyen / Braintree / Square all have a `metadata` concept. The leak is at the **vocabulary** level, not the **dependency** level. The field type is `map[string]string` — generic.
**Recommendation**: Leave as-is. Document the design choice in the type's docstring (it already does: line 173-189).

### [NIT-10] Boundary `Currency` is plain `string`, not `type Currency string`

**Where**: domain.go uses `currency string` everywhere; [domain/currency.go](../../internal/domain/currency.go) exposes only `NormalizeCurrency(s string) string`.
**What**: There is no type-safe Currency wrapper.
**Recommendation**: Roll into the MAJOR-2 Money refactor — `Currency` is the unit of `Money`, so introducing both at once is cheaper than two PRs.

---

## Recommendations summary table

| # | Severity | Finding | Lift | Where |
|---|---|---|---|---|
| 1 | MAJOR | Move webhook-classification predicates onto `Order` | ~80 LOC + tests | `domain/order.go` + retire helpers in `webhook_service.go` |
| 2 | MAJOR | Introduce `Money` + `Currency` value objects | 2-3 phased PRs | new `domain/money.go`, then propagate |
| 3 | MAJOR | Split `OrderRepository` into role interfaces | 1 medium PR | `domain/order.go` |
| 4 | MAJOR | Strip `infrastructure/config` import from `application/booking/*` | 5-line edits × 5 files | `application/booking/*.go`, `application/module.go` |
| 5 | MAJOR | Document bounded-context seam | docs only | `docs/architecture/bounded-contexts.md` |
| 6 | MINOR | Rename `eventID` → `ticketTypeID` in booking decorators; fix OTel attr key | 6 lines | `application/booking/service_{metrics,tracing}.go` |
| 7 | MINOR | Add `TicketType.AllowsPurchase` pre-D8 placeholder | ~30 LOC | `domain/ticket_type.go` |
| 8 | MINOR | Replace `(uuid.Nil, nil)` magic value in saga resolver with `(id, found, err)` | ~15 LOC | `application/saga/compensator.go` |
| 9 | NIT | Document `PaymentIntent.Metadata` as ACL-tolerated concept | comment only | `domain/payment.go` |
| 10 | NIT | `type Currency string` value object | rolled into MAJOR-2 | `domain/money.go` |

**No CRIT findings.** The architecture's correctness load-bearing pieces are all in place.

---

## References

1. **Vaughn Vernon, Implementing Domain-Driven Design** (the "Red Book") — aggregate design rules summarized at [Kalele · Effective Aggregate Design](https://kalele.io/effective-aggregate-design/) and [Archi-Lab.io · Aggregate Design Rules](https://www.archi-lab.io/infopages/ddd/aggregate-design-rules-vernon.html). Cited for: "in one transaction, you can only modify one aggregate and never more than one aggregate" — §2.
2. **Martin Fowler · Bliki: Bounded Context** — [martinfowler.com/bliki/BoundedContext.html](https://www.martinfowler.com/bliki/BoundedContext.html). Cited for §8.
3. **Three Dots Labs · Is Clean Architecture Overengineering?** — [threedots.tech/episode/is-clean-architecture-overengineering](https://threedots.tech/episode/is-clean-architecture-overengineering/). Cited for §1, §4, MAJOR-4. Quote (from search snippet, May 2025): *"Clean Architecture should be applied thoughtfully, based on the complexity and goals of the project, rather than treated as a one-size-fits-all solution."*
4. **Three Dots Labs · Wild Workouts** — [github.com/ThreeDotsLabs/wild-workouts-go-ddd-example](https://github.com/ThreeDotsLabs/wild-workouts-go-ddd-example). Canonical Go DDD reference; the project's own coding-style rules cite this as the precedent for accessor-method naming (no `Get` prefix).
5. **Microsoft Learn · Domain events: Design and implementation** — [learn.microsoft.com/en-us/dotnet/architecture/microservices/microservice-ddd-cqrs-patterns/domain-events-design-implementation](https://learn.microsoft.com/en-us/dotnet/architecture/microservices/microservice-ddd-cqrs-patterns/domain-events-design-implementation). Cited for §4 on domain vs integration events. Snippet (.NET 9.x update): *"Domain Events represent something that has happened in the past within a specific bounded context that domain experts care about… Integration events are boundary artifacts whose purpose is interoperability."*
6. **NILUS Consulting · Domain Events vs Integration Events in DDD** — [www.nilus.be/blog/domain_events_vs_integration_events_in_ddd](https://www.nilus.be/blog/domain_events_vs_integration_events_in_ddd/). Cited for §4 framing.
7. **Adrian Bailador · Anti-Corruption Layer in .NET: Protecting Your Domain from External APIs** (April 2026) — [medium.com/@adrianbailador/anti-corruption-layer-in-net-protecting-your-domain-from-external-apis-2e239532d195](https://medium.com/@adrianbailador/anti-corruption-layer-in-net-protecting-your-domain-from-external-apis-2e239532d195). Cited for §9 on Stripe ACL. Quote: *"The ACL translates external system responses into domain results, and the domain should never handle payment provider exceptions."*
8. **DEV.to · Payment Gateways as Anti-Corruption Layers: Applying Hexagonal Architecture in Real-World PHP** — [dev.to/jadlamed007007007/payment-gateways-as-anti-corruption-layers-applying-hexagonal-architecture-in-real-world-php-4c27](https://dev.to/jadlamed007007007/payment-gateways-as-anti-corruption-layers-applying-hexagonal-architecture-in-real-world-php-4c27). Cited for §9 on payment-gateway ACL pattern.
9. **DEV.to · The Repository Pattern Done Right: Consumer-Defined Interfaces in Go** — [dev.to/dentedlogic/the-repository-pattern-done-right-consumer-defined-interfaces-in-go-1f14](https://dev.to/dentedlogic/the-repository-pattern-done-right-consumer-defined-interfaces-in-go-1f14). Cited for §5 / MAJOR-3 on small interfaces. Snippet (2026 article via search): *"The interface grows and becomes a Pandora's box of hundreds of tangentially related queries, with implementations easily breaking 10k+ lines of unoptimized SQL in a single file."*
10. **Andrew Dodd · Thoughts on the repository pattern in Golang** — [adodd.net/post/go-ddd-repository-pattern](https://adodd.net/post/go-ddd-repository-pattern/). Cited for §5: *"These repository implementations do not really differ much from ORMs, and they are really too generic to be considered Repositories in the DDD sense, as they are more SQL-wrapper-tooling than business logic / application logic representations."*
11. **Three Dots Labs · The Repository pattern in Go** — [threedots.tech/post/repository-pattern-in-go](https://threedots.tech/post/repository-pattern-in-go/). Cited for §5.
12. **Ricardo Lüders · Demystifying Clean Architecture in Go** — [medium.com/@rluders/demystifying-clean-architecture-in-go-separating-fact-from-fiction-26fc8e81b99b](https://medium.com/@rluders/demystifying-clean-architecture-in-go-separating-fact-from-fiction-26fc8e81b99b). Cited for §1 framing.
13. **sklinkert/go-ddd** — [github.com/sklinkert/go-ddd](https://github.com/sklinkert/go-ddd). Reference implementation that pairs sqlc with DDD building blocks; cited for §5 (the codebase here is close to this template's spirit but uses hand-written row layers rather than sqlc — per the project's [memory/orm_research.md](../../.claude/projects/-Users-lileon-project-booking-monitor/memory/orm_research.md) sqlc was evaluated and deferred).
14. **Renaldi Purwanto · Is Clean Architecture the Right Choice for Your Golang Projects?** (July 2025) — [renaldid.medium.com/is-clean-architecture-the-right-choice-for-your-golang-projects-1d893c93db6f](https://renaldid.medium.com/is-clean-architecture-the-right-choice-for-your-golang-projects-1d893c93db6f). Cited for §1: balanced perspective on when Clean Architecture pays off in Go (complex business rules + long-term scalability + multiple delivery mechanisms).
15. **Pretix source — `items.py`** — [github.com/pretix/pretix/blob/master/src/pretix/base/models/items.py](https://github.com/pretix/pretix/blob/master/src/pretix/base/models/items.py). Cited at [docs/design/ticket_pricing.md](../design/ticket_pricing.md) §3 — the project's own KKTIX-vs-Pretix research. Pretix uses M:N Item/Quota; the project chose KKTIX-shape single-aggregate `TicketType` (`docs/architectural_backlog.md` § KKTIX-aligned).
16. **Project internal docs cited**:
    - [.claude/CLAUDE.md](../../.claude/CLAUDE.md) — bilingual contract, monitoring contract, current state through PR #111 (v1.0.0 closeout).
    - [docs/architectural_backlog.md](../architectural_backlog.md) — already tracks: legacy MarkCharging/MarkConfirmed cleanup (§ Legacy methods), Redis distributed lock decision (§ Cache-truth), KKTIX commitment (§ KKTIX-aligned ticket type model). Open: Money value object is **not** in the backlog — this review recommends adding it.
    - [docs/design/ticket_pricing.md](../design/ticket_pricing.md) — D4.1 price-snapshot design + Money deferral rationale.
    - [docs/PROJECT_SPEC.md](../PROJECT_SPEC.md) — full architectural history including the explicit "Full Clean Architecture" decision at PRs #30-#50.
    - Memory: [post_pr30_roadmap.md](../../.claude/projects/-Users-lileon-project-booking-monitor/memory/post_pr30_roadmap.md) — the 6-PR sequence that established the current architecture; this audit confirms PRs 31 → 35 landed as planned.

---

**End of report.**
