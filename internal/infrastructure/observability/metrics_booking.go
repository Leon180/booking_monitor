package observability

// Business-facing metrics emitted by the API layer (booking endpoints).
// Adapter implementations live alongside in this package
// (booking_metrics.go) so the application layer never imports
// `prometheus/*` directly.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PageViewsTotal tracks users entering the page for conversion rate calculation.
var PageViewsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "page_views_total",
		Help: "Total number of page views to measure funnel conversion",
	},
	[]string{"page"},
)

// BookingsTotal tracks booking outcomes at the API layer.
// Labels: status = "success" | "sold_out" | "error"
// (PR #129 A15 dropped "duplicate" — since N4 the Idempotency
// middleware short-circuits duplicates before they reach the
// booking service, so the decorator never emits status="duplicate".
// Replay activity is observable via idempotency_replays_total.)
var BookingsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "bookings_total",
		Help: "Total number of booking attempts by outcome",
	},
	[]string{"status"},
)
