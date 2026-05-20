package observability

// metrics_inventory_low.go closes a Layer A business-metric gap
// identified during the admin event streaming design review (see
// docs/design/admin_event_streaming.md § Q16).
//
// All other admin event types (order.*, saga.triggered, dlq.received)
// already have business counters elsewhere in the package:
//   - bookings_total              → metrics_booking.go
//   - payment_webhook_*           → metrics_payment_webhook.go
//   - expiry_sweep_resolved_total → metrics_expiry.go
//   - saga_compensator_*          → metrics_saga.go
//   - redis_dlq_routed_total      → metrics_redis_streams.go
//
// `inventory.low` had no corresponding counter; this file adds it.
//
// Cardinality: deliberately unlabeled. Labeling by ticket_type_id is
// rejected (potentially thousands of series); labeling by severity
// (warning/critical) is reserved for future use if multi-tier
// thresholds are added. Today there is a single threshold (10% default).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// InventoryLowAlertsTotal increments each time a ticket_type's
// available count crosses the configured low-stock threshold from
// above. This is an edge-trigger metric (transition, not state),
// matching how the corresponding admin event (`inventory.low`) is
// emitted.
//
// Alert recipe: rate(inventory_low_alerts_total[5m]) > 0 — any
// inventory-low signal during normal hours warrants attention.
var InventoryLowAlertsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "inventory_low_alerts_total",
		Help: "Times any ticket_type's available count crossed below the low-stock threshold (edge-triggered).",
	},
)
