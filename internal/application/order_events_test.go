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

// D7 (2026-05-08) deleted OrderCreatedEvent + the
// NewOrderFailedEvent(from OrderCreatedEvent, reason) factory along
// with the legacy A4 auto-charge path. The remaining wire contract
// covered by this file is OrderFailedEvent and its
// `NewOrderFailedEventFromOrder(domain.Order, reason)` factory.

func TestNewOrderFailedEventFromOrder(t *testing.T) {
	t.Parallel()

	orderID := uuid.New()
	eventID := uuid.New()
	ticketTypeID := uuid.New()
	o := domain.ReconstructOrder(orderID, 7, eventID, ticketTypeID, 3,
		domain.OrderStatusFailed, time.Now(), time.Time{}, "", 0, "")

	before := time.Now()
	got := application.NewOrderFailedEventFromOrder(o, "gateway timeout")
	after := time.Now()

	assert.Equal(t, orderID, got.OrderID, "OrderID maps from domain.Order.ID")
	assert.Equal(t, 7, got.UserID, "UserID must come from domain.UserID, not EventID")
	assert.Equal(t, eventID, got.EventID, "EventID must come from domain.EventID, not UserID")
	assert.Equal(t, ticketTypeID, got.TicketTypeID, "TicketTypeID required for D4.1+ saga compensator IncrementTicket routing")
	assert.Equal(t, 3, got.Quantity)
	assert.Equal(t, "gateway timeout", got.Reason)
	assert.Equal(t, application.OrderEventVersion, got.Version, "every produced event must carry the current schema version")
	// FailedAt is set to time.Now() inside the mapper.
	assert.True(t, !got.FailedAt.Before(before) && !got.FailedAt.After(after),
		"FailedAt must be set inside the mapper to time.Now()")
}

// TestOrderFailedEvent_WireFormatStable pins the field-name contract
// of the saga compensation payload — the consumer (saga compensator)
// agrees on these exact key names; renaming without coordination
// would silently break message processing in production.
func TestOrderFailedEvent_WireFormatStable(t *testing.T) {
	t.Parallel()

	o := domain.ReconstructOrder(uuid.New(), 7, uuid.New(), uuid.New(), 3,
		domain.OrderStatusFailed, time.Now(), time.Time{}, "", 0, "")
	ev := application.NewOrderFailedEventFromOrder(o, "test reason")

	bytes, err := json.Marshal(ev)
	assert.NoError(t, err)

	var wire map[string]any
	assert.NoError(t, json.Unmarshal(bytes, &wire))

	for _, key := range []string{"event_id", "order_id", "user_id", "ticket_type_id", "quantity", "failed_at", "reason", "version"} {
		assert.Contains(t, wire, key, "wire-format key %q must be present", key)
	}
}
