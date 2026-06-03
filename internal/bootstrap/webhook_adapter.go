package bootstrap

import (
	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/infrastructure/api/webhook"
	"booking_monitor/internal/infrastructure/observability"
)

// prometheusWebhookMetrics implements payment.WebhookMetrics by
// forwarding each call to the global Prometheus counters declared in
// `internal/infrastructure/observability/metrics_payment_webhook.go`.
//
// Same boundary-translation pattern as prometheusSagaMetrics /
// prometheusReconMetrics — keeps the application layer free of any
// `prometheus.*` import while letting the production wiring use the
// shared observability registry.
type prometheusWebhookMetrics struct{}

// NewPrometheusWebhookMetrics is the fx-friendly constructor.
func NewPrometheusWebhookMetrics() payment.WebhookMetrics {
	return prometheusWebhookMetrics{}
}

func (prometheusWebhookMetrics) Received(result string) {
	observability.PaymentWebhookReceivedTotal.WithLabelValues(result).Inc()
}
func (prometheusWebhookMetrics) Duplicate(priorStatus string) {
	observability.PaymentWebhookDuplicateTotal.WithLabelValues(priorStatus).Inc()
}
func (prometheusWebhookMetrics) UnknownIntent(reason string) {
	observability.PaymentWebhookUnknownIntentTotal.WithLabelValues(reason).Inc()
}
func (prometheusWebhookMetrics) IntentMismatch() {
	observability.PaymentWebhookIntentMismatchTotal.Inc()
}
func (prometheusWebhookMetrics) LateSuccess(detectedAt string) {
	observability.PaymentWebhookLateSuccessTotal.WithLabelValues(detectedAt).Inc()
}
// knownWebhookUnsupportedTypes bounds the cardinality of the
// payment_webhook_unsupported_type_total metric's event_type label.
// PR #129 A15: pre-A15 the label was passed verbatim from the
// Stripe envelope, which means a forged or future-Stripe event
// type could create unbounded label values — Prometheus cardinality
// scales with the unique label-value count, so a single bad client
// could 10×-100× the series count on this counter alone.
//
// The allowed set is "Stripe payment surface event types we have
// historically seen but explicitly do NOT handle". Anything outside
// this set collapses to "other"; the metric still records the
// occurrence but the cardinality stays bounded.
var knownWebhookUnsupportedTypes = map[string]struct{}{
	// Documented out-of-scope per webhook_service.go scope comment.
	"payment_intent.canceled":               {},
	"payment_intent.requires_action":        {},
	"payment_intent.processing":             {},
	"payment_intent.amount_capturable_updated": {},
	// Charge-level events some Stripe account configurations emit.
	"charge.succeeded": {},
	"charge.failed":    {},
	"charge.refunded":  {},
	"charge.captured":  {},
	"charge.updated":   {},
	// Refund + dispute flow events.
	"refund.created":        {},
	"refund.failed":         {},
	"charge.dispute.created": {},
}

func safeWebhookUnsupportedLabel(eventType string) string {
	if _, ok := knownWebhookUnsupportedTypes[eventType]; ok {
		return eventType
	}
	return "other"
}

func (prometheusWebhookMetrics) UnsupportedType(eventType string) {
	observability.PaymentWebhookUnsupportedTypeTotal.WithLabelValues(safeWebhookUnsupportedLabel(eventType)).Inc()
}

// prometheusWebhookHandlerMetrics implements webhook.HandlerMetrics.
// The handler-side counters cover signature verification (which fires
// BEFORE the application service ever runs) and body-parse failures.
type prometheusWebhookHandlerMetrics struct{}

// NewPrometheusWebhookHandlerMetrics is the fx-friendly constructor.
func NewPrometheusWebhookHandlerMetrics() webhook.HandlerMetrics {
	return prometheusWebhookHandlerMetrics{}
}

func (prometheusWebhookHandlerMetrics) SignatureInvalid(reason string) {
	observability.PaymentWebhookSignatureInvalidTotal.WithLabelValues(reason).Inc()
}
func (prometheusWebhookHandlerMetrics) BodyMalformed() {
	observability.PaymentWebhookReceivedTotal.WithLabelValues("malformed").Inc()
}
