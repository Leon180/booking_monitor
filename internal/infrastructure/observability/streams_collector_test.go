package observability

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// streamIDAgeMillis is unexported; this file lives in package
// observability (NOT observability_test) so the test can reach it.
// Tests cover pure-function behaviour — no Redis dependency.

func TestStreamIDAgeMillis_Recent(t *testing.T) {
	t.Parallel()

	// Synthesize a stream ID 250ms ago.
	pastMs := time.Now().Add(-250 * time.Millisecond).UnixMilli()
	id := strconv.FormatInt(pastMs, 10) + "-0"

	age, ok := streamIDAgeMillis(id)
	assert.True(t, ok, "valid ID must parse")
	// Age is "now - pastMs" measured at test time; tolerate scheduler
	// jitter by checking a band rather than an exact value.
	assert.GreaterOrEqual(t, age, int64(200))
	assert.LessOrEqual(t, age, int64(2000), "test scheduler jitter shouldn't exceed 1.75s")
}

func TestStreamIDAgeMillis_FutureIsClampedToZero(t *testing.T) {
	t.Parallel()

	// ID timestamped in the future — clock skew between Redis host
	// and the collector host would produce this. Must clamp to 0,
	// NOT emit a negative gauge value.
	futureMs := time.Now().Add(10 * time.Second).UnixMilli()
	id := strconv.FormatInt(futureMs, 10) + "-0"

	age, ok := streamIDAgeMillis(id)
	assert.True(t, ok, "future ID still parses")
	assert.Equal(t, int64(0), age, "future timestamp must clamp to 0, not negative")
}

func TestStreamIDAgeMillis_MalformedReturnsFalse(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",                 // empty
		"-0",               // no ms part
		"abc-0",            // non-numeric ms
		"1700000000000",    // missing dash + seq (incomplete ID)
		"-1700000000000-0", // negative ms (rejected)
		"0-0",              // zero ms (rejected — definitionally invalid)
	}
	for _, id := range tests {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			age, ok := streamIDAgeMillis(id)
			assert.False(t, ok, "malformed ID %q must return ok=false", id)
			assert.Equal(t, int64(0), age, "malformed ID must return zero age")
		})
	}
}

// TestStreamIDAgeMillis_RealRedisShape pins the parser against the
// actual stream-ID format Redis emits. Stream IDs are
// "<ms>-<seq>" where seq is the per-millisecond sequence number.
// This test guards against a future regression where someone tries
// to parse the seq portion as also-significant.
func TestStreamIDAgeMillis_IgnoresSequencePart(t *testing.T) {
	t.Parallel()

	pastMs := time.Now().Add(-100 * time.Millisecond).UnixMilli()
	id1 := strconv.FormatInt(pastMs, 10) + "-0"
	id2 := strconv.FormatInt(pastMs, 10) + "-99999"

	age1, ok1 := streamIDAgeMillis(id1)
	age2, ok2 := streamIDAgeMillis(id2)

	assert.True(t, ok1)
	assert.True(t, ok2)
	// Same ms part = same age regardless of seq.
	assert.Equal(t, age1, age2,
		"sequence portion of stream ID must NOT affect computed age — it represents per-ms ordering, not time")
	// Sanity: both are valid ms-since-epoch parsings.
	assert.True(t, strings.Contains(id1, "-"))
}
