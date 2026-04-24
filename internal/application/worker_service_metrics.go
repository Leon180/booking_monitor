package application

import (
	"context"
	"time"
)

// noopWorkerMetrics is a no-op WorkerMetrics for use in tests.
type noopWorkerMetrics struct{}

func (n *noopWorkerMetrics) RecordOrderOutcome(_ string)              {}
func (n *noopWorkerMetrics) RecordProcessingDuration(_ time.Duration) {}
func (n *noopWorkerMetrics) RecordInventoryConflict()                 {}

// NoopWorkerMetrics returns a no-op WorkerMetrics for use in tests.
func NoopWorkerMetrics() WorkerMetrics { return &noopWorkerMetrics{} }

// workerServiceMetricsDecorator wraps WorkerService — reserved for future lifecycle metrics.
type workerServiceMetricsDecorator struct {
	next WorkerService
}

func NewWorkerServiceMetricsDecorator(next WorkerService) WorkerService {
	return &workerServiceMetricsDecorator{next: next}
}

func (d *workerServiceMetricsDecorator) Start(ctx context.Context) error {
	return d.next.Start(ctx)
}
