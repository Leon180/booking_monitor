package observability

// A4 Reconciler metrics. The application-side `recon.Metrics` interface
// + `bootstrap.NewPrometheusReconMetrics()` adapter forward into these
// vars; recon application code never imports this package directly
// (CP2 layer-fix).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ReconResolvedTotal counts orders the reconciler resolved per sweep,
// labelled by outcome:
//
//	"charged"           — gateway said charged, MarkConfirmed succeeded
//	"declined"          — gateway said declined, MarkFailed succeeded
//	"not_found"         — gateway has no record, transitioned to Failed
//	"unknown"           — gateway returned an unclassified verdict, skipped (will retry)
//	"max_age_exceeded"  — Charging older than RECON_MAX_CHARGING_AGE, force-failed
//	"transition_lost"   — Mark{Confirmed,Failed} returned ErrInvalidTransition
//	                      (the row moved between FindStuckCharging and Mark*) — benign,
//	                      counted so a sustained rate can be alerted on as a
//	                      design-assumption violation
//
// "transient_error" is NOT a value here — that goes to ReconGatewayErrorsTotal
// because it tracks infrastructure failures (network, gateway 5xx) which
// are operationally distinct from per-order verdicts.
var ReconResolvedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "recon_resolved_total",
		Help: "Total orders resolved by the reconciler per sweep, by outcome",
	},
	[]string{"outcome"},
)

// ReconGatewayErrorsTotal increments on infrastructure failures during
// gateway.GetStatus — network errors, gateway 5xx, ctx timeout. The
// reconciler treats these as transient (retry next sweep) but emits
// the counter so a sustained rate is alertable. Distinct from
// ReconResolvedTotal{outcome="unknown"} which is a successful gateway
// call that returned an unclassifiable verdict.
var ReconGatewayErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_gateway_errors_total",
		Help: "Total gateway.GetStatus infrastructure failures (network, 5xx, timeout)",
	},
)

// ReconMarkErrorsTotal increments when MarkConfirmed / MarkFailed
// returns a non-ErrInvalidTransition error during reconciler resolve
// — i.e., a real DB failure (connection lost, deadlock, integrity
// constraint). ErrInvalidTransition itself is benign (race-loss to
// the worker; counted as `transition_lost` outcome instead).
//
// Without this counter, sustained DB failures during the resolve
// path would only surface in logs — no alert can fire on log lines.
var ReconMarkErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_mark_errors_total",
		Help: "Total Mark{Confirmed,Failed} DB failures during recon resolve (excludes ErrInvalidTransition)",
	},
)

// ReconFindStuckErrorsTotal fires when the per-sweep
// FindStuckCharging query itself errors (DB outage, missing index
// from a forgotten migration, query timeout). Without this counter,
// a sustained sweep-query failure shows nothing on dashboards
// EXCEPT a stale `recon_stuck_charging_orders` gauge — operators
// can't distinguish "orders are stuck" from "recon itself is broken".
//
// Pairs with the alert: `rate(recon_find_stuck_errors_total[5m]) > 0`
// fires immediately on any sustained failure.
var ReconFindStuckErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_find_stuck_errors_total",
		Help: "Total FindStuckCharging query failures (DB outage, missing index, timeout)",
	},
)

// ReconNullIntentIDSkippedTotal fires when the reconciler encounters
// a stuck-charging row with `payment_intent_id IS NULL` — caused by
// the documented `SetPaymentIntentID` race in
// `internal/application/payment/service.go:166` (gateway-side intent
// create succeeded, follow-on UPDATE failed). The reconciler skips
// the row (cannot call Stripe.GetStatus without an intent ID; calling
// with empty would produce a silent wrong-verdict — see
// `stripe_gateway.go` GetStatus null-guard).
//
// Pairs with the alert: `rate(recon_null_intent_id_skipped_total[5m]) > 0`
// fires immediately. Triage: search Stripe dashboard by
// `metadata.order_id` for the orphan PaymentIntent + reconcile manually.
//
// D4.2.
var ReconNullIntentIDSkippedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_null_intent_id_skipped_total",
		Help: "Stuck-charging rows skipped because payment_intent_id IS NULL (documented SetPaymentIntentID race; manual reconcile via Stripe dashboard)",
	},
)

// ReconStuckChargingOrders is a gauge of orders currently in Charging
// state older than RECON_CHARGING_THRESHOLD. Reset to the latest count
// on every sweep. Pairs with the alert
// `stuck_charging_orders > 0 for 5m` — sustained value = recon falling
// behind or systemic gateway degradation.
var ReconStuckChargingOrders = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "recon_stuck_charging_orders",
		Help: "Charging orders older than the threshold, set on each reconciler sweep",
	},
)

// ReconResolveDurationSeconds is a histogram of how long an
// individual stuck order took to resolve (FindStuckCharging time of
// detection → final Mark{X} commit). Drives the
// `recon_charging_resolve_age_seconds` p50/p95/p99 dashboard which
// is the primary signal for tuning RECON_CHARGING_THRESHOLD: if p95
// regularly exceeds your threshold, your threshold is too aggressive
// and you're stealing in-flight orders from the worker.
//
// Note this is NOT the order's age in Charging; that's
// `recon_resolve_age_seconds`. This is the resolve operation's
// own duration.
var ReconResolveDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "recon_resolve_duration_seconds",
		Help:    "Wall-clock duration of a single stuck-order resolve operation",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)

// ReconResolveAgeSeconds is a histogram of the AGE of orders at
// resolve time (NOW() - updated_at when we picked them up). The key
// signal for tuning RECON_CHARGING_THRESHOLD downward: if p50 is
// 130s and threshold is 120s, recon is consistently catching orders
// 10s after they cross — which means orders are in Charging for
// 130s+ before resolving, suggesting a slow gateway. Tighten or
// add an upstream alert.
var ReconResolveAgeSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "recon_resolve_age_seconds",
		Help:    "Age of stuck orders at the moment the reconciler resolves them",
		Buckets: []float64{30, 60, 90, 120, 180, 300, 600, 1800, 3600, 21600, 86400}, // 30s … 24h
	},
)

// ReconGatewayDurationSeconds is a histogram of gateway.GetStatus
// latency observed by the reconciler. Drives RECON_GATEWAY_TIMEOUT
// tuning: p99 should be well below the timeout. A long tail indicates
// a slow gateway, which probably also affects the worker's Charge
// path.
var ReconGatewayDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "recon_gateway_get_status_duration_seconds",
		Help:    "Wall-clock duration of gateway.GetStatus calls from the reconciler",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)
