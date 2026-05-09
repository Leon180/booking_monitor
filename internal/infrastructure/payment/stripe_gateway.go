package payment

// StripeGateway is the real Stripe SDK adapter — implements
// `domain.PaymentGateway` (both `PaymentIntentCreator` and
// `PaymentStatusReader`) over `github.com/stripe/stripe-go/v82`.
// Sibling to `MockGateway`; the fx provider in
// `cmd/booking-cli/server.go` selects between the two via the
// `PAYMENT_PROVIDER=mock|stripe` config switch (Slice 2a).
//
// Architectural posture (D4.2):
//   - Adapter holds a per-instance `*stripe.Client` configured via
//     `stripe.NewClient(apiKey, stripe.WithBackends(...))` — the
//     modern v82 builder pattern, NOT the legacy global-mutation
//     pattern (`stripe.SetBackends` etc.).
//   - `BackendConfig.LeveledLogger` injection (per-Backend) supplies
//     the redacting logger; we DO NOT touch `stripe.DefaultLeveledLogger`
//     — that global write is racy under `-race` and was dropped per
//     plan-review H1.
//   - `BackendConfig.MaxNetworkRetries` overrides the SDK's
//     `DefaultMaxNetworkRetries=2` const (configurable via
//     `STRIPE_MAX_NETWORK_RETRIES` env in Slice 2a).
//   - `Idempotency-Key` header set to `orderID.String()` for every
//     CreatePaymentIntent — Stripe's server-side idempotency cache
//     (24h) makes retries safe and matches the
//     `domain.PaymentIntentCreator` contract.
//   - Stripe API version pinning: pinned via go.mod (stripe-go v82.5.1
//     ships against a specific Stripe API version). Per-request
//     override not needed; documented in plan-review L2.
//
// PCI scope: this adapter never receives raw card data. Stripe Elements
// (D8 demo + future production frontend) collects card details client-
// side and submits them directly to Stripe's API; we only see
// `PaymentIntent.ID`, `PaymentIntent.client_secret`, and webhook events.
// Out of PCI DSS SAQ A scope per plan-review M2-CR.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	stripe "github.com/stripe/stripe-go/v82"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
)

// StripeMetrics is the per-call observability port — every Stripe
// API call inside `StripeGateway.{CreatePaymentIntent,GetStatus}`
// flows an `IncCall(op, outcome)` + `ObserveDuration(op, seconds)`
// pair through this interface, NOT through the global
// `internal/infrastructure/observability` Prometheus singletons. The
// adapter pattern matches `recon.Metrics` / `saga.CompensatorMetrics`
// — the Prometheus-backed implementation lives in
// `internal/bootstrap/payment_provider.go` (`NewPrometheusStripeMetrics`)
// and forwards into the prom vars in
// `internal/infrastructure/observability/metrics_stripe.go`.
//
// Tests substitute `NopStripeMetrics` (zero behaviour) or a
// captured-call spy. Slice 3a's tests rely on the spy variant.
//
// Outcome label vocabulary (must align with `metrics_init.go`
// pre-warm + `metrics_stripe.go` Help text):
//
//   - "success"        — call succeeded
//   - "declined"       — wrapped errors.Is(err, ErrPaymentDeclined)
//   - "transient"      — wrapped errors.Is(err, ErrPaymentTransient)
//   - "misconfigured"  — wrapped errors.Is(err, ErrPaymentMisconfigured)
//   - "invalid"        — wrapped errors.Is(err, ErrPaymentInvalid)
//
// Op label vocabulary:
//
//   - "create_payment_intent"  — POST /v1/payment_intents
//   - "get_status"             — GET  /v1/payment_intents/:id
type StripeMetrics interface {
	// IncCall increments the per-(op, outcome) counter.
	IncCall(op, outcome string)
	// ObserveDuration records the wall-clock duration of one call.
	ObserveDuration(op string, seconds float64)
}

// NopStripeMetrics is the zero-behaviour implementation. Mirrors the
// `NopMetrics` patterns in `recon` / `saga`. Used by tests + by
// `MockGateway`'s callers (the mock doesn't talk to Stripe so its
// callers never invoke a `StripeMetrics` method).
type NopStripeMetrics struct{}

func (NopStripeMetrics) IncCall(string, string)            {}
func (NopStripeMetrics) ObserveDuration(string, float64)   {}

// StripeConfig is the configuration injected into NewStripeGateway.
// Held separately from the global `Config` so this file doesn't pull
// the cleanenv struct as a dependency — the bootstrap layer translates
// from `config.Config.Payment.Stripe` to `StripeConfig` before
// constructing the adapter (Slice 2a wires this).
type StripeConfig struct {
	// APIKey is the Stripe secret key — accepts any of:
	//   sk_test_...  / sk_live_...  (full-account keys, dev only)
	//   rk_test_...  / rk_live_...  (restricted-scope keys, production)
	// Production deploys SHOULD use restricted-scope keys with
	// `payment_intent:read,write` scope only. The adapter doesn't
	// enforce key format — runbook + production policy are the
	// scope-enforcement layer.
	APIKey string

	// Timeout is the per-Stripe-call HTTP deadline. Stripe's docs
	// prescribe 30s; faster timeouts cause spurious failures during
	// their occasional 3-5s response time on PaymentIntent create.
	Timeout time.Duration

	// MaxNetworkRetries is the per-Backend retry budget. Stripe-go
	// retries 5xx + network errors with exponential backoff;
	// idempotency-key on POST makes retries safe. Default 2 (matches
	// stripe-go's `DefaultMaxNetworkRetries`).
	MaxNetworkRetries int64
}

// StripeGateway is the production-grade Stripe adapter.
type StripeGateway struct {
	client  *stripe.Client
	log     *mlog.Logger
	metrics StripeMetrics
}

// NewStripeGateway constructs a StripeGateway with production-grade
// defaults wired through `stripe.BackendConfig`:
//
//   - HTTPClient with the configured Timeout
//   - Redacting LeveledLogger (strips client_secret / card.* / PII)
//   - MaxNetworkRetries override
//
// Returns an error (NOT panic) on missing API key so the fx provider
// can surface it as a clean startup failure rather than a runtime
// panic. The fx graph in `cmd/booking-cli/server.go` (Slice 2a)
// upgrades this to fx.Shutdowner if the constructor fails.
func NewStripeGateway(cfg StripeConfig, logger *mlog.Logger, metrics StripeMetrics) (*StripeGateway, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("stripe: APIKey is required (set STRIPE_API_KEY)")
	}
	if metrics == nil {
		// Defensive: callers should always inject metrics (production
		// wires `prometheusStripeMetrics`; tests use `NopStripeMetrics`),
		// but a nil dep would panic on the first IncCall call. Default
		// to nop so a partial wiring slip surfaces as missing-metric
		// rather than nil-deref crash.
		metrics = NopStripeMetrics{}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second // Stripe-documented default
	}
	if cfg.Timeout > 5*time.Minute {
		// Sanity ceiling — a 5+ minute Stripe call holds a goroutine
		// way past anything operationally meaningful; almost certainly
		// a config typo (`STRIPE_TIMEOUT=300` interpreted as seconds
		// when meant as milliseconds).
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.MaxNetworkRetries < 0 {
		cfg.MaxNetworkRetries = stripe.DefaultMaxNetworkRetries
	}
	// Slice 1 review MED #4: clamp upper bound so a misconfigured
	// `STRIPE_MAX_NETWORK_RETRIES=9999` doesn't hold a recon goroutine
	// in a partially-failing Stripe endpoint for tens of minutes.
	// 10 retries with stripe-go's exponential backoff gives ~64s of
	// retry budget — well above transient-blip recovery, well below
	// "blocks the sweep loop" threshold.
	if cfg.MaxNetworkRetries > 10 {
		cfg.MaxNetworkRetries = 10
	}

	httpClient := &http.Client{Timeout: cfg.Timeout}

	// Redacting logger — implements stripe.LeveledLoggerInterface.
	// Strips known-sensitive fields from any structured log payload.
	// Wired ONLY via BackendConfig (per-Backend), NOT via
	// `stripe.DefaultLeveledLogger` global — see plan-review H1.
	scoped := logger.With(mlog.String("component", "stripe_gateway"))
	redacting := newRedactingLeveledLogger(scoped)

	backendCfg := &stripe.BackendConfig{
		HTTPClient:        httpClient,
		LeveledLogger:     redacting,
		MaxNetworkRetries: stripe.Int64(cfg.MaxNetworkRetries),
	}
	apiBackend := stripe.GetBackendWithConfig(stripe.APIBackend, backendCfg)
	uploadsBackend := stripe.GetBackendWithConfig(stripe.UploadsBackend, backendCfg)

	backends := &stripe.Backends{
		API:     apiBackend,
		Uploads: uploadsBackend,
		// Connect + MeterEvents fields left zero — we don't use Stripe
		// Connect (no marketplace flow) and don't use usage-based
		// billing (MeterEvents). Zero values are tolerated by the
		// stripe-go client; the relevant service fields just aren't
		// expected to be exercised.
	}

	client := stripe.NewClient(cfg.APIKey, stripe.WithBackends(backends))

	return &StripeGateway{
		client:  client,
		log:     scoped,
		metrics: metrics,
	}, nil
}

// CreatePaymentIntent — `domain.PaymentIntentCreator`. Maps to
// `POST /v1/payment_intents` with `Idempotency-Key` keyed on the
// orderID.
//
// Currency normalization: the parameter is `strings.ToLower(currency)`-
// converted before sending to Stripe. Stripe accepts both cases but
// canonical lowercase per their convention; the returned
// `PaymentIntent.Currency` is always lowercase. Callers comparing
// against the original `currency` argument should normalize on their
// side OR fetch from `PaymentIntent.Currency`. (Slice 1 review LOW #8.)
func (g *StripeGateway) CreatePaymentIntent(
	ctx context.Context, orderID uuid.UUID, amountCents int64,
	currency string, metadata map[string]string,
) (domain.PaymentIntent, error) {
	const op = "create_payment_intent"
	start := time.Now()
	// Always observe the duration histogram + emit one outcome
	// counter increment, exactly once per call. Defer + named-return-
	// like pattern using a closure variable so both branches (error
	// and success) participate without duplicating the call.
	//
	// Slice 2b review LOW #2: initialize to "transient" (a pre-warmed
	// label) rather than zero-value "". A panic between this defer
	// registration and the first explicit `outcome = ...` assignment
	// would otherwise emit `outcome=""`, breaking the pre-warm
	// guarantee (creates a new label that isn't in the alert's
	// pre-warmed series). "transient" matches `mapStripeError`'s
	// fall-through policy for unclassified errors — semantically
	// correct for "something panicked, treat as retryable".
	outcome := "transient"
	defer func() {
		g.metrics.ObserveDuration(op, time.Since(start).Seconds())
		g.metrics.IncCall(op, outcome)
	}()

	// Build params. AutomaticPaymentMethods.Enabled lets Stripe pick
	// the right payment method types (card, ACH, etc.) based on the
	// merchant's dashboard config — modern Stripe practice over
	// hardcoding `PaymentMethodTypes: ["card"]`.
	params := &stripe.PaymentIntentCreateParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(strings.ToLower(currency)),
		AutomaticPaymentMethods: &stripe.PaymentIntentCreateAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
		Metadata: metadata,
	}
	// Idempotency-Key: orderID. Stripe's server-side idempotency cache
	// (24h) makes repeat calls with the same orderID return the SAME
	// PaymentIntent — matches the `domain.PaymentIntentCreator`
	// contract.
	params.SetIdempotencyKey(orderID.String())
	params.Context = ctx

	pi, err := g.client.V1PaymentIntents.Create(ctx, params)
	if err != nil {
		mapped := mapStripeError(err)
		outcome = classifyOutcome(mapped)
		return domain.PaymentIntent{}, mapped
	}
	outcome = "success"

	return domain.PaymentIntent{
		ID:           pi.ID,
		ClientSecret: pi.ClientSecret,
		AmountCents:  pi.Amount,
		// stripe.Currency is a typed string in v82; convert via string()
		// so the domain doesn't pull stripe types upstream.
		Currency: string(pi.Currency),
		Metadata: pi.Metadata,
	}, nil
}

// GetStatus — `domain.PaymentStatusReader`. D4.2 changed the input
// from `orderID uuid.UUID` to `paymentIntentID string` — Stripe's
// API has no "get intent by metadata.order_id" cheap call.
//
// Empty intent ID is rejected with ErrPaymentInvalid. The reconciler
// guards against this BEFORE calling — the documented
// `SetPaymentIntentID` race in
// `internal/application/payment/service.go:166` can produce NULL
// intent IDs in `orders.payment_intent_id`. The recon null-guard
// (added in this PR) catches the row before reaching the gateway,
// so this branch is defense-in-depth.
func (g *StripeGateway) GetStatus(ctx context.Context, paymentIntentID string) (domain.ChargeStatus, error) {
	const op = "get_status"
	start := time.Now()
	// Slice 2b review LOW #2: same panic-safe init as
	// CreatePaymentIntent. "transient" is pre-warmed.
	outcome := "transient"
	defer func() {
		g.metrics.ObserveDuration(op, time.Since(start).Seconds())
		g.metrics.IncCall(op, outcome)
	}()

	if paymentIntentID == "" {
		outcome = "invalid"
		return domain.ChargeStatusUnknown, fmt.Errorf("stripe: empty payment intent ID: %w", domain.ErrPaymentInvalid)
	}

	pi, err := g.client.V1PaymentIntents.Retrieve(ctx, paymentIntentID, &stripe.PaymentIntentRetrieveParams{})
	if err != nil {
		mapped := mapStripeError(err)
		outcome = classifyOutcome(mapped)
		return domain.ChargeStatusUnknown, mapped
	}
	outcome = "success"

	return g.mapStripeStatusToCharge(pi.Status, paymentIntentID), nil
}

// classifyOutcome maps a wrapped error from `mapStripeError` to the
// `StripeAPICallsTotal` outcome label vocabulary. Pure function;
// uses `errors.Is` against the domain sentinels (which `mapStripeError`
// wires via `errors.Join` so this works even when the underlying
// `*stripe.Error` is also in the chain).
//
// Defaults to "transient" — same fall-through policy `mapStripeError`
// uses for unclassified Stripe error types. Keeps the metric label
// exhaustively populated even if a future error class slips past
// the sentinel-tagging logic.
//
// Known conflation (Slice 2b review INFO #4): `context.Canceled`
// from caller cancellation (recon max-age, request timeout) lands in
// the `errors.As(err, &serr) == false` branch of mapStripeError,
// gets wrapped as ErrPaymentTransient, and emits `outcome="transient"`
// here. That conflates "we cancelled the call" (expected during
// graceful shutdown / sweep timeout) with "Stripe returned 5xx"
// (real degradation). Alert tuning on
// `rate(stripe_api_calls_total{outcome="transient"}[5m]) > N`
// will false-positive on shutdown bursts. A dedicated `"cancelled"`
// outcome label would disambiguate; deferred until alert tuning
// surfaces a need.
func classifyOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, domain.ErrPaymentDeclined):
		return "declined"
	case errors.Is(err, domain.ErrPaymentMisconfigured):
		return "misconfigured"
	case errors.Is(err, domain.ErrPaymentInvalid):
		return "invalid"
	case errors.Is(err, domain.ErrPaymentTransient):
		return "transient"
	default:
		// Defensive — `mapStripeError`'s default branch wraps
		// unclassified errors as ErrPaymentTransient, so this case
		// shouldn't fire in practice. Matches the metric's
		// pre-warmed label set; never emits a fresh label that
		// would break alert evaluation.
		return "transient"
	}
}

// mapStripeStatusToCharge translates Stripe's PaymentIntent.Status
// enum to our domain.ChargeStatus. Plan §3 has the full mapping
// table; this implementation pins it.
//
// Pure function (no I/O); takes the gateway-injected logger only to
// surface unknown-future-status as a Warn log so operators can
// distinguish "stuck in known 3DS flow" from "stuck because Stripe
// added a status we don't handle yet" (Slice 1 review MED #3).
func (g *StripeGateway) mapStripeStatusToCharge(s stripe.PaymentIntentStatus, intentID string) domain.ChargeStatus {
	switch s {
	case stripe.PaymentIntentStatusSucceeded:
		// Money moved. Reconciler transitions Charging → Confirmed.
		return domain.ChargeStatusCharged
	case stripe.PaymentIntentStatusCanceled:
		// Cancelled by user / Stripe / our side. Reconciler treats as
		// declined → fail order → saga compensates.
		return domain.ChargeStatusDeclined
	case stripe.PaymentIntentStatusRequiresPaymentMethod,
		stripe.PaymentIntentStatusRequiresConfirmation:
		// Intent created but no payment attempted yet. Reconciler
		// treats as "no charge attempt" → fail order with "no gateway
		// record" reason. Functionally identical to NotFound from the
		// recon's perspective.
		return domain.ChargeStatusNotFound
	case stripe.PaymentIntentStatusRequiresAction,
		stripe.PaymentIntentStatusProcessing,
		stripe.PaymentIntentStatusRequiresCapture:
		// Stripe is still processing OR awaiting 3DS / SCA action OR
		// awaiting manual capture. Reconciler retries next sweep.
		return domain.ChargeStatusUnknown
	default:
		// Unknown future status — Stripe added a state in a later
		// API version that we don't have a mapping for. Surface as
		// Unknown (loud-by-default per the ChargeStatusUnknown
		// zero-value design); recon retries until max-age force-fail.
		// Slice 1 review MED #3: log Warn so operators can grep for
		// "unknown Stripe status" before the 24h max-age burns.
		g.log.Warn(context.Background(), "stripe: unknown PaymentIntent status — mapping to ChargeStatusUnknown; recon will retry until max-age",
			mlog.String("stripe_status", string(s)),
			mlog.String("intent_id", intentID),
		)
		return domain.ChargeStatusUnknown
	}
}

// mapStripeError translates a stripe-go error into a domain-typed
// error wrapped with one of the `ErrPayment*` sentinels so callers
// can branch via `errors.Is`. Plan §3 has the full mapping table;
// this implementation pins it.
//
// Uses `errors.Join(serr, sentinel)` so BOTH the original Stripe
// error (for `errors.As` code-level inspection) AND the domain
// sentinel (for `errors.Is` branch-on-class) are preserved in the
// chain — plan-review H3-SFH.
//
// Captures `serr.RequestID` in every wrapped error message for
// production support tickets — plan-review M5.
//
// Stripe-go v82 consolidated error types (the older ErrorTypeRateLimit /
// ErrorTypeAPIConnection / ErrorTypeAuthentication / ErrorTypePermission
// are gone — collapsed into ErrorTypeAPI). To distinguish 401/403 (auth
// — page-worthy) from 429 (rate-limit — retry) from 5xx (server —
// retry), we inspect HTTPStatusCode under ErrorTypeAPI. Plan-stage
// review verified ErrorType constants against v82.5.1 source —
// taxonomy is: ErrorTypeAPI, ErrorTypeCard, ErrorTypeIdempotency,
// ErrorTypeInvalidRequest, ErrorTypeTemporarySessionExpired.
func mapStripeError(err error) error {
	var serr *stripe.Error
	if !errors.As(err, &serr) {
		// Network / transport error — no Stripe-side request ID
		// because the request never reached Stripe.
		return fmt.Errorf("stripe: transport: %w", errors.Join(err, domain.ErrPaymentTransient))
	}
	reqID := serr.RequestID
	switch serr.Type {
	case stripe.ErrorTypeCard:
		// card_declined, expired_card, insufficient_funds. Surface
		// as 422 to client.
		return fmt.Errorf("stripe card error (code=%s req_id=%s): %w", serr.Code, reqID, errors.Join(serr, domain.ErrPaymentDeclined))
	case stripe.ErrorTypeIdempotency:
		// Same Idempotency-Key, different params. PROGRAMMER ERROR —
		// retrying with the same params loops forever (see plan-review
		// C2). Map to ErrPaymentInvalid (NON-retryable).
		return fmt.Errorf("stripe idempotency violation (orderID-key collision with different params; req_id=%s): %w", reqID, errors.Join(serr, domain.ErrPaymentInvalid))
	case stripe.ErrorTypeInvalidRequest:
		// 400 — bad amount, bad currency code, etc.
		return fmt.Errorf("stripe bad request (code=%s req_id=%s): %w", serr.Code, reqID, errors.Join(serr, domain.ErrPaymentInvalid))
	case stripe.ErrorTypeTemporarySessionExpired:
		// Stripe-side session that expired; transient, caller can retry.
		return fmt.Errorf("stripe temporary session expired (req_id=%s): %w", reqID, errors.Join(serr, domain.ErrPaymentTransient))
	case stripe.ErrorTypeAPI:
		// Stripe-go v82 collapsed RateLimit / APIConnection / Authentication /
		// Permission into this single ErrorTypeAPI. Use HTTPStatusCode to
		// disambiguate: 401/403 are page-worthy config errors;
		// 429 + 5xx are retryable.
		switch serr.HTTPStatusCode {
		case 0:
			// Slice 1 review HIGH #1: status=0 means stripe-go parsed
			// a *stripe.Error from a body that lacks an HTTP status
			// (SDK-level parse, transport edge case, or Stripe wrapping
			// changes). Distinct log message so operators can grep
			// for this path separately from genuine 5xx — avoids
			// silent misclassification as routine "transient blip".
			return fmt.Errorf("stripe error with no HTTP status (SDK-level parse or transport edge; code=%s req_id=%s): %w",
				serr.Code, reqID,
				errors.Join(serr, domain.ErrPaymentTransient))
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("stripe auth/permission error (status=%d code=%s req_id=%s): %w",
				serr.HTTPStatusCode, serr.Code, reqID,
				errors.Join(serr, domain.ErrPaymentMisconfigured))
		case http.StatusTooManyRequests:
			return fmt.Errorf("stripe rate-limited (status=%d req_id=%s): %w",
				serr.HTTPStatusCode, reqID,
				errors.Join(serr, domain.ErrPaymentTransient))
		default:
			// 5xx server error or other — transient.
			return fmt.Errorf("stripe api error (status=%d code=%s req_id=%s): %w",
				serr.HTTPStatusCode, serr.Code, reqID,
				errors.Join(serr, domain.ErrPaymentTransient))
		}
	default:
		// Future Stripe.ErrorType added in a later API version. Wrap
		// as ErrPaymentTransient (retryable) so `errors.Is(err,
		// ErrPaymentTransient)` succeeds at the caller; without this
		// wrap, callers' sentinel branches all fall through to
		// "unknown 500" — silent misclassification (plan-review
		// H3-SFH).
		return fmt.Errorf("stripe unclassified error type=%s (code=%s req_id=%s): %w", serr.Type, serr.Code, reqID, errors.Join(serr, domain.ErrPaymentTransient))
	}
}

// ─── redactingLeveledLogger ───────────────────────────────────────

// redactingLeveledLogger implements stripe.LeveledLoggerInterface.
// Strips known-sensitive substrings from any logged payload BEFORE
// forwarding to the underlying logger. Fields stripped:
//
//   - `client_secret=...` (Stripe Elements token; never log)
//   - `card.number=...`, `card.cvc=...`, `card.exp_month=...`
//     (defensive; we never receive raw card data but adapter belt-
//     and-suspenders against future logging changes in stripe-go)
//
// Plan-review M2-CR PCI scope statement: this adapter never receives
// raw card data, so the redactions are belt-and-suspenders. Out of
// PCI DSS SAQ A scope.
type redactingLeveledLogger struct {
	inner *mlog.Logger
}

func newRedactingLeveledLogger(inner *mlog.Logger) *redactingLeveledLogger {
	return &redactingLeveledLogger{inner: inner}
}

func (r *redactingLeveledLogger) Errorf(format string, v ...interface{}) {
	r.inner.Error(context.Background(), redact(fmt.Sprintf(format, v...)), mlog.String("source", "stripe_sdk"))
}

func (r *redactingLeveledLogger) Warnf(format string, v ...interface{}) {
	r.inner.Warn(context.Background(), redact(fmt.Sprintf(format, v...)), mlog.String("source", "stripe_sdk"))
}

func (r *redactingLeveledLogger) Infof(format string, v ...interface{}) {
	r.inner.Info(context.Background(), redact(fmt.Sprintf(format, v...)), mlog.String("source", "stripe_sdk"))
}

func (r *redactingLeveledLogger) Debugf(format string, v ...interface{}) {
	r.inner.Debug(context.Background(), redact(fmt.Sprintf(format, v...)), mlog.String("source", "stripe_sdk"))
}

// redact replaces sensitive substrings in a log line. Covers BOTH
// form-encoded (`key=value`) AND JSON-encoded (`"key":"value"` or
// `"key": "value"`) shapes. stripe-go v82 logs response bodies as
// JSON at Debug level — without the JSON-aware pass, sensitive
// fields would leak in plaintext (Slice 1 review HIGH #2).
//
// New sensitive fields added in future stripe-go versions need to
// be added to `sensitiveFields` to maintain redaction coverage —
// `TestStripeGateway_LogRedaction` (Slice 3a) verifies the known set
// for both encodings.
func redact(s string) string {
	for _, sub := range sensitiveFields {
		s = redactKeyValue(s, sub)
		s = redactJSONField(s, sub)
	}
	return s
}

var sensitiveFields = []string{
	"client_secret",
	"card.number",
	"card.cvc",
	"card.exp_month",
	"card.exp_year",
}

// redactKeyValue strips the value after a form-encoded `key=value`
// pair. Used for URL-form bodies and Stripe SDK debug logs that
// format params as `key1=value1&key2=value2`.
func redactKeyValue(s, key string) string {
	prefix := key + "="
	for {
		idx := strings.Index(s, prefix)
		if idx < 0 {
			return s
		}
		valueStart := idx + len(prefix)
		valueEnd := valueStart
		for valueEnd < len(s) {
			c := s[valueEnd]
			if c == ' ' || c == '\t' || c == '\n' || c == ',' || c == '"' || c == '&' {
				break
			}
			valueEnd++
		}
		s = s[:valueStart] + "<redacted>" + s[valueEnd:]
	}
}

// redactJSONField strips the value of a JSON-encoded `"key":"value"`
// pair. Tolerant of optional whitespace between the colon and the
// opening quote (`"key": "value"` is also valid JSON). Closes Slice 1
// review HIGH #2 — stripe-go v82 logs response bodies as JSON.
//
// Conservative matching: only handles string values, not numbers /
// arrays / nested objects. Sensitive fields in our scope are all
// string-typed (`client_secret`, `card.number` etc.) so this is
// sufficient. Future structured redactor (stripe-go's request_log
// field, for instance) is deferred until needed.
func redactJSONField(s, key string) string {
	for _, prefix := range []string{`"` + key + `":"`, `"` + key + `": "`} {
		for {
			idx := strings.Index(s, prefix)
			if idx < 0 {
				break
			}
			valueStart := idx + len(prefix)
			valueEnd := valueStart
			for valueEnd < len(s) && s[valueEnd] != '"' {
				if s[valueEnd] == '\\' && valueEnd+1 < len(s) {
					valueEnd += 2 // skip escaped char
					continue
				}
				valueEnd++
			}
			s = s[:valueStart] + "<redacted>" + s[valueEnd:]
		}
	}
	return s
}
