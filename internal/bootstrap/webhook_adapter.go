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
func (prometheusWebhookMetrics) UnsupportedType(eventType string) {
	observability.PaymentWebhookUnsupportedTypeTotal.WithLabelValues(eventType).Inc()
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
