package observability

// Worker-side metrics: order processing outcomes, processing duration,
// inventory conflicts, DLQ routing, Kafka consumer-retry signal, and
// saga poison-message counter. Adapter implementations live in
// worker_metrics.go + queue_metrics.go and forward into these vars.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// WorkerOrdersTotal tracks order processing outcomes in the worker.
// Labels: status = "success" | "sold_out" | "duplicate" | "db_error"
var WorkerOrdersTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "worker_orders_total",
		Help: "Total number of orders processed by the worker, by outcome",
	},
	[]string{"status"},
)

// WorkerProcessingDuration tracks how long the worker takes to process each message.
var WorkerProcessingDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "worker_processing_duration_seconds",
		Help:    "Duration of worker message processing in seconds",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	},
)

// InventoryConflictsTotal counts how often Redis approved but DB rejected (oversell prevention).
var InventoryConflictsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "inventory_conflicts_total",
		Help: "Total number of inventory conflicts (Redis approved, DB rejected)",
	},
)

// InventoryRehydrateDriftTotal counts events where app-startup rehydrate
// found Redis already had a key BUT with a value materially different
// from DB's available_tickets. Normal operation has Redis < DB by the
// in-flight count (Lua deducts that haven't reached the worker yet);
// a sustained > 0 rate, or a single >>0 spike, signals corruption,
// manual tinkering, or a NOGROUP-aftermath inconsistency.
//
// Read this metric WITH `inventory_conflicts_total` — if both fire,
// the system is reading drifted state. PR-D will add a continuous
// drift reconciler; this counter is the early signal until then.
var InventoryRehydrateDriftTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "inventory_rehydrate_drift_total",
		Help: "Total events where startup rehydrate detected Redis-vs-DB drift (Redis key exists, value differs).",
	},
)

// DLQMessagesTotal counts messages routed to a dead-letter queue by
// topic and reason. Labels:
//   topic  = "order.failed.dlq"  // post-D7 the only Kafka DLQ topic; pre-D7 also "order.created.dlq"
//   reason = "invalid_payload" | "invalid_event" | "max_retries"
var DLQMessagesTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "dlq_messages_total",
		Help: "Total number of messages written to a dead-letter queue",
	},
	[]string{"topic", "reason"},
)

// SagaPoisonMessagesTotal counts saga events that exceeded the
// compensator retry budget and were dead-lettered.
var SagaPoisonMessagesTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "saga_poison_messages_total",
		Help: "Total number of saga events dead-lettered after max retries",
	},
)

// KafkaConsumerRetryTotal counts Kafka messages that failed to
// process transiently and were left UNCOMMITTED for Kafka rebalance
// to re-deliver. This is the "silent retry" surface: if this
// counter's rate stays high, it means downstream infra (DB / Redis
// / payment gateway) is degraded and consumers are in a stuck-but-
// not-dead state. Labels:
//   topic  = original Kafka topic name
//   reason = "transient_processing_error" for now; future budget
//            implementation will add "retry_budget_exceeded" etc.
//
// Paired with the `KafkaConsumerStuck` Prometheus alert which
// fires when `rate(kafka_consumer_retry_total[5m]) > 1` for 2m.
var KafkaConsumerRetryTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kafka_consumer_retry_total",
		Help: "Total Kafka messages left uncommitted for rebalance-based retry due to transient errors",
	},
	[]string{"topic", "reason"},
)
