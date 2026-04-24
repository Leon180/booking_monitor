package application

import "context"

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
