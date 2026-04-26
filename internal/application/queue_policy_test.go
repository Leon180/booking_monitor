package application_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
)

// TestDefaultOrderRetryPolicy pins the contract that
// redis_queue.processWithRetry depends on:
//   - malformed-input errors → policy returns false (don't retry)
//   - everything else        → policy returns true  (burn the budget)
//
// If this test fails after a domain change (e.g. a new ErrInvalid*
// sentinel that's not classified as malformed), the worker's DLQ
// fast-path silently regresses to "burn 600ms of backoff before DLQ".
func TestDefaultOrderRetryPolicy(t *testing.T) {
	policy := application.DefaultOrderRetryPolicy()

	tests := []struct {
		name string
		err  error
		want bool // expected return value (true = retry)
	}{
		{name: "ErrInvalidUserID skips retry", err: domain.ErrInvalidUserID, want: false},
		{name: "ErrInvalidEventID skips retry", err: domain.ErrInvalidEventID, want: false},
		{name: "ErrInvalidQuantity skips retry", err: domain.ErrInvalidQuantity, want: false},
		{name: "wrapped malformed sentinel still skips retry", err: fmt.Errorf("upstream: %w", domain.ErrInvalidUserID), want: false},
		{name: "ErrSoldOut retries (transient inventory conflict)", err: domain.ErrSoldOut, want: true},
		{name: "ErrUserAlreadyBought retries (DB constraint race)", err: domain.ErrUserAlreadyBought, want: true},
		{name: "generic db error retries", err: errors.New("conn reset by peer"), want: true},
		{name: "nil error retries (no-op — handler returned success but caller still asks)", err: nil, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}
