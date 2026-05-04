package postgres

import (
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestOrderRow_FromDomain_AllFieldsCopied(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	id := uuid.New()
	eventID := uuid.New()
	o := domain.ReconstructOrder(id, 7, eventID, 3, domain.OrderStatusConfirmed, createdAt, time.Time{}, "")

	got := orderRowFromDomain(o)

	// Field-by-field — a swap (UserID/EventID, status enum/raw)
	// silently ships a wrong row to the DB. This is the seam contract.
	assert.Equal(t, id, got.ID)
	assert.Equal(t, eventID, got.EventID, "EventID must come from domain.EventID, not UserID")
	assert.Equal(t, 7, got.UserID, "UserID must come from domain.UserID, not EventID")
	assert.Equal(t, 3, got.Quantity)
	assert.Equal(t, "confirmed", got.Status, "row.Status is raw string; OrderStatus enum is flattened")
	assert.Equal(t, createdAt, got.CreatedAt)
	assert.False(t, got.ReservedUntil.Valid, "legacy order (zero reservedUntil) must map to NULL ReservedUntil column")
}

func TestOrderRow_FromDomain_PatternA_ReservedUntilCopied(t *testing.T) {
	t.Parallel()

	// Pattern A reservation: a non-zero reservedUntil must round-trip
	// to a Valid sql.NullTime so the INSERT writes the actual TTL.
	createdAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	reservedUntil := createdAt.Add(15 * time.Minute)
	o := domain.ReconstructOrder(uuid.New(), 7, uuid.New(), 1, domain.OrderStatusAwaitingPayment, createdAt, reservedUntil, "")

	got := orderRowFromDomain(o)

	assert.True(t, got.ReservedUntil.Valid, "Pattern A reservation must produce a Valid sql.NullTime")
	assert.Equal(t, reservedUntil, got.ReservedUntil.Time)
}

func TestOrderRow_ToDomain_RoundTrip(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	original := domain.ReconstructOrder(uuid.New(), 7, uuid.New(), 3, domain.OrderStatusPending, createdAt, time.Time{}, "")

	roundTripped := orderRowFromDomain(original).toDomain()

	// Round-trip equality: every field that survives the row layer
	// must emerge unchanged. If a future row-shape change drops a
	// field (or coerces a type lossy), this test catches it.
	assert.Equal(t, original, roundTripped)
}

func TestOrderRow_ToDomain_PatternA_RoundTrip(t *testing.T) {
	t.Parallel()

	// Same shape as the legacy round-trip, but with a non-zero
	// reservedUntil. Pins that the NULL <-> non-NULL mapping is
	// symmetric across the row layer.
	createdAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	reservedUntil := createdAt.Add(15 * time.Minute)
	original := domain.ReconstructOrder(uuid.New(), 7, uuid.New(), 2, domain.OrderStatusAwaitingPayment, createdAt, reservedUntil, "")

	roundTripped := orderRowFromDomain(original).toDomain()

	assert.Equal(t, original, roundTripped)
	assert.Equal(t, reservedUntil, roundTripped.ReservedUntil())
}

func TestOrderRow_ToDomain_StatusEnumRehydrated(t *testing.T) {
	t.Parallel()

	// Row stores Status as raw string (DB column type). toDomain must
	// re-type it back to OrderStatus so domain consumers still get the
	// enum semantics.
	row := orderRow{
		ID:        uuid.New(),
		EventID:   uuid.New(),
		UserID:    1,
		Quantity:  1,
		Status:    "compensated",
		CreatedAt: time.Now(),
	}
	got := row.toDomain()

	assert.Equal(t, domain.OrderStatusCompensated, got.Status())
	assert.IsType(t, domain.OrderStatus(""), got.Status(), "Status must rehydrate as domain.OrderStatus, not raw string")
	assert.True(t, got.ReservedUntil().IsZero(),
		"row with NULL ReservedUntil (Valid=false) must rehydrate to a zero-value time.Time on the domain side")
}
