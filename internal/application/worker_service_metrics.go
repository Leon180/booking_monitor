package application

import (
	"context"
	"time"

	"booking_monitor/internal/domain"
)

// noopWorkerMetrics is a no-op domain.WorkerMetrics for use in tests.
type noopWorkerMetrics struct{}

func (n *noopWorkerMetrics) RecordOrderOutcome(_ string)              {}
func (n *noopWorkerMetrics) RecordProcessingDuration(_ time.Duration) {}
func (n *noopWorkerMetrics) RecordInventoryConflict()                 {}

// NoopWorkerMetrics returns a no-op domain.WorkerMetrics for use in tests.
func NoopWorkerMetrics() domain.WorkerMetrics { return &noopWorkerMetrics{} }

// workerServiceMetricsDecorator wraps WorkerService — reserved for future lifecycle metrics.
type workerServiceMetricsDecorator struct {
	next WorkerService
}

func NewWorkerServiceMetricsDecorator(next WorkerService) WorkerService {
	return &workerServiceMetricsDecorator{next: next}
}

func (d *workerServiceMetricsDecorator) Start(ctx context.Context) {
	d.next.Start(ctx)
}
