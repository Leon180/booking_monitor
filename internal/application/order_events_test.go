package application_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
)

func TestNewOrderCreatedEvent(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	orderID := uuid.New()
	eventID := uuid.New()
	o := domain.ReconstructOrder(orderID, 7, eventID, uuid.Nil, 3, domain.OrderStatusPending, created, time.Time{}, "", 0, "")

	got := application.NewOrderCreatedEvent(o)

	// Field-by-field — a swap (UserID/EventID) silently breaks the
	// payment consumer's behavior. This test is the seam contract.
	assert.Equal(t, orderID, got.OrderID, "OrderID maps from domain.Order.ID")
	assert.Equal(t, "pending", got.Status, "Status flattens enum to plain string")
	assert.Equal(t, 7, got.UserID, "UserID must come from domain.UserID, not EventID")
	assert.Equal(t, eventID, got.EventID, "EventID must come from domain.EventID, not UserID")
	assert.Equal(t, 3, got.Quantity)
	assert.Equal(t, float64(0), got.Amount, "Amount is 0 today — pre-existing semantic gap, see order_events.go comment")
	assert.Equal(t, created, got.CreatedAt)
	assert.Equal(t, application.OrderEventVersion, got.Version, "every produced event must carry the current schema version")
}

func TestNewOrderFailedEvent(t *testing.T) {
	t.Parallel()

	orderID := uuid.New()
	eventID := uuid.New()
	from := application.OrderCreatedEvent{
		OrderID:  orderID,
		UserID:   7,
		EventID:  eventID,
		Quantity: 3,
		Status:   "pending",
	}
	before := time.Now()

	got := application.NewOrderFailedEvent(from, "gateway timeout")

	after := time.Now()

	assert.Equal(t, orderID, got.OrderID)
	assert.Equal(t, 7, got.UserID)
	assert.Equal(t, eventID, got.EventID)
	assert.Equal(t, 3, got.Quantity)
	assert.Equal(t, "gateway timeout", got.Reason)
	assert.Equal(t, application.OrderEventVersion, got.Version)
	// FailedAt is set to time.Now() inside the mapper — bound it
	// between the test's before/after timestamps to confirm it isn't
	// taking the value from `from` (which has no FailedAt) or the
	// zero value.
	assert.True(t, !got.FailedAt.Before(before) && !got.FailedAt.After(after),
		"FailedAt must be set inside the mapper to time.Now()")
}

// TestOrderCreatedEvent_WireFormatStable pins the field-name contract
// of the JSON payload. Producer + consumer (in payment service)
// agree on these exact key names; renaming a field here without
// coordinating with the consumer would silently break message
// processing in production.
func TestOrderCreatedEvent_WireFormatStable(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	orderID := uuid.New()
	ev := application.NewOrderCreatedEvent(domain.ReconstructOrder(
		orderID, 7, uuid.New(), uuid.Nil, 3, domain.OrderStatusPending, created, time.Time{}, "", 0, "",
	))

	bytes, err := json.Marshal(ev)
	assert.NoError(t, err)

	// Round-trip into a generic map so we assert key names, not Go
	// field names.
	var wire map[string]any
	assert.NoError(t, json.Unmarshal(bytes, &wire))

	for _, key := range []string{"id", "status", "user_id", "event_id", "quantity", "amount", "created_at", "version"} {
		assert.Contains(t, wire, key, "wire-format key %q must be present", key)
	}
	// UUID serialises as a string under JSON key "id" — the payment
	// consumer's OrderCreatedEvent.OrderID has json:"id" and uses
	// uuid.UUID's UnmarshalJSON to parse.
	assert.Equal(t, orderID.String(), wire["id"], "OrderID must serialise under JSON key \"id\" as the canonical UUID string")
}

// TestOrderFailedEvent_WireFormatStable pins the field-name contract
// of the saga compensation payload.
func TestOrderFailedEvent_WireFormatStable(t *testing.T) {
	t.Parallel()

	ev := application.NewOrderFailedEvent(application.OrderCreatedEvent{
		OrderID: uuid.New(), UserID: 7, EventID: uuid.New(), Quantity: 3,
	}, "test reason")

	bytes, err := json.Marshal(ev)
	assert.NoError(t, err)

	var wire map[string]any
	assert.NoError(t, json.Unmarshal(bytes, &wire))

	for _, key := range []string{"event_id", "order_id", "user_id", "quantity", "failed_at", "reason", "version"} {
		assert.Contains(t, wire, key, "wire-format key %q must be present", key)
	}
}
