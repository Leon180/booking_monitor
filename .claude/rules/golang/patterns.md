---
paths:
  - "**/*.go"
  - "**/go.mod"
  - "**/go.sum"
---
# Go Patterns

> This file extends [common/patterns.md](../common/patterns.md) with Go specific content.

## Functional Options

```go
type Option func(*Server)

func WithPort(port int) Option {
    return func(s *Server) { s.port = port }
}

func NewServer(opts ...Option) *Server {
    s := &Server{port: 8080}
    for _, opt := range opts {
        opt(s)
    }
    return s
}
```

## Small Interfaces

Define interfaces where they are used, not where they are implemented.

## Dependency Injection

Use constructor functions to inject dependencies:

```go
func NewUserService(repo UserRepository, logger Logger) *UserService {
    return &UserService{repo: repo, logger: logger}
}
```

## Entity Factory + Reconstructor

For domain entities (under `internal/domain/`), pair every entity
with three things: a `NewX` factory, a `ReconstructX` rehydration
helper, and immutable `WithX` transition methods.

```go
// internal/domain/order.go

var (
    ErrInvalidUserID   = errors.New("order user_id must be positive")
    ErrInvalidEventID  = errors.New("order event_id must be positive")
    ErrInvalidQuantity = errors.New("order quantity must be positive")
)

// NewOrder validates invariants and returns a fresh Pending order.
// ID is caller-supplied (typically `uuid.NewV7()` at the API
// boundary) so the same id flows handler → queue → worker → DB.
// CreatedAt is factory-assigned; never DB-assigned.
func NewOrder(id uuid.UUID, userID, eventID, quantity int) (Order, error) {
    if id == uuid.Nil { return Order{}, ErrInvalidOrderID }
    if userID <= 0    { return Order{}, ErrInvalidUserID }
    if eventID <= 0   { return Order{}, ErrInvalidEventID }
    if quantity <= 0  { return Order{}, ErrInvalidQuantity }
    return Order{
        ID: id, UserID: userID, EventID: eventID, Quantity: quantity,
        Status: OrderStatusPending, CreatedAt: time.Now(),
    }, nil
}

// ReconstructOrder rehydrates from a persisted row. Skips validation
// because the row was already validated at insert time. Use ONLY
// from repository scan code.
func ReconstructOrder(id, userID, eventID, quantity int, status OrderStatus, createdAt time.Time) Order {
    return Order{ID: id, UserID: userID, EventID: eventID, Quantity: quantity, Status: status, CreatedAt: createdAt}
}

// WithStatus is an immutable transition — value receiver, returns a
// new Order. The original is never mutated.
func (o Order) WithStatus(s OrderStatus) Order {
    o.Status = s
    return o
}
```

For events / messages with a small set of well-known types, pair the
type constant with a typed factory so callers don't spell the wire
string at all:

```go
const EventTypeOrderFailed = "order.failed"
const OutboxStatusPending  = "PENDING"

func NewOrderFailedOutbox(payload []byte) (OutboxEvent, error) {
    id, err := uuid.NewV7()
    if err != nil {
        return OutboxEvent{}, fmt.Errorf("generate outbox event id: %w", err)
    }
    return OutboxEvent{
        ID:        id,
        EventType: EventTypeOrderFailed,
        Payload:   payload,
        Status:    OutboxStatusPending,
    }, nil
}
```

Application-layer call sites become:

```go
order, err := domain.NewOrder(msg.OrderID, msg.UserID, msg.EventID, msg.Quantity)
if err != nil { return err }   // DLQ via worker classifier
// ... persist via repo

outboxEvent, err := domain.NewOrderFailedOutbox(payload)
if err != nil { return err }
if _, err := s.outboxRepo.Create(ctx, outboxEvent); err != nil { return err }
```

— no `&domain.Order{...}` literals, no inline `"order.failed"`
string, no possibility of a typo at the call site.

## Reference

See skill: `golang-patterns` for comprehensive Go patterns including concurrency, error handling, and package organization.
