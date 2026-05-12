package messaging

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/application/booking"
)

// encodeIntakeMessage serialises a booking.IntakeMessage to the bytes
// that land in Kafka's value. Extracted as a pure function so the
// round-trip can be unit-tested without spinning up Kafka.
func encodeIntakeMessage(m booking.IntakeMessage) ([]byte, error) {
	return json.Marshal(m)
}

// DecodeIntakeMessage is the inverse of encodeIntakeMessage. Used by
// the Stage 5 intake consumer when processing a fetched Kafka
// message.
//
// Validates that the order_id round-tripped cleanly — a zero UUID
// would indicate either a serialisation bug or a producer that
// violated the "never publish with zero order_id" guard in
// IntakePublisher.PublishIntake.
func DecodeIntakeMessage(b []byte) (booking.IntakeMessage, error) {
	var m booking.IntakeMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return booking.IntakeMessage{}, fmt.Errorf("intake message: unmarshal: %w", err)
	}
	if m.OrderID == uuid.Nil {
		return booking.IntakeMessage{}, fmt.Errorf("intake message: zero order_id after decode")
	}
	return m, nil
}
