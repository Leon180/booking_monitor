package admin

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOrderLifecycleEvent_HappyPath(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
	}{
		{"created", EventTypeOrderCreated},
		{"paid", EventTypeOrderPaid},
		{"failed", EventTypeOrderFailed},
		{"expired", EventTypeOrderExpired},
		{"compensated", EventTypeOrderCompensated},
	}

	orderID := uuid.Must(uuid.NewV7())
	ttID := uuid.Must(uuid.NewV7())
	payload := OrderLifecyclePayload{
		OrderID:      orderID,
		UserID:       42,
		TicketTypeID: ttID,
		Quantity:     2,
		FromStatus:   "awaiting_payment",
		ToStatus:     "paid",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now().UTC()
			evt, err := NewOrderLifecycleEvent(tc.eventType, payload)
			after := time.Now().UTC()
			require.NoError(t, err)

			assert.NotEqual(t, uuid.Nil, evt.EventID, "EventID must be set")
			assert.Equal(t, tc.eventType, evt.EventType)
			assert.Equal(t, AdminEventSchemaVersion, evt.SchemaVersion)
			assert.True(t, !evt.OccurredAt.Before(before) && !evt.OccurredAt.After(after),
				"OccurredAt %v should be in [%v, %v]", evt.OccurredAt, before, after)

			var got OrderLifecyclePayload
			require.NoError(t, json.Unmarshal(evt.Data, &got))
			assert.Equal(t, payload, got)
		})
	}
}

func TestNewOrderLifecycleEvent_Invariants(t *testing.T) {
	validOrderID := uuid.Must(uuid.NewV7())
	validTTID := uuid.Must(uuid.NewV7())
	basePayload := OrderLifecyclePayload{
		OrderID:      validOrderID,
		TicketTypeID: validTTID,
		UserID:       1,
		Quantity:     1,
		ToStatus:     "reserved",
	}

	cases := []struct {
		name      string
		eventType string
		payload   OrderLifecyclePayload
		wantErr   error
	}{
		{
			name:      "wrong event type rejected",
			eventType: "not.a.real.event",
			payload:   basePayload,
			wantErr:   ErrInvalidEventType,
		},
		{
			name:      "saga event type rejected for lifecycle factory",
			eventType: EventTypeSagaTriggered,
			payload:   basePayload,
			wantErr:   ErrInvalidEventType,
		},
		{
			name:      "nil order_id rejected",
			eventType: EventTypeOrderCreated,
			payload: OrderLifecyclePayload{
				OrderID:      uuid.Nil,
				TicketTypeID: validTTID,
				ToStatus:     "reserved",
			},
			wantErr: ErrInvalidOrderID,
		},
		{
			name:      "nil ticket_type_id rejected",
			eventType: EventTypeOrderCreated,
			payload: OrderLifecyclePayload{
				OrderID:      validOrderID,
				TicketTypeID: uuid.Nil,
				ToStatus:     "reserved",
			},
			wantErr: ErrInvalidTicketTypeID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewOrderLifecycleEvent(tc.eventType, tc.payload)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.wantErr),
				"expected %v, got %v", tc.wantErr, err)
		})
	}
}

func TestNewSagaTriggeredEvent(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		evt, err := NewSagaTriggeredEvent(SagaTriggeredPayload{
			OrderID:        uuid.Must(uuid.NewV7()),
			CompensationID: "order:abc-123",
			Reason:         "payment_failed",
		})
		require.NoError(t, err)
		assert.Equal(t, EventTypeSagaTriggered, evt.EventType)

		var got SagaTriggeredPayload
		require.NoError(t, json.Unmarshal(evt.Data, &got))
		assert.Equal(t, "payment_failed", got.Reason)
	})

	t.Run("nil order_id rejected", func(t *testing.T) {
		_, err := NewSagaTriggeredEvent(SagaTriggeredPayload{
			OrderID: uuid.Nil,
			Reason:  "payment_failed",
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidOrderID))
	})

	t.Run("empty reason rejected", func(t *testing.T) {
		_, err := NewSagaTriggeredEvent(SagaTriggeredPayload{
			OrderID: uuid.Must(uuid.NewV7()),
			Reason:  "",
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyReason))
	})
}

func TestNewDLQReceivedEvent(t *testing.T) {
	t.Run("with order_id", func(t *testing.T) {
		evt, err := NewDLQReceivedEvent(DLQReceivedPayload{
			OrderID:  uuid.Must(uuid.NewV7()),
			Reason:   "unrecoverable_parse",
			Attempts: 3,
		})
		require.NoError(t, err)
		assert.Equal(t, EventTypeDLQReceived, evt.EventType)
	})

	t.Run("malformed message — nil order_id allowed", func(t *testing.T) {
		// DLQ malformed-parse case: original message couldn't yield order_id
		evt, err := NewDLQReceivedEvent(DLQReceivedPayload{
			OrderID:  uuid.Nil,
			Reason:   "malformed_parse: missing order_id field",
			Attempts: 1,
		})
		require.NoError(t, err, "nil order_id allowed for parse failures")

		var got DLQReceivedPayload
		require.NoError(t, json.Unmarshal(evt.Data, &got))
		assert.Equal(t, uuid.Nil, got.OrderID)
	})

	t.Run("empty reason rejected", func(t *testing.T) {
		_, err := NewDLQReceivedEvent(DLQReceivedPayload{
			OrderID: uuid.Must(uuid.NewV7()),
			Reason:  "",
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyReason))
	})

	t.Run("negative attempts rejected", func(t *testing.T) {
		_, err := NewDLQReceivedEvent(DLQReceivedPayload{
			OrderID:  uuid.Must(uuid.NewV7()),
			Reason:   "x",
			Attempts: -1,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attempts must be non-negative")
	})
}

func TestNewInventoryLowEvent(t *testing.T) {
	validTTID := uuid.Must(uuid.NewV7())

	t.Run("happy path", func(t *testing.T) {
		evt, err := NewInventoryLowEvent(InventoryLowPayload{
			TicketTypeID: validTTID,
			Available:    50,
			Total:        500,
			ThresholdPct: 0.10,
		})
		require.NoError(t, err)
		assert.Equal(t, EventTypeInventoryLow, evt.EventType)
	})

	t.Run("zero available allowed (sold out is alert-worthy)", func(t *testing.T) {
		_, err := NewInventoryLowEvent(InventoryLowPayload{
			TicketTypeID: validTTID,
			Available:    0,
			Total:        500,
			ThresholdPct: 0.10,
		})
		require.NoError(t, err)
	})

	t.Run("nil ticket_type_id rejected", func(t *testing.T) {
		_, err := NewInventoryLowEvent(InventoryLowPayload{
			TicketTypeID: uuid.Nil,
			Total:        500,
			ThresholdPct: 0.10,
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidTicketTypeID))
	})

	t.Run("non-positive total rejected", func(t *testing.T) {
		_, err := NewInventoryLowEvent(InventoryLowPayload{
			TicketTypeID: validTTID,
			Total:        0,
			ThresholdPct: 0.10,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "total must be positive")
	})

	t.Run("threshold out of range rejected", func(t *testing.T) {
		for _, bad := range []float64{-0.01, 1.01, 1.5} {
			_, err := NewInventoryLowEvent(InventoryLowPayload{
				TicketTypeID: validTTID,
				Total:        500,
				ThresholdPct: bad,
			})
			require.Error(t, err, "threshold %f should be rejected", bad)
			assert.Contains(t, err.Error(), "threshold_pct must be in [0, 1]")
		}
	})
}

// TestEventIDMonotonicity verifies UUIDv7 IDs increase in lexicographic
// order. Important because dashboards and dedup logic may compare IDs.
// This is a property of uuid.NewV7() but worth tripwire-testing in case
// the codebase swaps to a different ID scheme.
func TestEventIDMonotonicity(t *testing.T) {
	const n = 100
	ids := make([]uuid.UUID, n)
	for i := 0; i < n; i++ {
		evt, err := NewSagaTriggeredEvent(SagaTriggeredPayload{
			OrderID: uuid.Must(uuid.NewV7()),
			Reason:  "test",
		})
		require.NoError(t, err)
		ids[i] = evt.EventID
		// Tiny sleep so different milliseconds — v7 spec: ms-precision
		// timestamps; same-ms IDs are monotonic via seq but cross-ms is
		// what we want to test here.
		time.Sleep(time.Millisecond)
	}
	for i := 1; i < n; i++ {
		assert.True(t, ids[i].String() > ids[i-1].String(),
			"id %d (%s) should sort after id %d (%s)",
			i, ids[i], i-1, ids[i-1])
	}
}
