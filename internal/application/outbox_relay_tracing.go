package application

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// outboxTracerName scopes outbox spans to their own subsystem so a
// future tracer-config change can target outbox specifically. Local
// const because CP2.6 booking subpackage move otherwise removes the
// `tracerName` symbol that this file used to share with booking
// decorators. When outbox itself moves to `application/outbox/` later
// in CP2.6 this const moves with it.
const outboxTracerName = "application/outbox"

// OutboxRelayTracingDecorator wraps OutboxRelay.processBatch with an OTEL span.
// It follows the Decorator Pattern used throughout this codebase (see booking_service_tracing.go).
type OutboxRelayTracingDecorator struct {
	next *OutboxRelay
}

// NewOutboxRelayTracingDecorator wraps an OutboxRelay with tracing.
func NewOutboxRelayTracingDecorator(next *OutboxRelay) *OutboxRelayTracingDecorator {
	return &OutboxRelayTracingDecorator{next: next}
}

// Run delegates to the inner relay's Run loop. Tracing is applied per-batch inside processBatch.
func (d *OutboxRelayTracingDecorator) Run(ctx context.Context) {
	d.next.runWithBatchHook(ctx, d.processBatchTraced)
}

// processBatchTraced wraps a single processBatch call with an OTEL span
// and records batch errors via span.RecordError + span.SetStatus so
// Jaeger reflects failures instead of always showing OK.
func (d *OutboxRelayTracingDecorator) processBatchTraced(ctx context.Context) error {
	// No attributes: batch size is logged, not traced, to avoid high-cardinality spans.
	ctx, span := otel.Tracer(outboxTracerName).Start(ctx, "OutboxRelay.processBatch")
	defer span.End()

	err := d.next.processBatch(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
