package observability

// Stage 5 (durable Kafka intake) specific metrics. Only exercised by
// cmd/booking-cli-stage5/; other stage binaries register them globally
// via promauto but never increment them, which is fine — Prometheus
// scrapers see a flat zero series.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"booking_monitor/internal/application/booking"
)

// stage5KafkaPublishFailuresTotal tracks Kafka publish failures on
// the Stage 5 hot path with the compensation outcome as a label.
//
// Labels:
//
//	reverted = "true"   — publish failed, RevertInventory succeeded
//	                       (clean compensation — Redis qty restored)
//	reverted = "false"  — publish failed AND RevertInventory failed
//	                       (drift — inventory permanently leaked,
//	                       requires the drift reconciler / manual ops)
//
// Why log.Error + counter both: log gives human-readable correlation,
// counter gives the rate-of-failure signal Prometheus alerts can fire
// on. A sustained Kafka broker degradation will show as a steady
// rate increase here — the page is the single best signal for "Stage 5
// durability gate is broken".
//
// Unexported: callers go through prometheusStage5Metrics (booking.Stage5Metrics
// interface) so the application layer never imports prometheus.
var stage5KafkaPublishFailuresTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "stage5_kafka_publish_failures_total",
		Help: "Stage 5 hot-path Kafka publish failures by compensation outcome (label: reverted=true|false)",
	},
	[]string{"reverted"},
)

// prometheusStage5Metrics implements booking.Stage5Metrics by writing
// to the prometheus counters declared above. Mirrors the
// prometheusBookingMetrics pattern in booking_metrics.go so the
// application layer ports stay framework-agnostic.
type prometheusStage5Metrics struct{}

// NewStage5Metrics constructs the prometheus-backed implementation.
func NewStage5Metrics() booking.Stage5Metrics {
	return prometheusStage5Metrics{}
}

// RecordPublishFailure increments the publish-failure counter with
// the appropriate `reverted` label.
func (prometheusStage5Metrics) RecordPublishFailure(reverted bool) {
	label := "false"
	if reverted {
		label = "true"
	}
	stage5KafkaPublishFailuresTotal.WithLabelValues(label).Inc()
}
