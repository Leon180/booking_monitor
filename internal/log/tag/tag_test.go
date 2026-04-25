package tag_test

import (
	"errors"
	"testing"

	"booking_monitor/internal/log/tag"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
)

// Each tag helper must return a Field whose Key matches the canonical
// log key name used in the codebase. A table-driven test ensures any
// future rename is caught immediately.
func TestTagKeys(t *testing.T) {
	sampleUUID := uuid.New()
	cases := []struct {
		name    string
		wantKey string
		got     string
	}{
		{"OrderID", "order_id", tag.OrderID(sampleUUID).Key},
		{"EventID", "event_id", tag.EventID(sampleUUID).Key},
		{"UserID", "user_id", tag.UserID(1).Key},
		{"MsgID", "msg_id", tag.MsgID("x").Key},
		{"LockID", "lock_id", tag.LockID(1).Key},
		{"Topic", "topic", tag.Topic("t").Key},
		{"Partition", "partition", tag.Partition(0).Key},
		{"Offset", "offset", tag.Offset(0).Key},
		{"Quantity", "quantity", tag.Quantity(1).Key},
		{"Amount", "amount", tag.Amount(1.0).Key},
		{"Status", "status", tag.Status("x").Key},
		{"Error", "error", tag.Error(errors.New("boom")).Key},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantKey, tc.got)
		})
	}
}

func TestTag_Types(t *testing.T) {
	// Spot-check the zapcore.FieldType for a few constructors to make
	// sure ints / strings / errors are encoded as the right field type.
	// OrderID/EventID are now uuid.UUID, encoded via zap.Stringer (the
	// type-tag is StringerType, marshalled as a string at write time).
	assert.Equal(t, zapcore.StringerType, tag.OrderID(uuid.New()).Type)
	assert.Equal(t, zapcore.StringerType, tag.EventID(uuid.New()).Type)
	assert.Equal(t, zapcore.Int64Type, tag.UserID(7).Type)
	assert.Equal(t, zapcore.StringType, tag.Status("paid").Type)
	assert.Equal(t, zapcore.Int64Type, tag.Offset(100).Type)
	assert.Equal(t, zapcore.Float64Type, tag.Amount(3.14).Type)
	assert.Equal(t, zapcore.ErrorType, tag.Error(errors.New("x")).Type)
}
