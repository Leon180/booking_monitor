package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	App            AppConfig            `yaml:"app"`
	Server         ServerConfig         `yaml:"server"`
	Redis          RedisConfig          `yaml:"redis"`
	Worker         WorkerConfig         `yaml:"worker"`
	Postgres       PostgresConfig       `yaml:"postgres"`
	Kafka          KafkaConfig          `yaml:"kafka"`
	Recon          ReconConfig          `yaml:"recon"`
	Saga           SagaConfig           `yaml:"saga"`
	InventoryDrift InventoryDriftConfig `yaml:"inventory_drift"`
	Booking        BookingConfig        `yaml:"booking"`
	Payment        PaymentConfig        `yaml:"payment"`
	Expiry         ExpiryConfig         `yaml:"expiry"`
}

// BookingConfig holds the tunables for `BookingService.BookTicket` —
// the Pattern A reservation entry point. Per-event TTL override
// (events.reservation_window_seconds, added in 000012) lands in D8
// when admin section CRUD is wired; until then this single value
// applies to every event.
type BookingConfig struct {
	// ReservationWindow is how long a Pattern A reservation stays
	// valid before the D6 expiry sweeper flips it to Expired and
	// reverts the Redis-side inventory deduct.
	//
	// Default 15 minutes mirrors Stripe Checkout / KKTIX / event-
	// ticketing-industry norms — long enough that a real customer can
	// finish a Stripe Elements flow (median ~2-3 min) without timing
	// out, short enough that abandoned carts don't sit on inventory
	// during a sold-out flash sale (the worst case is 15 min × peak-
	// concurrent bookings worth of "soft-locked" stock).
	//
	// Lower bound: must comfortably exceed the slowest expected
	// Stripe Elements interaction (failure scenarios: 3DS challenge,
	// retry after declined card, resubmit on validation error). 5 min
	// is the practical floor; below that, false expiries hurt UX more
	// than they protect inventory.
	//
	// Upper bound: must stay below the per-event sold-out window so
	// inventory doesn't soft-lock for a meaningfully long share of the
	// sale. For a 30-second flash sale, 15 min would lock all stock
	// for the duration of the sale window — plan to override per-event
	// once D8 lands.
	ReservationWindow time.Duration `yaml:"reservation_window" env:"BOOKING_RESERVATION_WINDOW" env-default:"15m"`

	// D4.1 NOTE: pre-D4.1 BookingConfig also held
	// `DefaultTicketPriceCents` + `DefaultCurrency` — every payment
	// used a single global default. D4.1 retired both: prices live on
	// the `event_ticket_types` table (KKTIX 票種 model), are picked by
	// `BookingService.BookTicket` from the chosen ticket_type at book
	// time, and are snapshotted onto `orders.amount_cents` /
	// `orders.currency`. `PaymentService.CreatePaymentIntent` then
	// reads them off the order via `order.AmountCents()` /
	// `order.Currency()`. The customer pays exactly what they were
	// quoted, even if the merchant edits the ticket_type's price
	// mid-checkout. See `docs/design/ticket_pricing.md` for the
	// schema-level rationale.
	//
	// `BOOKING_DEFAULT_TICKET_PRICE_CENTS` and `BOOKING_DEFAULT_CURRENCY`
	// env vars are no longer read. cleanenv ignores unknown env vars
	// silently, but `LoadConfig` calls `checkDeprecatedEnv` after
	// parsing to log a stderr warning if either is still set in the
	// process environment — turning the silent misconfig into a loud
	// signal for operators migrating from pre-D4.1 deployment configs.
}

// ReconConfig holds the tunables for the `recon` subcommand — the
// reconciler that sweeps stuck-Charging orders and resolves them via
// gateway.GetStatus. See internal/application/recon for the loop;
// see PROJECT_SPEC §A4 for the design.
//
// EVERY default below was chosen heuristically — none are backed by
// production data from this codebase yet. Adjust after the first
// production-shaped k6 run produces histograms for:
//
//	recon_charging_resolve_age_seconds — actual age at resolve time
//	recon_gateway_get_status_duration_seconds — actual GetStatus latency
//
// The defaults are deliberately CONSERVATIVE (longer thresholds, fewer
// givings-up) so the system tends toward correctness while we collect
// data.
type ReconConfig struct {
	// SweepInterval is how often the reconciler loop fires when running
	// in default loop mode. Each tick scans for stuck-Charging orders
	// and resolves them.
	//
	// Default 120s rationale: industry standard is "2× provider charge
	// timeout" (Stripe charge timeout is 30s; 2× = 60s); we add another
	// 60s for clock skew between payment-worker and DB. If GetStatus
	// is fast (sub-second) and the gateway is reliable, 120s gives
	// stuck orders ~2 minutes to resolve naturally before recon
	// intervenes — far below SLA-impacting territory.
	//
	// Lower if production data shows recon resolves orders that are
	// significantly older than 120s (lost time-to-recovery). Higher
	// if MockGateway/Stripe call duration trends toward minutes.
	SweepInterval time.Duration `yaml:"sweep_interval" env:"RECON_SWEEP_INTERVAL" env-default:"120s"`

	// ChargingThreshold is the minimum age a Charging order must have
	// before recon considers it "stuck" and queries the gateway. Set
	// HIGHER than the typical successful Pending→Charging→Confirmed
	// duration to avoid stealing orders mid-flight from the worker.
	//
	// Default 120s rationale: same as SweepInterval — typical Stripe
	// p99 charge duration is well under 60s; 120s ensures we never
	// race a normal in-flight Charge call.
	ChargingThreshold time.Duration `yaml:"charging_threshold" env:"RECON_CHARGING_THRESHOLD" env-default:"120s"`

	// GatewayTimeout bounds the per-order GetStatus call. Without a
	// budget, a hung gateway pins the whole sweep loop and blocks
	// SIGTERM-driven shutdown indefinitely.
	//
	// Default 10s rationale: GET /charges/:id at Stripe is a cached,
	// indexed read — typical p99 is well under 1s. 10s gives 10×
	// headroom while keeping the worst case within k8s CronJob
	// terminationGracePeriodSeconds (default 30s).
	GatewayTimeout time.Duration `yaml:"gateway_timeout" env:"RECON_GATEWAY_TIMEOUT" env-default:"10s"`

	// MaxChargingAge is the give-up cutoff. A Charging order older
	// than this is force-transitioned to Failed with reason
	// "max_charging_age_exceeded" and a Prometheus counter increment.
	// Without this, a permanently-Unknown order sweeps forever and
	// the stuck-Charging alert fires with no actionable remediation.
	//
	// Default 24h rationale: payment providers (Stripe, PayPal)
	// typically resolve disputes/manual-review within 24-72h; 24h
	// is short enough that operators are alerted before a customer
	// follow-up window, long enough to absorb provider-side multi-day
	// review states. Tune up to 72h if false-positive give-ups appear.
	MaxChargingAge time.Duration `yaml:"max_charging_age" env:"RECON_MAX_CHARGING_AGE" env-default:"24h"`

	// BatchSize is the maximum number of orders the reconciler resolves
	// per sweep cycle. Caps memory + DB transaction count when a
	// large queue accumulates (e.g., gateway outage drained).
	//
	// Default 100 rationale: at 1 order / ~50ms (one GetStatus + one
	// MarkX), 100 orders = ~5 seconds per sweep. Bounded well below
	// SweepInterval so the next tick can fire on schedule. Bump if
	// production data shows persistent backlog growth.
	BatchSize int `yaml:"batch_size" env:"RECON_BATCH_SIZE" env-default:"100"`
}

// PaymentConfig holds the tunables for the D4 `/pay` + D5 webhook
// surfaces + (D4.2) the real Stripe SDK adapter switch.
//
// Provider selects which `domain.PaymentGateway` adapter the fx graph
// wires:
//   - "mock"    — `internal/infrastructure/payment/mock_gateway.go`
//                 (default for dev / CI / tests; "always succeed" semantics)
//   - "stripe"  — `internal/infrastructure/payment/stripe_gateway.go`
//                 (production; talks to api.stripe.com via stripe-go v82)
//
// Production-mode validation REQUIRES `Provider == "stripe"` plus a
// non-empty `Stripe.APIKey` and `Stripe.WebhookSecret` (or the legacy
// `WebhookSecret` falls through with a deprecation warning).
type PaymentConfig struct {
	// Provider selects the gateway adapter (D4.2). Default "mock"
	// preserves backward compatibility — existing `make demo-up` /
	// integration tests / CI continue using `MockGateway` without
	// requiring a Stripe account.
	Provider string `yaml:"provider" env:"PAYMENT_PROVIDER" env-default:"mock"`

	// Stripe is the configuration sub-tree for the Stripe SDK adapter
	// (D4.2). All fields are required ONLY when `Provider == "stripe"`.
	Stripe StripePaymentConfig `yaml:"stripe"`

	// WebhookSecret is the HMAC-SHA256 signing secret the provider
	// shares with us. Source of truth: the provider's dashboard
	// (Stripe: developer settings → webhooks → signing secret;
	// real-world string starts with `whsec_`). For local mock /
	// integration tests, anything non-empty works as long as the
	// mock signer + the verifier read the same value.
	//
	// D4.2 backward-compat note: when `Provider == "stripe"`, the
	// canonical env var is `STRIPE_WEBHOOK_SECRET` (Stripe.WebhookSecret).
	// This `PAYMENT_WEBHOOK_SECRET` field is kept for backward compat
	// with pre-D4.2 deploys; if STRIPE_WEBHOOK_SECRET is empty AND
	// PAYMENT_WEBHOOK_SECRET is set, Validate() fills the former from
	// the latter and emits a deprecation warning to stderr.
	//
	// HARD RULE: empty value MUST cause startup failure when
	// Provider != "mock" (see `application/payment/webhook_service.go`
	// consumer + the VerifySignature config-error branch). Silently
	// accepting any signature against an empty key is a forgery vector.
	WebhookSecret string `yaml:"webhook_secret" env:"PAYMENT_WEBHOOK_SECRET"`

	// WebhookReplayTolerance bounds how far the `t=<unix>` in the
	// signature header can drift from our wall clock before we 401
	// the request. 5 minutes mirrors Stripe's documented default;
	// tighter is safer (smaller replay window for a captured
	// signature) but risks rejecting legitimate retries from a
	// provider whose clock drifted. Lower for tests so verifier-
	// behaviour assertions don't have to wait wall-clock seconds.
	WebhookReplayTolerance time.Duration `yaml:"webhook_replay_tolerance" env:"PAYMENT_WEBHOOK_REPLAY_TOLERANCE" env-default:"5m"`

	// WebhookExpectedLiveMode tells the WebhookService whether this
	// deployment expects livemode=true events (production with live
	// keys) or livemode=false (test / staging). Mismatched envelopes
	// are 200-no-op'd with `unknown_intent{cross_env_livemode}` so a
	// misrouted test webhook can't error-storm a prod listener.
	WebhookExpectedLiveMode bool `yaml:"webhook_expected_live_mode" env:"PAYMENT_WEBHOOK_EXPECTED_LIVE_MODE" env-default:"false"`

	// WebhookLoopbackURL is the URL the testapi handler POSTs back
	// into for the mock-confirm flow (full-pipeline simulation of a
	// real provider's webhook delivery). Defaults to localhost on
	// the configured server port — override for tests / containers.
	// Only consulted when `Server.EnableTestEndpoints` is true.
	WebhookLoopbackURL string `yaml:"webhook_loopback_url" env:"PAYMENT_WEBHOOK_LOOPBACK_URL" env-default:"http://127.0.0.1:8080/webhook/payment"`
}

// StripePaymentConfig holds the configuration tree for the real Stripe
// SDK adapter (D4.2). All fields are required ONLY when
// `Payment.Provider == "stripe"`. Production-mode validation rejects
// missing APIKey / WebhookSecret + test keys without explicit opt-in
// via `STRIPE_ALLOW_TEST_KEY`.
//
// Sub-config (not flattened into PaymentConfig) so the YAML namespace
// is unambiguous (`payment.stripe.api_key`) and so Mock-only deploys
// don't need to read or set any of these fields.
type StripePaymentConfig struct {
	// APIKey is the Stripe secret key. Accepts any of:
	//   sk_test_*  — full-account test key (dev only)
	//   sk_live_*  — full-account live key (PRODUCTION POLICY VIOLATION;
	//                use restricted keys instead)
	//   rk_test_*  — restricted-scope test key (recommended for staging)
	//   rk_live_*  — restricted-scope live key (recommended for production)
	//
	// Production deploys SHOULD use restricted-scope keys with
	// `payment_intent:read,write` scope only. Adapter doesn't enforce
	// key format — runbook + Stripe dashboard policy are the
	// scope-enforcement layer. Validate() rejects test keys (sk_test_/
	// rk_test_) when APP_ENV=production unless AllowTestKey is true.
	APIKey string `yaml:"api_key" env:"STRIPE_API_KEY"`

	// WebhookSecret is the Stripe signing secret (whsec_*) for
	// inbound webhook payload verification. Replaces the legacy
	// `Payment.WebhookSecret` field — Validate() falls back to that
	// field with a deprecation warning if this one is empty.
	WebhookSecret string `yaml:"webhook_secret" env:"STRIPE_WEBHOOK_SECRET"`

	// Timeout is the per-Stripe-call HTTP deadline. Stripe's docs
	// prescribe 30s; faster timeouts cause spurious failures during
	// their occasional 3-5s response time on PaymentIntent create.
	// Adapter clamps to 5min ceiling (defends against config-typo
	// `STRIPE_TIMEOUT=300` interpreted as seconds-when-meant-as-ms).
	Timeout time.Duration `yaml:"timeout" env:"STRIPE_TIMEOUT" env-default:"30s"`

	// MaxNetworkRetries is the per-Backend retry budget (passed to
	// `BackendConfig.MaxNetworkRetries`). Default 2 matches stripe-go's
	// `DefaultMaxNetworkRetries` const. Adapter clamps to 10 max
	// (defends against config-typo `STRIPE_MAX_NETWORK_RETRIES=9999`
	// holding a recon goroutine for tens of minutes).
	MaxNetworkRetries int64 `yaml:"max_network_retries" env:"STRIPE_MAX_NETWORK_RETRIES" env-default:"2"`

	// AllowTestKey is the explicit opt-in for "production stack
	// pointing at Stripe test mode" runs (CI smoke against a staging
	// environment that uses production-mode app config but Stripe
	// test keys). Default false: production-mode validation rejects
	// `sk_test_*` / `rk_test_*` / `pk_test_*` API keys at startup
	// unless this is true.
	//
	// Boolean parsing (plan-review HIGH #3): cleanenv delegates to
	// `strconv.ParseBool` which accepts ONLY `1/t/T/TRUE/true/True/
	// 0/f/F/FALSE/false/False`. **Strings like `yes` / `on` / `Y` are
	// rejected** with a parse error that surfaces as a startup
	// failure with no payment-config context. Set this to literal
	// `true` (or `1`) — anything else fails loud but confusingly.
	AllowTestKey bool `yaml:"allow_test_key" env:"STRIPE_ALLOW_TEST_KEY" env-default:"false"`
}

// ExpiryConfig holds the tunables for the `expiry-sweeper` subcommand
// (D6) — the sweeper that transitions overdue `awaiting_payment`
// reservations to `expired` and emits `order.failed` so the saga
// compensator reverts Redis inventory.
//
// Structurally similar to SagaConfig but with no analog of
// `StuckThreshold` — D6's eligibility check is `reserved_until <=
// NOW() - GracePeriod` itself (the threshold IS the deadline). Saga
// separates StuckThreshold from the deadline because the saga
// consumer races the watchdog for the same row; D6 has no such
// consumer-side path (the customer already missed their window).
//
// Defaults are aligned with the v0.5.0 demo arc — sub-minute end-to-
// end (book → expired → compensated) with `BOOKING_RESERVATION_WINDOW=20s`
// in the smoke env. Production would typically run with the default
// 15-minute reservation window and pick up corresponding values for
// SweepInterval if the sweeper-cadence-vs-volume tradeoff shifts.
type ExpiryConfig struct {
	// SweepInterval — how often the sweeper loop fires. Default 30s,
	// lower than SagaConfig.WatchdogInterval's 60s because every tick
	// of "row past reserved_until but not swept" is a soft-locked
	// Redis seat. Floor of 30s gives the partial-index scan
	// comfortable margin and matches the cadence implied by
	// migration 000012's design comment.
	SweepInterval time.Duration `yaml:"sweep_interval" env:"EXPIRY_SWEEP_INTERVAL" env-default:"30s"`

	// ExpiryGracePeriod — polite head-start for in-flight D5 webhooks.
	// Default 2s ≈ typical D5 webhook end-to-end latency.
	//
	// **Validation: `>= 0`** (NOT `> 0`). 0 is a legal "tight mode"
	// — the sweeper hits a row the moment `reserved_until` ticks past
	// `NOW()`. Tradeoff: more `already_terminal` log lines from
	// concurrent succeeded webhooks, but tighter inventory recovery.
	// Correctness-wise, the grace doesn't matter — D5's MarkPaid SQL
	// has its own `reserved_until > NOW()` predicate so a true late-
	// success still routes to `handleLateSuccess`. The grace just
	// reduces benign-race noise.
	ExpiryGracePeriod time.Duration `yaml:"expiry_grace_period" env:"EXPIRY_GRACE_PERIOD" env-default:"2s"`

	// MaxAge — labeling/alerting threshold. **Does NOT gate the
	// transition.** Rows past MaxAge get the `expired_overaged`
	// outcome label + bump the dedicated `expiry_max_age_total`
	// counter (consumed by the `ExpiryMaxAgeExceeded` alert), but
	// they ARE still expired + emit `order.failed`. Skipping ancient
	// rows would re-create the soft-lock problem D6 exists to solve.
	//
	// Saga's "skip + alert" rationale (Redis state unknown) does NOT
	// apply: D6 doesn't touch Redis directly, and the saga
	// compensator's `saga:reverted:order:<id>` SETNX guard makes the
	// re-emit idempotent.
	MaxAge time.Duration `yaml:"max_age" env:"EXPIRY_MAX_AGE" env-default:"24h"`

	// BatchSize — orders processed per sweep cycle. Default 100
	// (matches recon + saga). At default cadence: 100 × 120 = 12k
	// rows/h drainage cap, comfortably above realistic abandonment
	// peak. Bumpable transiently to 1000+ for backlog recovery after
	// a long sweeper outage.
	BatchSize int `yaml:"batch_size" env:"EXPIRY_BATCH_SIZE" env-default:"100"`
}

// SagaConfig holds the tunables for the `saga-watchdog` subcommand
// (A5) — the sweeper that re-drives stuck-Failed orders through the
// existing (idempotent) compensator. Same shape as ReconConfig but
// distinct so the two sweepers can tune independently:
//
//   - The reconciler queries an EXTERNAL gateway (slow, rate-limited);
//     its tunables reflect gateway latency budget.
//   - The watchdog re-invokes the local compensator (fast, in-process);
//     its tunables can be tighter without affecting external systems.
//
// All defaults are heuristic — same caveat as ReconConfig: tune after
// production data accumulates on `saga_watchdog_resolve_age_seconds`.
type SagaConfig struct {
	// WatchdogInterval — how often the watchdog loop fires. Tighter
	// than the reconciler (60s vs 120s) because the compensator call
	// is local + fast; we'd rather catch stuck-Failed states quickly
	// to keep saga compensations close to real-time customer
	// expectations.
	WatchdogInterval time.Duration `yaml:"watchdog_interval" env:"SAGA_WATCHDOG_INTERVAL" env-default:"60s"`

	// StuckThreshold — minimum age a Failed row must have before the
	// watchdog considers it stuck. Set above the typical
	// Failed→Compensated saga round-trip so we never race the legitimate
	// in-flight compensator.
	//
	// Typical saga compensation is sub-second (Redis revert + DB
	// MarkCompensated); 60s gives 60× headroom and keeps the watchdog
	// conservative. Lower if production shows compensations completing
	// well under the threshold consistently.
	StuckThreshold time.Duration `yaml:"stuck_threshold" env:"SAGA_STUCK_THRESHOLD" env-default:"60s"`

	// MaxFailedAge — give-up cutoff. A Failed row older than this is
	// logged at ERROR + counted under
	// `saga_watchdog_resolved_total{outcome="max_age_exceeded"}` but
	// NOT auto-transitioned (unlike the reconciler's force-fail
	// path) — moving Failed → Compensated without verifying inventory
	// was actually reverted is unsafe. Persistent max-age-exceeded
	// fires the operator alert for manual review.
	//
	// 24h matches the reconciler's MaxChargingAge for symmetry.
	MaxFailedAge time.Duration `yaml:"max_failed_age" env:"SAGA_MAX_FAILED_AGE" env-default:"24h"`

	// BatchSize — max orders to resolve per sweep cycle. 100 mirrors
	// recon. The compensator is faster than gateway.GetStatus, so
	// 100 orders/sweep at <50ms each = ~5s, well within
	// WatchdogInterval. Bump if backlog grows.
	BatchSize int `yaml:"batch_size" env:"SAGA_BATCH_SIZE" env-default:"100"`
}

// InventoryDriftConfig holds tunables for the PR-D drift detector that
// runs in the same `recon` subcommand process. Sibling to ReconConfig
// rather than a sub-struct because the two sweepers SHOULD tune
// independently — the reconciler queries an EXTERNAL gateway (slow);
// the drift detector reads the LOCAL Redis pool (sub-ms) so it can
// afford a tighter cadence on a much larger scan.
//
// Final piece of the cache-truth roadmap (PR-A: Makefile reset; PR-B:
// rehydrate-on-startup; PR-C: NOGROUP self-heal alert; PR-D: drift
// detection). See docs/architectural_backlog.md "Cache-truth
// architecture" for the full sequence.
type InventoryDriftConfig struct {
	// SweepInterval — how often the drift detector loop fires when
	// running in default loop mode. 60s default rationale: Redis GET
	// is sub-ms and ListAvailable scans a single index — even at 10k
	// active events one sweep is well under a second. 60s gives the
	// alert's `for: 5m` clause five fresh data points before paging,
	// which is the standard SRE "transient blip vs sustained anomaly"
	// discriminator. Lower if drift events MUST be caught faster than
	// 5 minutes (reasonable for a high-throughput flash sale).
	SweepInterval time.Duration `yaml:"sweep_interval" env:"INVENTORY_DRIFT_SWEEP_INTERVAL" env-default:"60s"`

	// AbsoluteTolerance — the |drift| ceiling for the `cache_low_excess`
	// branch. Drift values within this band are NOT flagged.
	//
	// Default 100 rationale: under steady-state load the difference
	// between Redis (decremented immediately by Lua) and DB (decremented
	// by the worker after async stream-consume) is bounded by inflight-
	// bookings. At C=500 VUs with a typical 50ms worker round-trip,
	// inflight is on the order of 25 messages. 100 leaves comfortable
	// headroom while still catching the operationally-meaningful
	// "worker is failing to commit hundreds of bookings" failure mode.
	//
	// Set lower (e.g. 10) in test environments where in-flight is
	// always zero. Set higher only after observing sustained false
	// positives in production at the chosen value.
	AbsoluteTolerance int `yaml:"absolute_tolerance" env:"INVENTORY_DRIFT_ABSOLUTE_TOLERANCE" env-default:"100"`
}

type AppConfig struct {
	Name     string `yaml:"name" env:"APP_NAME" env-default:"booking_monitor"`
	Version  string `yaml:"version" env:"APP_VERSION" env-default:"1.0.0"`
	LogLevel string `yaml:"log_level" env:"LOG_LEVEL" env-default:"info"`
	WorkerID string `yaml:"worker_id" env:"WORKER_ID" env-default:"worker-1"`
	// Env is the deployment environment label (e.g. "production",
	// "staging", "development"). Used by Validate() to gate
	// production-only invariants (no localhost Redis/Kafka, no
	// /test/* endpoints). Empty string is normalised to
	// "production" by normalizedAppEnv() — fail-closed so a
	// bypass-cleanenv literal can't accidentally relax production
	// guards.
	Env string `yaml:"env" env:"APP_ENV" env-default:"production"`
}

type ServerConfig struct {
	Port         string        `yaml:"port" env:"PORT" env-default:"8080"`
	ReadTimeout  time.Duration `yaml:"read_timeout" env:"SERVER_READ_TIMEOUT" env-default:"5s"`
	WriteTimeout time.Duration `yaml:"write_timeout" env:"SERVER_WRITE_TIMEOUT" env-default:"10s"`

	// EnablePprof gates the operator-only pprof listener. Off by default
	// so heap dumps + /admin/loglevel aren't exposed in every deployment.
	EnablePprof bool `yaml:"enable_pprof" env:"ENABLE_PPROF" env-default:"false"`

	// EnableTestEndpoints gates the `/test/*` route group used by
	// integration tests + dev demos to simulate provider-side events
	// (e.g. POST /test/payment/confirm/:order_id triggers a signed
	// webhook delivery). Off by default — production deployments
	// MUST leave it false. The route group is conditionally mounted
	// inside `buildGinEngine`, so when this is false the paths
	// return 404 (not 401) — impossible to enable accidentally.
	EnableTestEndpoints bool `yaml:"enable_test_endpoints" env:"ENABLE_TEST_ENDPOINTS" env-default:"false"`
	// PprofAddr is the bind address for the pprof listener. Defaults to
	// loopback so remote access requires an explicit override.
	PprofAddr         string        `yaml:"pprof_addr" env:"PPROF_ADDR" env-default:"127.0.0.1:6060"`
	PprofReadTimeout  time.Duration `yaml:"pprof_read_timeout" env:"PPROF_READ_TIMEOUT" env-default:"5s"`
	PprofWriteTimeout time.Duration `yaml:"pprof_write_timeout" env:"PPROF_WRITE_TIMEOUT" env-default:"30s"`

	// TrustedProxies is the list of CIDRs Gin trusts for ClientIP()
	// resolution. Default covers RFC1918 ranges (docker / k8s pod
	// CIDRs); override for service meshes with non-RFC1918 IPs (some
	// GKE / EKS setups). In yaml write as a sequence; env vars are
	// parsed as a comma-separated list (env-separator:",").
	TrustedProxies []string `yaml:"trusted_proxies" env:"TRUSTED_PROXIES" env-separator:"," env-default:"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"`

	// CORSAllowedOrigins is the exact-match allow-list of Origins
	// the API will echo via Access-Control-Allow-Origin. Empty
	// disables the CORS middleware entirely (production-safe
	// default — same-origin / non-browser callers are unaffected).
	// For the D8-minimal browser demo, set to
	// "http://localhost:5173,http://127.0.0.1:5173" so Vite dev
	// server on either loopback variant works.
	CORSAllowedOrigins []string `yaml:"cors_allowed_origins" env:"CORS_ALLOWED_ORIGINS" env-separator:","`
}

type RedisConfig struct {
	Addr         string        `yaml:"addr" env:"REDIS_ADDR" env-default:"localhost:6379"`
	Password     string        `yaml:"password" env:"REDIS_PASSWORD"`
	DB           int           `yaml:"db" env:"REDIS_DB" env-default:"0"`
	PoolSize     int           `yaml:"pool_size" env:"REDIS_POOL_SIZE" env-default:"200"`
	MinIdleConns int           `yaml:"min_idle_conns" env:"REDIS_MIN_IDLE_CONNS" env-default:"20"`
	ReadTimeout  time.Duration `yaml:"read_timeout" env:"REDIS_READ_TIMEOUT" env-default:"500ms"`
	WriteTimeout time.Duration `yaml:"write_timeout" env:"REDIS_WRITE_TIMEOUT" env-default:"500ms"`
	PoolTimeout  time.Duration `yaml:"pool_timeout" env:"REDIS_POOL_TIMEOUT" env-default:"2s"`

	// MaxConsecutiveReadErrors bounds how long the order-stream Subscribe
	// loop tolerates a broken Redis before returning and letting the
	// caller restart the worker. At the current 2s block + 1s sleep
	// cadence, 30 ≈ 90s of persistent failure — long enough to ride out
	// a brief blip, short enough that k8s restarts the pod before the
	// booking backlog becomes unrecoverable. Raise for stricter pods
	// that should self-heal rather than restart; lower for clusters
	// that want faster shedding to a healthy replica.
	MaxConsecutiveReadErrors int `yaml:"max_consecutive_read_errors" env:"REDIS_MAX_CONSECUTIVE_READ_ERRORS" env-default:"30"`

	// InventoryTTL is the maximum lifetime of ticket-type runtime keys
	// (`ticket_type_qty:{uuid}` + `ticket_type_meta:{uuid}`). Long by
	// default (30d) — active ticket types are re-upserted by
	// operational flows well before expiry; the TTL exists so orphaned
	// keys from deleted events fall off Redis instead of accumulating
	// forever. Lower for tighter memory budgets; raise for events with
	// very long sale windows (festival pre-sale, season-pass).
	InventoryTTL time.Duration `yaml:"inventory_ttl" env:"REDIS_INVENTORY_TTL" env-default:"720h"`

	// IdempotencyTTL bounds how long an `Idempotency-Key`-keyed cached
	// response is retained. Must align with the longest client retry
	// window your callers use; raise for financial reconciliation
	// flows that retry across days.
	IdempotencyTTL time.Duration `yaml:"idempotency_ttl" env:"REDIS_IDEMPOTENCY_TTL" env-default:"24h"`

	// DLQRetention is the bounded retention window for the
	// `orders:dlq` stream. Translated to a Redis Streams MINID
	// directive on every XADD so entries older than NOW-DLQRetention
	// are evicted.
	//
	// Tunable per-environment because retention is a POLICY decision,
	// not a defensive invariant:
	//   * Compliance (PCI / SOX / GDPR) varies by industry
	//   * Operator may want to extend during incident investigation
	//   * Different deployments may have different review SLAs
	//
	// Default 720h (30d) matches typical operator-investigation SLA.
	// Future: pair with S3 archival before MINID drops them — see
	// PROJECT_SPEC §6.8 deferred items.
	DLQRetention time.Duration `yaml:"dlq_retention" env:"REDIS_DLQ_RETENTION" env-default:"720h"`
}

// WorkerConfig holds tunables for the order-stream worker loop
// (`internal/infrastructure/cache/redis_queue.go`). These are pure
// per-environment knobs — the wire-contract values (`streamKey`,
// `groupName`, `dlqKey`, the XADD field names) deliberately stay as
// const because mismatches across replicas would silently split
// brain. See the Const-vs-Config split documented in
// `memory/config_tunables_audit.md`.
type WorkerConfig struct {
	// StreamReadCount is the XReadGroup batch size. Higher = more
	// throughput per call, more memory per goroutine; tune for
	// ingest rate vs. tail latency.
	StreamReadCount int `yaml:"stream_read_count" env:"WORKER_STREAM_READ_COUNT" env-default:"10"`

	// StreamBlockTimeout is how long XReadGroup blocks waiting for
	// new messages before returning empty. Lower = faster shutdown
	// detection, higher CPU; higher = lower CPU, slower shutdown.
	StreamBlockTimeout time.Duration `yaml:"stream_block_timeout" env:"WORKER_STREAM_BLOCK_TIMEOUT" env-default:"2s"`

	// MaxRetries is the per-message retry budget inside
	// `processWithRetry`. Each retry that yields a transient error
	// burns one slot; deterministic-failure errors (NewOrder
	// invariant violations) bypass the budget via the retry policy
	// and route directly to DLQ.
	MaxRetries int `yaml:"max_retries" env:"WORKER_MAX_RETRIES" env-default:"3"`

	// RetryBaseDelay is the base delay for the linear backoff
	// (attempt N waits N*base). Industry would prefer exponential
	// + jitter under heavy concurrency to avoid thundering herd on
	// simultaneous failures; that is a separate refactor.
	RetryBaseDelay time.Duration `yaml:"retry_base_delay" env:"WORKER_RETRY_BASE_DELAY" env-default:"100ms"`

	// FailureTimeout bounds the `handleFailure` compensation budget
	// (Redis revert + DLQ XAdd). Runs against a fresh background
	// context so compensation completes even if the parent ctx was
	// cancelled mid-processing.
	FailureTimeout time.Duration `yaml:"failure_timeout" env:"WORKER_FAILURE_TIMEOUT" env-default:"5s"`

	// PendingBlockTimeout is the XReadGroup block timeout used by
	// the startup PEL recovery sweep. Short so the call honours
	// shutdown signals on cold boot when the stream is empty.
	PendingBlockTimeout time.Duration `yaml:"pending_block_timeout" env:"WORKER_PENDING_BLOCK_TIMEOUT" env-default:"100ms"`

	// ReadErrorBackoff is the sleep between XReadGroup failure
	// retries (NOGROUP / connection drop). Bounds how fast the
	// outer loop re-attempts under persistent breakage.
	ReadErrorBackoff time.Duration `yaml:"read_error_backoff" env:"WORKER_READ_ERROR_BACKOFF" env-default:"1s"`

	// MetricsAddr is the bind address for the worker subcommand's
	// metrics-only HTTP listener (Prometheus `/metrics` +
	// `/healthz`). Empty disables — useful for tests and `--once`
	// CronJob hosting where the process exits before Prometheus
	// could scrape it.
	//
	// The application server (`booking-cli server`) does NOT use
	// this — it serves /metrics on its main Gin engine at
	// `cfg.Server.Port`. Worker subcommands (`payment`, `recon`,
	// `saga-watchdog`) have no Gin engine, so a dedicated listener
	// is the cheapest way to expose their per-process Prometheus
	// registry to the scraper. Closes Phase 2 checkpoint O3:
	// before this, recon_*, saga_watchdog_*, and
	// kafka_consumer_retry_total were registered into worker
	// processes' default registries but never scraped.
	//
	// Default `:9091` matches the convention of "9090 = Prometheus,
	// 9091 = first scrape target." Bind on all interfaces because
	// the in-cluster scraper reaches the worker via Docker DNS;
	// override to a specific interface (e.g. `127.0.0.1:9091`) for
	// host-network deployments where the port shouldn't be public.
	MetricsAddr string `yaml:"metrics_addr" env:"WORKER_METRICS_ADDR" env-default:":9091"`
}

type PostgresConfig struct {
	DSN          string        `yaml:"dsn" env:"DATABASE_URL"` // Constructed or direct override
	MaxOpenConns int           `yaml:"max_open_conns" env:"DB_MAX_OPEN_CONNS" env-default:"50"`
	MaxIdleConns int           `yaml:"max_idle_conns" env:"DB_MAX_IDLE_CONNS" env-default:"5"`
	MaxIdleTime  time.Duration `yaml:"max_idle_time" env:"DB_MAX_IDLE_TIME" env-default:"5m"`
	// MaxLifetime bounds how long a single Postgres connection may live.
	// Unlike MaxIdleTime (which evicts idle conns), MaxLifetime forces
	// recycling of busy conns so long-lived ones don't accumulate
	// staleness (memory, prepared statement caches, PgBouncer auth
	// drift). 30m is a safe default for most deployments.
	MaxLifetime time.Duration `yaml:"max_lifetime" env:"DB_MAX_LIFETIME" env-default:"30m"`

	// PingAttempts / PingInterval / PingPerAttempt bound how long the
	// startup loop waits for Postgres to become reachable. Slower
	// environments (k8s initContainers, spin-up dependencies) should
	// raise PingAttempts; there's no exponential backoff so total budget
	// is roughly PingAttempts * (PingInterval + PingPerAttempt).
	PingAttempts   int           `yaml:"ping_attempts" env:"DB_PING_ATTEMPTS" env-default:"10"`
	PingInterval   time.Duration `yaml:"ping_interval" env:"DB_PING_INTERVAL" env-default:"1s"`
	PingPerAttempt time.Duration `yaml:"ping_per_attempt" env:"DB_PING_PER_ATTEMPT" env-default:"3s"`
}

type KafkaConfig struct {
	// Brokers is the list of Kafka broker addresses. In yaml write as
	// a sequence; env vars are parsed as a comma-separated list
	// (env-separator:",").
	Brokers []string `yaml:"brokers" env:"KAFKA_BROKERS" env-separator:"," env-default:"localhost:9092"`
	// WriteTimeout is the max time to wait for a Kafka write to complete.
	WriteTimeout time.Duration `yaml:"write_timeout" env:"KAFKA_WRITE_TIMEOUT" env-default:"5s"`
	// OutboxBatchSize controls how many outbox events are processed per relay tick.
	OutboxBatchSize int `yaml:"outbox_batch_size" env:"KAFKA_OUTBOX_BATCH_SIZE" env-default:"100"`
	// SagaGroupID is the Kafka consumer group id for the saga consumer
	// (in-process inside `app`). Pre-D7 there was a sibling
	// `PaymentGroupID` for the legacy A4 `payment_worker` consumer of
	// `order.created`; D7 deleted that binary along with the Kafka
	// `KAFKA_PAYMENT_GROUP_ID` / `KAFKA_ORDER_CREATED_TOPIC` env vars.
	SagaGroupID string `yaml:"saga_group_id" env:"KAFKA_SAGA_GROUP_ID" env-default:"booking-saga-group"`
	// OrderFailedTopic is the topic the saga consumer subscribes to.
	OrderFailedTopic string `yaml:"order_failed_topic" env:"KAFKA_ORDER_FAILED_TOPIC" env-default:"order.failed"`
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}

	if err := cleanenv.ReadConfig(path, cfg); err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}

	// D4.2 backward-compat: if STRIPE_WEBHOOK_SECRET is unset but the
	// legacy PAYMENT_WEBHOOK_SECRET is set, populate the new field
	// from the legacy field and emit a deprecation warning. Stripe
	// adapter (Slice 1) reads `Payment.Stripe.WebhookSecret`; webhook
	// handler reads `Payment.WebhookSecret` (legacy path); this
	// fallback unifies them so dev/CI deploys that still set the
	// legacy var keep working without explicit env updates.
	if cfg.Payment.Stripe.WebhookSecret == "" && cfg.Payment.WebhookSecret != "" {
		cfg.Payment.Stripe.WebhookSecret = cfg.Payment.WebhookSecret
		fmt.Fprintln(os.Stderr,
			"WARN: STRIPE_WEBHOOK_SECRET is empty; falling back to legacy PAYMENT_WEBHOOK_SECRET. "+
				"Set STRIPE_WEBHOOK_SECRET explicitly — PAYMENT_WEBHOOK_SECRET is deprecated and will be "+
				"removed in a future release. (D4.2)")
	}
	// D4.2 plan-review M1: when BOTH STRIPE_WEBHOOK_SECRET and
	// PAYMENT_WEBHOOK_SECRET are set with DIFFERENT values, the
	// silent precedence (STRIPE wins) hides typos. A mid-migration
	// deploy that typo'd the new var would silently 401 every
	// inbound webhook with no diagnostic. Surface the conflict
	// loudly so operators can detect-and-correct.
	if cfg.Payment.Stripe.WebhookSecret != "" &&
		cfg.Payment.WebhookSecret != "" &&
		cfg.Payment.Stripe.WebhookSecret != cfg.Payment.WebhookSecret {
		fmt.Fprintln(os.Stderr,
			"WARN: Both STRIPE_WEBHOOK_SECRET and PAYMENT_WEBHOOK_SECRET are set with DIFFERENT values; "+
				"STRIPE_WEBHOOK_SECRET takes precedence. If this is intentional, remove "+
				"PAYMENT_WEBHOOK_SECRET to suppress this warning; if not, the values likely diverged "+
				"due to a typo during D4.2 migration. (D4.2)")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	checkDeprecatedEnv()

	return cfg, nil
}

// deprecatedEnvVars is the registry of env vars that USED to be read
// by an earlier release and are now silently ignored by `cleanenv`
// (which doesn't error on unknown keys). Each entry maps the env name
// to a short operator-facing message describing what replaced it.
//
// Why this exists: a fresh-build deploy where the operator's
// `.env` / configmap / EnvironmentFile still sets one of these is
// otherwise undetectable at runtime — the variable is parsed by no
// struct field, no validator complains, and the process starts with
// the wrong-by-default behaviour. Logging at startup turns the silent
// misconfig into a loud warning.
//
// Convention: when removing a field that previously had `env:"X"`,
// add an entry here for at least one major release so operators
// migrating from older docker-compose / Helm charts get a signal.
var deprecatedEnvVars = map[string]string{
	"BOOKING_DEFAULT_TICKET_PRICE_CENTS": "removed in D4.1; price is now per `event_ticket_types` row (KKTIX 票種 model). Set price via POST /api/v1/events { price_cents } at event creation time.",
	"BOOKING_DEFAULT_CURRENCY":           "removed in D4.1; currency is now per `event_ticket_types` row (KKTIX 票種 model). Set currency via POST /api/v1/events { currency } at event creation time.",
	"KAFKA_PAYMENT_GROUP_ID":             "removed in D7 (2026-05-08); the legacy A4 `payment_worker` Kafka consumer was deleted. Pattern A drives money movement through `/api/v1/orders/:id/pay` (D4) + the provider webhook (D5); no payment-side Kafka group remains. Saga consumer's `KAFKA_SAGA_GROUP_ID` is unaffected.",
	"KAFKA_ORDER_CREATED_TOPIC":          "removed in D7 (2026-05-08); the `order.created` Kafka topic has no producer or consumer post-D7 (the worker UoW no longer emits `events_outbox(order.created)`). Saga consumer's `KAFKA_ORDER_FAILED_TOPIC` is unaffected.",
}

// checkDeprecatedEnv writes a Stderr warning for any deprecated env
// var that's still set in the process environment. Stderr (NOT the
// structured log) because LoadConfig runs BEFORE the logger is wired,
// and we want this signal to be unconditionally visible in any
// container log even on a misconfigured logging stack.
//
// Empty values are treated as unset (`os.Getenv` returns "" for both
// "absent" and "explicitly empty" — they're operationally equivalent).
func checkDeprecatedEnv() {
	for name, replacement := range deprecatedEnvVars {
		if val := os.Getenv(name); val != "" {
			fmt.Fprintf(os.Stderr, "[config] WARN: env var %s=%q is deprecated and IGNORED — %s\n", name, val, replacement)
		}
	}
}

// Validate checks that required-at-startup fields are present. It
// returns the aggregated list of missing fields as one error. Callers
// (cmd/booking-cli) should treat this as fatal and exit before any fx
// wiring runs, so operators get a precise error instead of a cryptic
// connection failure several seconds later.
func (c *Config) Validate() error {
	var missing []string

	// DSN has no env-default: it MUST be set via DATABASE_URL or yaml.
	if strings.TrimSpace(c.Postgres.DSN) == "" {
		missing = append(missing, "postgres.dsn / DATABASE_URL")
	}

	// Server port is defaulted via env-default but a zero-length explicit
	// value would still be invalid.
	if strings.TrimSpace(c.Server.Port) == "" {
		missing = append(missing, "server.port / PORT")
	}

	// Redis / Kafka defaults are fine for local dev, but in production
	// (APP_ENV=production) we reject the localhost defaults so ops can't
	// ship on a silent localhost connection. Reads the typed AppConfig
	// field via normalizedAppEnv() so a `&Config{...}` literal that
	// bypasses cleanenv's env-default still hits the production-fail-closed
	// branch (was os.Getenv("APP_ENV") which would return "" for that
	// path → guards would silently relax).
	if normalizedAppEnv(c.App.Env) == "production" {
		if isLocalhostAddr(c.Redis.Addr) {
			missing = append(missing, "redis.addr / REDIS_ADDR (localhost default not permitted in production)")
		}
		if isLocalhostBrokers(c.Kafka.Brokers) {
			missing = append(missing, "kafka.brokers / KAFKA_BROKERS (localhost default not permitted in production)")
		}
		// Test endpoints (POST /test/payment/confirm/:order_id, etc.) are
		// for integration tests + the D8-minimal browser demo. They
		// bypass the real payment provider and forge webhooks — shipping
		// production with this enabled would let anyone confirm/expire
		// any order id by URL guess. Reject the combination at startup
		// so it can never reach a live deployment.
		if c.Server.EnableTestEndpoints {
			missing = append(missing, "server.enable_test_endpoints / ENABLE_TEST_ENDPOINTS (forbidden when APP_ENV=production)")
		}

		// D4.2: production must use the real Stripe adapter. MockGateway
		// is "always succeed" semantics — running it in production
		// would silently confirm payments without any actual money
		// movement. Reject startup.
		//
		// Plan-review M2 ordering: the Provider whitelist check
		// (below the production block) runs FIRST when Provider is a
		// typo (so a "stipe" → "must be 'mock' or 'stripe'" error is
		// the only message), avoiding 4 overlapping confusing errors
		// for a single typo. Production-mode block here only runs
		// for known-valid values "mock" / "stripe" / "".
		if c.Payment.Provider == "mock" || c.Payment.Provider == "" {
			missing = append(missing, fmt.Sprintf(
				"payment.provider / PAYMENT_PROVIDER must be 'stripe' in production (got %q); MockGateway is a dev-only adapter that never moves real money",
				c.Payment.Provider))
		}
	}

	// D4.2 Plan-review M2: whitelist Provider FIRST. Reject typo'ed
	// values with a single clear message. If we get past this switch
	// the Provider is known-valid (mock / stripe / empty-as-mock).
	switch c.Payment.Provider {
	case "mock", "stripe":
		// OK
	case "":
		// Empty falls through to env-default "mock"; cleanenv's
		// env-default kicks in BEFORE Validate, but a `&Config{...}`
		// literal that bypasses env loading would have empty here.
		// Treat as "mock" rather than rejecting.
	default:
		missing = append(missing, fmt.Sprintf(
			"payment.provider / PAYMENT_PROVIDER must be 'mock' or 'stripe' (got %q)",
			c.Payment.Provider))
	}

	// D4.2 Plan-review M6: when Provider=stripe, require APIKey +
	// WebhookSecret regardless of APP_ENV. Without this, a dev/staging
	// deploy that sets PAYMENT_PROVIDER=stripe but forgets the keys
	// passes Validate and fails late at fx-construction time
	// (NewStripeGateway returns "APIKey is required") — loud-but-
	// delayed. Catching it at startup gives a "config validation:"
	// prefix consistent with other config errors.
	if c.Payment.Provider == "stripe" {
		if c.Payment.Stripe.APIKey == "" {
			missing = append(missing, "payment.stripe.api_key / STRIPE_API_KEY (required when PAYMENT_PROVIDER=stripe)")
		} else {
			// Refuse to start with a Stripe test key in production-mode
			// unless explicitly opted in. A `sk_test_` / `rk_test_` /
			// `pk_test_` key in production silently runs against
			// Stripe's test mode — money never actually moves; we look
			// healthy but customers don't get charged.
			//
			// Plan-review L7: also reject `pk_test_` (Stripe publishable
			// key, test-mode) for completeness — a copy-paste from the
			// Stripe dashboard could put it in STRIPE_API_KEY by
			// mistake; pk_* is server-side-invalid but our prefix
			// match would silently accept it.
			if normalizedAppEnv(c.App.Env) == "production" &&
				(strings.HasPrefix(c.Payment.Stripe.APIKey, "sk_test_") ||
					strings.HasPrefix(c.Payment.Stripe.APIKey, "rk_test_") ||
					strings.HasPrefix(c.Payment.Stripe.APIKey, "pk_test_")) &&
				!c.Payment.Stripe.AllowTestKey {
				missing = append(missing, "payment.stripe.api_key / STRIPE_API_KEY is a test key (sk_test_/rk_test_/pk_test_); set STRIPE_ALLOW_TEST_KEY=true to allow (deliberate staging-vs-prod cutover scenarios only)")
			}
			// Final-pre-merge review fix: also reject the LIVE publishable
			// key (`pk_live_*`). Same justification as `pk_test_` —
			// publishable keys belong in the FRONTEND, not the backend's
			// STRIPE_API_KEY. Stripe rejects pk_live_ on every server-side
			// API call with 401; without this guard the deploy boots
			// successfully and produces a flood of confusing
			// `ErrPaymentMisconfigured` responses instead of failing fast
			// at startup.
			if normalizedAppEnv(c.App.Env) == "production" &&
				strings.HasPrefix(c.Payment.Stripe.APIKey, "pk_live_") {
				missing = append(missing, "payment.stripe.api_key / STRIPE_API_KEY is a publishable key (pk_live_*) — Stripe rejects publishable keys on server-side API calls with 401; use a Secret Key (sk_live_*) or Restricted Key (rk_live_*; preferred — minimum-privilege scope)")
			}
		}
		if c.Payment.Stripe.WebhookSecret == "" && c.Payment.WebhookSecret == "" {
			missing = append(missing, "payment.stripe.webhook_secret / STRIPE_WEBHOOK_SECRET (or legacy PAYMENT_WEBHOOK_SECRET) required when PAYMENT_PROVIDER=stripe")
		}
	}

	// Worker tunables must be positive — a zero-value config (e.g. an
	// operator setting `WORKER_MAX_RETRIES=0` thinking "disable", or a
	// partially-constructed `&config.Config{...}` literal that bypasses
	// cleanenv's env-default tags) makes the worker silently
	// non-functional rather than producing the assumed behaviour:
	//
	//   * MaxRetries=0  → for-loop never iterates → handler never runs → message stuck
	//   * RetryBaseDelay=0 → backoff is instant → fast spin on transient errors
	//   * StreamReadCount=0 → Redis interprets as "all messages" → unbounded batch
	//   * StreamBlockTimeout=0 → busy-poll → CPU pegged
	//   * MaxConsecutiveReadErrors=0 → exit on first error (looks like "disabled")
	//
	// Reject all of these at startup so ops sees the misconfiguration
	// before traffic hits.
	if c.Worker.MaxRetries <= 0 {
		missing = append(missing, "worker.max_retries / WORKER_MAX_RETRIES (must be >= 1)")
	}
	if c.Worker.RetryBaseDelay <= 0 {
		missing = append(missing, "worker.retry_base_delay / WORKER_RETRY_BASE_DELAY (must be > 0)")
	}
	if c.Worker.StreamReadCount <= 0 {
		missing = append(missing, "worker.stream_read_count / WORKER_STREAM_READ_COUNT (must be >= 1)")
	}
	if c.Worker.StreamBlockTimeout <= 0 {
		missing = append(missing, "worker.stream_block_timeout / WORKER_STREAM_BLOCK_TIMEOUT (must be > 0)")
	}
	if c.Worker.FailureTimeout <= 0 {
		missing = append(missing, "worker.failure_timeout / WORKER_FAILURE_TIMEOUT (must be > 0)")
	}
	if c.Worker.PendingBlockTimeout <= 0 {
		missing = append(missing, "worker.pending_block_timeout / WORKER_PENDING_BLOCK_TIMEOUT (must be > 0)")
	}
	if c.Worker.ReadErrorBackoff <= 0 {
		missing = append(missing, "worker.read_error_backoff / WORKER_READ_ERROR_BACKOFF (must be > 0)")
	}
	if c.Redis.MaxConsecutiveReadErrors <= 0 {
		missing = append(missing, "redis.max_consecutive_read_errors / REDIS_MAX_CONSECUTIVE_READ_ERRORS (must be >= 1; 0 silently exits on first error)")
	}
	if c.Redis.DLQRetention <= 0 {
		missing = append(missing, "redis.dlq_retention / REDIS_DLQ_RETENTION (must be > 0; 0 would trim entire DLQ on every XADD)")
	}

	// Recon tunables — same rationale as Worker tunables above. Zero
	// values produce silent reconciler malfunctions:
	//
	//   * SweepInterval=0     → time.Ticker panics
	//   * ChargingThreshold=0 → recon steals every Charging order, even
	//                           in-flight ones the worker hasn't finished
	//   * GatewayTimeout=0    → context.WithTimeout fires instantly →
	//                           every GetStatus call returns ctx error
	//   * BatchSize=0         → SQL LIMIT 0 → silent no-op every sweep
	//   * MaxChargingAge ≤ ChargingThreshold → every order recon sees is
	//                           force-failed before the gateway is queried
	//
	// Reject all of these at startup.
	if c.Recon.SweepInterval <= 0 {
		missing = append(missing, "recon.sweep_interval / RECON_SWEEP_INTERVAL (must be > 0)")
	}
	if c.Recon.ChargingThreshold <= 0 {
		missing = append(missing, "recon.charging_threshold / RECON_CHARGING_THRESHOLD (must be > 0)")
	}
	if c.Recon.GatewayTimeout <= 0 {
		missing = append(missing, "recon.gateway_timeout / RECON_GATEWAY_TIMEOUT (must be > 0)")
	}
	if c.Recon.MaxChargingAge <= 0 {
		missing = append(missing, "recon.max_charging_age / RECON_MAX_CHARGING_AGE (must be > 0)")
	}
	if c.Recon.BatchSize <= 0 {
		missing = append(missing, "recon.batch_size / RECON_BATCH_SIZE (must be >= 1; 0 makes every sweep a silent no-op)")
	}
	if c.Recon.MaxChargingAge > 0 && c.Recon.ChargingThreshold > 0 &&
		c.Recon.MaxChargingAge <= c.Recon.ChargingThreshold {
		missing = append(missing, fmt.Sprintf(
			"recon.max_charging_age (%s) must be > recon.charging_threshold (%s); otherwise every order recon sees is immediately force-failed",
			c.Recon.MaxChargingAge, c.Recon.ChargingThreshold,
		))
	}

	// Saga watchdog (A5) — same guard logic as Recon. Each duration > 0
	// because 0 means "every sweep is a no-op" silently, and the
	// max-age/threshold cross-field invariant prevents force-flagging
	// every visible order on the first sweep.
	if c.Saga.WatchdogInterval <= 0 {
		missing = append(missing, "saga.watchdog_interval / SAGA_WATCHDOG_INTERVAL (must be > 0)")
	}
	if c.Saga.StuckThreshold <= 0 {
		missing = append(missing, "saga.stuck_threshold / SAGA_STUCK_THRESHOLD (must be > 0)")
	}
	if c.Saga.MaxFailedAge <= 0 {
		missing = append(missing, "saga.max_failed_age / SAGA_MAX_FAILED_AGE (must be > 0)")
	}
	if c.Saga.BatchSize <= 0 {
		missing = append(missing, "saga.batch_size / SAGA_BATCH_SIZE (must be >= 1; 0 makes every sweep a silent no-op)")
	}
	if c.Saga.MaxFailedAge > 0 && c.Saga.StuckThreshold > 0 &&
		c.Saga.MaxFailedAge <= c.Saga.StuckThreshold {
		missing = append(missing, fmt.Sprintf(
			"saga.max_failed_age (%s) must be > saga.stuck_threshold (%s); otherwise every order the watchdog sees is immediately flagged max_age_exceeded",
			c.Saga.MaxFailedAge, c.Saga.StuckThreshold,
		))
	}

	// Inventory drift detector (PR-D). SweepInterval must be > 0 (zero
	// = no sweeps ever fire); AbsoluteTolerance must be >= 0 (negative
	// is meaningless — would flag every event including healthy ones).
	if c.InventoryDrift.SweepInterval <= 0 {
		missing = append(missing, "inventory_drift.sweep_interval / INVENTORY_DRIFT_SWEEP_INTERVAL (must be > 0)")
	}
	if c.InventoryDrift.AbsoluteTolerance < 0 {
		missing = append(missing, fmt.Sprintf(
			"inventory_drift.absolute_tolerance / INVENTORY_DRIFT_ABSOLUTE_TOLERANCE (must be >= 0; got %d)",
			c.InventoryDrift.AbsoluteTolerance,
		))
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %s", strings.Join(missing, ", "))
	}

	return nil
}

// normalizedAppEnv returns the canonical lower-cased deployment
// environment label, normalising empty / whitespace to "production".
// Fail-closed: a `&Config{...}` literal that bypasses cleanenv's
// env-default would otherwise leave AppConfig.Env="" and silently
// relax production guards (no localhost-redis check, /test/* routes
// permitted). Empty → production keeps Validate() honest in every
// construction path.
func normalizedAppEnv(env string) string {
	env = strings.ToLower(strings.TrimSpace(env))
	if env == "" {
		return "production"
	}
	return env
}

// isLocalhostAddr returns true if the string matches the unconfigured
// localhost default cleanenv hands back when REDIS_ADDR is unset.
func isLocalhostAddr(addr string) bool {
	return strings.TrimSpace(addr) == "localhost:6379"
}

// isLocalhostBrokers returns true if the broker slice is the single
// unconfigured localhost default cleanenv hands back when KAFKA_BROKERS
// is unset.
func isLocalhostBrokers(brokers []string) bool {
	return len(brokers) == 1 && strings.TrimSpace(brokers[0]) == "localhost:9092"
}
