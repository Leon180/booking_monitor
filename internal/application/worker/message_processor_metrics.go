package worker

import (
	"context"
	"errors"
	"time"

	"booking_monitor/internal/domain"
)

// messageProcessorMetricsDecorator observes Process outcomes and emits
// the corresponding Metrics. Splitting this out of the base
// processor keeps the core processing flow free of observability
// noise and mirrors the pattern used by bookingServiceMetricsDecorator.
type messageProcessorMetricsDecorator struct {
	next    MessageProcessor
	metrics Metrics
}

// NewMessageProcessorMetricsDecorator wraps a MessageProcessor with
// outcome + duration metrics. Caller provides the metrics
// implementation (typically Prometheus-backed from infrastructure).
func NewMessageProcessorMetricsDecorator(next MessageProcessor, metrics Metrics) MessageProcessor {
	return &messageProcessorMetricsDecorator{next: next, metrics: metrics}
}

func (d *messageProcessorMetricsDecorator) Process(ctx context.Context, msg *QueuedBookingMessage) error {
	// Duration in defer so panics, early returns, and future refactors
	// that add new return paths cannot silently skip the histogram.
	start := time.Now()
	defer func() {
		d.metrics.RecordProcessingDuration(time.Since(start))
	}()

	err := d.next.Process(ctx, msg)

	switch {
	case err == nil:
		d.metrics.RecordOrderOutcome("success")
	case errors.Is(err, domain.ErrSoldOut),
		errors.Is(err, domain.ErrTicketTypeSoldOut):
		// Inventory conflict — Redis approved but DB disagreed.
		// Record both: the specific conflict signal (for drift dashboards)
		// and the outcome bucket (for throughput categorisation).
		//
		// Both sentinels classify here:
		//   - ErrSoldOut         legacy events.available_tickets path
		//   - ErrTicketTypeSoldOut  D4.1 follow-up event_ticket_types path
		// They are NOT wrapped versions of each other (errors.New
		// returns distinct values), so the explicit OR is required.
		// Without it the new D4.1 sold-out path falls through to
		// "db_error" and `inventory_conflict_total` flatlines at zero
		// — caught at multi-agent review (silent-failure-hunter G).
		d.metrics.RecordInventoryConflict()
		d.metrics.RecordOrderOutcome("sold_out")
	case errors.Is(err, domain.ErrUserAlreadyBought):
		d.metrics.RecordOrderOutcome("duplicate")
	case errors.Is(err, domain.ErrInvalidUserID),
		errors.Is(err, domain.ErrInvalidEventID),
		errors.Is(err, domain.ErrInvalidQuantity):
		// Malformed queue message — invariant violation caught by
		// NewOrder. Distinct from "db_error" because operators see
		// these as a permanent dead-letter signal (no amount of PEL
		// retry will heal a UserID=0 message), whereas "db_error"
		// implies "transient downstream issue, will recover".
		d.metrics.RecordOrderOutcome("malformed_message")
	default:
		d.metrics.RecordOrderOutcome("db_error")
	}

	return err
}
