package observability

// D5 payment-webhook metrics. The series here are the only signal
// ops have on whether the webhook surface is healthy — every alert in
// `deploy/prometheus/alerts.yml` for payment_webhook_* reads from
// these counters.
//
// Why a dedicated file: the surface has its own incident response
// (signature failures point at secret rotation; unknown_intent points
// at the SetPaymentIntentID race; intent_mismatch is single-event
// paging). Keeping the metrics declarations adjacent to one another
// makes the runbook ↔ counter mapping legible.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PaymentWebhookReceivedTotal — top-line outcome counter. Every event
// that reaches the application service (i.e. signature verified +
// body parsed) increments this exactly once with one of:
//
//   - "succeeded"         payment_intent.succeeded → MarkPaid
//   - "failed"            payment_intent.payment_failed → MarkPaymentFailed + saga
//   - "unsupported"       event type we don't dispatch (canceled / future types)
//   - "unexpected_status" order in non-Pattern-A status (legacy A4)
//   - "persist_failed"    UoW returned a non-recoverable error
//
// Hot-path dashboards read this as the throughput series; alerting
// reads more specific counters (signature_invalid_total etc.).
var PaymentWebhookReceivedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "payment_webhook_received_total",
		Help: "Total number of payment-provider webhook events received, labelled by service-layer result.",
	},
	[]string{"result"},
)

// PaymentWebhookSignatureInvalidTotal — verifier-side rejection
// counter. Sustained > 0 at any rate is page-worthy because either:
//
//   - secret rotation is half-done (legitimate provider sending a sig
//     against the new secret while we still hold the old, or vice
//     versa), OR
//   - someone is probing the endpoint
//
// `reason` mirrors `webhook.ClassifySignatureError`:
//
//   - "missing"       no Stripe-Signature header
//   - "malformed"     header present but unparseable
//   - "skew_exceeded" t=<unix> too far from our clock
//   - "mismatch"      HMAC didn't match any v1 candidate
//   - "config_error"  empty secret (config bug, NOT request-shape)
var PaymentWebhookSignatureInvalidTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "payment_webhook_signature_invalid_total",
		Help: "Total signature-verification failures, labelled by reason (missing / malformed / skew_exceeded / mismatch / config_error).",
	},
	[]string{"reason"},
)

// PaymentWebhookUnknownIntentTotal — fired when neither
// metadata.order_id nor payment_intent_id resolved an order.
//
//   - "not_found"           genuinely unknown — provider retries (500),
//                           operator must investigate. The rescue path
//                           for the SetPaymentIntentID race orphan
//                           lives here. Critical alert.
//   - "cross_env_livemode"  livemode mismatch (test webhook hitting
//                           prod listener or vice versa). 200 ACK,
//                           no retry. Soak-based alert if rate > 0
//                           sustained — could mean misconfigured
//                           dashboards.
var PaymentWebhookUnknownIntentTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "payment_webhook_unknown_intent_total",
		Help: "Total unresolved-intent events, labelled by reason (not_found / cross_env_livemode).",
	},
	[]string{"reason"},
)

// PaymentWebhookDuplicateTotal — fires when an inbound event hits an
// order already in a terminal state, OR when concurrent webhook
// redelivery races our own MarkPaid / MarkPaymentFailed and we fall
// through the ErrInvalidTransition re-read path.
//
// `prior_status` is the order's status at duplicate-detection time.
// Hot dashboard view: stack the prior-status values to see whether
// duplicates are mostly post-resolution (`paid`/`payment_failed` —
// healthy provider retry) or mid-flight (`awaiting_payment` —
// shouldn't happen since terminal isn't AwaitingPayment, but the
// label exists so a future regression surfaces).
var PaymentWebhookDuplicateTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "payment_webhook_duplicate_total",
		Help: "Total duplicate webhook deliveries against terminal orders, labelled by prior_status (paid / payment_failed / expired / compensated).",
	},
	[]string{"prior_status"},
)

// PaymentWebhookIntentMismatchTotal — single-event paging counter.
// Fires when a metadata-resolved order's persisted payment_intent_id
// disagrees with the webhook envelope's object.id. Could be an
// attacker forging metadata, a leaked test fixture in prod, or a
// real provider bug. The alert is `increase(...[5m]) > 0` —
// sole-event paging; humans MUST look at the order before any
// auto-resolution.
var PaymentWebhookIntentMismatchTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "payment_webhook_intent_mismatch_total",
		Help: "Total webhook events whose metadata.order_id resolved to an order with a different persisted payment_intent_id. Single-event paging.",
	},
)

// PaymentWebhookLateSuccessTotal — also single-event paging. Fires
// when a `succeeded` webhook arrives after the reservation TTL
// elapsed. Money moved at the provider but our reservation is dead;
// we walk the order to Expired + emit `order.failed` so saga reverts
// inventory, but a human MUST issue a refund. Every count = one
// manual refund ticket.
//
// `detected_at` shows which guard caught the late delivery:
//
//   - "service_check"  service-level pre-check (cheap, before UoW)
//   - "sql_predicate"  SQL `reserved_until > NOW()` predicate fired
//                      AFTER the service-level check passed (extremely
//                      narrow race — reservation lapsed mid-request)
//
// Dashboards stack the labels: a `service_check`-dominated profile
// means the gateway round-trip is slow / the reservation_window is
// too tight for the median Stripe Elements latency.
var PaymentWebhookLateSuccessTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "payment_webhook_late_success_total",
		Help: "Total succeeded webhooks against expired reservations (manual refund required). Labelled by where the late-arrival was detected.",
	},
	[]string{"detected_at"},
)

// PaymentWebhookUnsupportedTypeTotal — counts events whose `Type`
// discriminator we don't dispatch on. Today (D5) that's everything
// except payment_intent.succeeded + payment_intent.payment_failed.
// Stripe sends a long tail of types we may eventually want to handle
// (`payment_intent.canceled`, `charge.refunded`, etc.); this counter
// is the lighthouse showing which types are actually arriving.
var PaymentWebhookUnsupportedTypeTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "payment_webhook_unsupported_type_total",
		Help: "Total webhook events with a Type the dispatcher doesn't handle, labelled by event_type.",
	},
	[]string{"event_type"},
)
