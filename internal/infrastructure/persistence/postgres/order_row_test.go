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
	o := domain.ReconstructOrder(id, 7, eventID, 3, domain.OrderStatusConfirmed, createdAt)

	got := orderRowFromDomain(o)

	// Field-by-field — a swap (UserID/EventID, status enum/raw)
	// silently ships a wrong row to the DB. This is the seam contract.
	assert.Equal(t, id, got.ID)
	assert.Equal(t, eventID, got.EventID, "EventID must come from domain.EventID, not UserID")
	assert.Equal(t, 7, got.UserID, "UserID must come from domain.UserID, not EventID")
	assert.Equal(t, 3, got.Quantity)
	assert.Equal(t, "confirmed", got.Status, "row.Status is raw string; OrderStatus enum is flattened")
	assert.Equal(t, createdAt, got.CreatedAt)
}

func TestOrderRow_ToDomain_RoundTrip(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	original := domain.ReconstructOrder(uuid.New(), 7, uuid.New(), 3, domain.OrderStatusPending, createdAt)

	roundTripped := orderRowFromDomain(original).toDomain()

	// Round-trip equality: every field that survives the row layer
	// must emerge unchanged. If a future row-shape change drops a
	// field (or coerces a type lossy), this test catches it.
	assert.Equal(t, original, roundTripped)
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
}
