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

// stage5IntakeRevertFailuresTotal counts Stage 5 CONSUMER-side revert
// failures: terminal-error processing required reverting the leaked
// Redis qty (Lua deducted at publish time, worker UoW rolled back),
// and RevertInventory itself failed. Distinct counter from
// stage5_kafka_publish_failures_total because the failure modes have
// different operational signatures:
//
//   - publish-failures fire BEFORE the 202 is returned — the user
//     sees a 5xx and the operator gets a paged "durability gate down".
//   - intake-revert-failures fire AFTER the 202 — the user already
//     has the order_id; the failure is fully internal and only the
//     drift reconciler will eventually pick it up.
//
// Alert recipe: `rate(stage5_intake_revert_failures_total[5m]) > 0`
// for 2m → page operator. Pairs with the drift reconciler's
// inventory_drift_* series as the upstream signal that drift WILL
// appear in the next sweep (closes the observability gap that a
// pure-logs failure path created).
var stage5IntakeRevertFailuresTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "stage5_intake_revert_failures_total",
		Help: "Stage 5 consumer-side terminal-error revert failures (leaked Redis qty pending drift-reconciler recovery)",
	},
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

// RecordIntakeRevertFailure increments the consumer-side revert
// failure counter. See the var-level docstring for the operational
// rationale + alert recipe.
func (prometheusStage5Metrics) RecordIntakeRevertFailure() {
	stage5IntakeRevertFailuresTotal.Inc()
}
