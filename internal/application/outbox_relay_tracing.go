package application

import (
	"context"

	"go.opentelemetry.io/otel"
)

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

// processBatchTraced wraps a single processBatch call with an OTEL span.
func (d *OutboxRelayTracingDecorator) processBatchTraced(ctx context.Context) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "OutboxRelay.processBatch") // No attributes here — batch size is logged, not traced, to avoid high-cardinality spans.

	defer span.End()

	d.next.processBatch(ctx)
}
