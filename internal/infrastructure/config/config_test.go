package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/config"
)

// validBase returns a Config that satisfies every Validate() guard.
// Tests build on this with one field tweaked to verify each guard
// independently.
func validBase() *config.Config {
	return &config.Config{
		// Explicit non-production env so the production-only guards
		// (no localhost Redis/Kafka, no /test/* endpoints) don't
		// trigger on the baseline. Without this, an empty Env would
		// normalise to "production" via normalizedAppEnv, which is
		// fine today (Redis/Kafka zero-values bypass localhost
		// checks, EnableTestEndpoints is false), but the moment a
		// future production guard keys on something validBase
		// happens to set, this test silently starts asserting a
		// production config — the opposite of intent.
		App:    config.AppConfig{Env: "development"},
		Server: config.ServerConfig{Port: "8080"},
		Postgres: config.PostgresConfig{
			DSN: "postgres://u:p@h/db?sslmode=disable",
		},
		Redis: config.RedisConfig{
			MaxConsecutiveReadErrors: 30,
			DLQRetention:             720 * time.Hour,
		},
		Worker: config.WorkerConfig{
			MaxRetries:          3,
			RetryBaseDelay:      100 * time.Millisecond,
			StreamReadCount:     10,
			StreamBlockTimeout:  2 * time.Second,
			FailureTimeout:      5 * time.Second,
			PendingBlockTimeout: 100 * time.Millisecond,
			ReadErrorBackoff:    1 * time.Second,
		},
		Recon: config.ReconConfig{
			SweepInterval:     120 * time.Second,
			ChargingThreshold: 120 * time.Second,
			GatewayTimeout:    10 * time.Second,
			MaxChargingAge:    24 * time.Hour,
			BatchSize:         100,
		},
		Saga: config.SagaConfig{
			WatchdogInterval: 60 * time.Second,
			StuckThreshold:   60 * time.Second,
			MaxFailedAge:     24 * time.Hour,
			BatchSize:        100,
		},
		InventoryDrift: config.InventoryDriftConfig{
			SweepInterval:     60 * time.Second,
			AbsoluteTolerance: 100,
		},
		// D4.2: production-mode validation requires Provider=stripe +
		// non-empty APIKey + non-empty WebhookSecret. validBase()
		// sets these to test-shaped values so the existing
		// production-guard tests (which override App.Env="" /
		// "production") still pass without needing per-test Stripe
		// setup. The Stripe adapter doesn't validate key format
		// (rk_live_/sk_test_/etc.) at config time — runtime errors
		// surface from Stripe's API call, not Validate().
		Payment: config.PaymentConfig{
			Provider:      "stripe",
			WebhookSecret: "whsec_test_legacy_field",
			Stripe: config.StripePaymentConfig{
				APIKey:        "rk_live_validBaseFakeKey_xxxxxxxxxx",
				WebhookSecret: "whsec_test_validBaseFakeSecret",
			},
		},
		// PR #121: admin SSE stream. JWTMaxTTL > 0 mandatory;
		// JWTSecret must be >= 32 bytes if set, and required in
		// production. validBase() provides a 32-byte fake secret so
		// production-mode tests for other fields don't trip on this
		// guard. Dedicated tests below cover empty / short secret.
		AdminStream: config.AdminStreamConfig{
			JWTSecret: "validBaseFakeJWTSecret_32bytes_!!",
			JWTMaxTTL: time.Hour,
		},
	}
}

func TestValidate_BasePasses(t *testing.T) {
	t.Parallel()
	require.NoError(t, validBase().Validate(),
		"validBase() must pass — adjust if Validate() gains a new required field")
}

// TestValidate_ReconZeroValues exhaustively covers fix #2 from the
// post-A4 review pass: every ReconConfig field MUST be > 0. Without
// these guards a partially-constructed config or an operator typo
// (e.g. RECON_BATCH_SIZE=0 thinking "disable") would produce silent
// reconciler malfunctions — see config.go header for the rationale
// per field.
func TestValidate_ReconZeroValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(c *config.Config)
		expectKey string // substring expected in the error message
	}{
		{"sweep_interval=0", func(c *config.Config) { c.Recon.SweepInterval = 0 }, "sweep_interval"},
		{"charging_threshold=0", func(c *config.Config) { c.Recon.ChargingThreshold = 0 }, "charging_threshold"},
		{"gateway_timeout=0", func(c *config.Config) { c.Recon.GatewayTimeout = 0 }, "gateway_timeout"},
		{"max_charging_age=0", func(c *config.Config) { c.Recon.MaxChargingAge = 0 }, "max_charging_age"},
		{"batch_size=0", func(c *config.Config) { c.Recon.BatchSize = 0 }, "batch_size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validBase()
			tt.mutate(c)

			err := c.Validate()
			require.Error(t, err, "Validate must reject %s", tt.name)
			assert.Contains(t, err.Error(), tt.expectKey,
				"error message must name the offending field for diagnosability")
		})
	}
}

// TestValidate_ReconCrossField covers the cross-field invariant added
// in fix #2: MaxChargingAge MUST exceed ChargingThreshold. Otherwise
// every order recon picks up is immediately force-failed before the
// gateway is queried — a silent mass-failure mode.
func TestValidate_ReconCrossField(t *testing.T) {
	t.Parallel()

	t.Run("equal violates strict > requirement", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.Recon.ChargingThreshold = 60 * time.Second
		c.Recon.MaxChargingAge = 60 * time.Second // equal → should reject

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_charging_age")
		assert.Contains(t, err.Error(), "must be > recon.charging_threshold")
	})

	t.Run("max less than threshold rejects", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.Recon.ChargingThreshold = 5 * time.Minute
		c.Recon.MaxChargingAge = 1 * time.Minute // less → MUST reject

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_charging_age")
	})

	t.Run("max greater than threshold accepts", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.Recon.ChargingThreshold = 2 * time.Minute
		c.Recon.MaxChargingAge = 24 * time.Hour // far greater → accept

		require.NoError(t, c.Validate())
	})
}

// TestValidate_InventoryDriftRejections covers the drift-detector
// guards: SweepInterval > 0 (zero means "no sweeps fire"), and
// AbsoluteTolerance >= 0 (negative is meaningless — would flag every
// healthy event).
func TestValidate_InventoryDriftRejections(t *testing.T) {
	t.Parallel()

	t.Run("SweepInterval=0 rejected", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.InventoryDrift.SweepInterval = 0
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INVENTORY_DRIFT_SWEEP_INTERVAL")
	})

	t.Run("AbsoluteTolerance negative rejected", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.InventoryDrift.AbsoluteTolerance = -1
		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INVENTORY_DRIFT_ABSOLUTE_TOLERANCE")
	})

	t.Run("AbsoluteTolerance=0 accepted (strict mode for tests / idle prod)", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.InventoryDrift.AbsoluteTolerance = 0
		require.NoError(t, c.Validate())
	})
}

// TestValidate_AppEnvProductionGuards covers the env-pair guard added
// for D8: when AppConfig.Env normalises to "production", the
// /test/* endpoint group MUST stay disabled. Empty / whitespace /
// mixed-case Env all normalise to "production" so a literal config
// that bypasses cleanenv's env-default ("") still hits the guard
// (fail-closed). Non-production envs let the demo / integration
// tests enable /test/* freely.
func TestValidate_AppEnvProductionGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         string
		enableTests bool
		wantErr     bool
		errSubstr   string
	}{
		{"empty env normalises to production — test endpoints rejected",
			"", true, true, "ENABLE_TEST_ENDPOINTS"},
		{"whitespace env normalises to production — test endpoints rejected",
			"  ", true, true, "ENABLE_TEST_ENDPOINTS"},
		{"mixed-case PRODUCTION normalises — test endpoints rejected",
			"PRODUCTION", true, true, "ENABLE_TEST_ENDPOINTS"},
		{"production with test endpoints disabled — passes",
			"production", false, false, ""},
		{"empty env with test endpoints disabled — passes (closed default)",
			"", false, false, ""},
		// PR #130 A1 fixup: these PASS cases use the rk_test_* override
		// applied inside the loop below — the literal name now reads as
		// "dev/staging + test-endpoints + (test key)" rather than
		// "dev/staging + test-endpoints + ANY key", because the new
		// env-independent live-key guard would reject the validBase()
		// rk_live_* combo. See TestValidate_TestEndpointsAndLiveKeyGuard
		// for the live-key REJECT axis.
		{"development with test endpoints enabled — passes (test key injected)",
			"development", true, false, ""},
		{"staging with test endpoints enabled — passes (test key injected)",
			"staging", true, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validBase()
			c.App.Env = tt.env
			c.Server.EnableTestEndpoints = tt.enableTests
			// PR #130 A1: validBase() uses rk_live_* (production-shaped).
			// The new env-independent guard would reject every
			// EnableTestEndpoints=true case in this table on the live-key
			// axis. The cases that legitimately *should* pass with test
			// endpoints enabled (dev/staging) must therefore use a test key.
			if tt.enableTests && !tt.wantErr {
				c.Payment.Stripe.APIKey = "rk_test_devOrStagingFakeKey_xxxxxxxx"
			}

			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate must reject %s", tt.name)
				assert.Contains(t, err.Error(), tt.errSubstr,
					"error must name the offending field for diagnosability")
				return
			}
			require.NoError(t, err, "Validate must accept %s", tt.name)
		})
	}
}

// TestValidate_TestEndpointsAndLiveKeyGuard covers PR #130 A1 (CRIT) —
// the env-INDEPENDENT cross-field guard rejecting EnableTestEndpoints=true
// + sk_live_*/rk_live_* in ANY environment. The /test/payment/confirm
// endpoint forges webhooks bypassing payment auth; against a real Stripe
// account that's a payment-confirmation oracle for any guessable order_id.
//
// Distinct from TestValidate_AppEnvProductionGuards, which only covers the
// production-only EnableTestEndpoints rejection. This test specifically
// verifies dev / staging / empty-env all reject the live-key + test-endpoint
// combo too.
func TestValidate_TestEndpointsAndLiveKeyGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         string
		enableTests bool
		apiKey      string
		wantErr     bool
		errSubstr   string
	}{
		// REJECT cases — the live-key + test-endpoint combination is
		// unconditionally rejected regardless of APP_ENV value.
		{"staging + rk_live + test endpoints — REJECT",
			"staging", true, "rk_live_stagingMisconfigured_xxxxxx", true,
			"server.enable_test_endpoints / ENABLE_TEST_ENDPOINTS cannot be true while payment.stripe.api_key"},
		{"staging + sk_live + test endpoints — REJECT",
			"staging", true, "sk_live_stagingMisconfigured_xxxxxx", true,
			"server.enable_test_endpoints / ENABLE_TEST_ENDPOINTS cannot be true while payment.stripe.api_key"},
		{"development + rk_live + test endpoints — REJECT",
			"development", true, "rk_live_devMisconfigured_xxxxxxxxx", true,
			"server.enable_test_endpoints / ENABLE_TEST_ENDPOINTS cannot be true while payment.stripe.api_key"},
		{"empty env + sk_live + test endpoints — REJECT (covered by both production-only and env-independent guards)",
			"", true, "sk_live_emptyEnvLiveKey_xxxxxxxxxx", true,
			"ENABLE_TEST_ENDPOINTS"},

		// ACCEPT cases — either test endpoints are disabled OR the key
		// is a test key.
		{"staging + rk_test + test endpoints — PASS (test key is the safe combo)",
			"staging", true, "rk_test_stagingSafeKey_xxxxxxxxxxxx", false, ""},
		{"staging + sk_test + test endpoints — PASS",
			"staging", true, "sk_test_stagingSafeKey_xxxxxxxxxxxx", false, ""},
		{"staging + rk_live + test endpoints DISABLED — PASS (live key alone is fine without /test/* mounted)",
			"staging", false, "rk_live_stagingNoTestEndpoints_xxx", false, ""},
		{"development + rk_test + test endpoints — PASS",
			"development", true, "rk_test_devSafeKey_xxxxxxxxxxxxxx", false, ""},
		// PR #130 A1 review fixup: document the empty-APIKey escape
		// hatch explicitly. Empty APIKey + test endpoints is the
		// canonical dev-loop combo (PAYMENT_PROVIDER=mock + no Stripe
		// keys). The new guard is prefix-scoped to `sk_live_*`/`rk_live_*`
		// so an empty string slips through — that case is gated by the
		// downstream "STRIPE_API_KEY required when PAYMENT_PROVIDER=stripe"
		// check at config.go:868 (mock provider + empty key + test
		// endpoints is genuinely safe because MockGateway moves no
		// real money).
		{"staging + EMPTY APIKey + test endpoints — PASS (mock provider implied; live-key guard intentionally prefix-scoped)",
			"staging", true, "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validBase()
			c.App.Env = tt.env
			c.Server.EnableTestEndpoints = tt.enableTests
			c.Payment.Stripe.APIKey = tt.apiKey
			// PR #130 review fixup: when apiKey is empty, the test is
			// exercising the prefix-scoped escape hatch — the canonical
			// dev-loop combo is mock provider + no Stripe keys at all,
			// so flip the provider AND clear the webhook secret too.
			// (validBase() defaults to Provider=stripe with non-empty
			// APIKey + WebhookSecret; mock + lingering Stripe credentials
			// trips a separate "likely misconfiguration" guard.)
			if tt.apiKey == "" {
				c.Payment.Provider = "mock"
				c.Payment.Stripe.WebhookSecret = ""
				c.Payment.WebhookSecret = ""
			}

			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate must reject %s", tt.name)
				assert.Contains(t, err.Error(), tt.errSubstr,
					"error must name the offending field for diagnosability")
				return
			}
			require.NoError(t, err, "Validate must accept %s", tt.name)
		})
	}
}

// TestValidate_AggregatesAllMissing confirms multiple missing fields
// are reported in one error rather than failing fast on the first.
// Operators iterating on config files want to see all problems at once.
func TestValidate_AggregatesAllMissing(t *testing.T) {
	t.Parallel()
	c := validBase()
	c.Postgres.DSN = ""
	c.Recon.BatchSize = 0
	c.Worker.MaxRetries = 0

	err := c.Validate()
	require.Error(t, err)
	msg := err.Error()
	for _, substr := range []string{"postgres.dsn", "batch_size", "max_retries"} {
		assert.Contains(t, msg, substr, "Validate must aggregate all problems, not fail-fast")
	}
	// Belt-and-suspenders: verify it really is one error string,
	// not multiple wrapped errors.
	assert.Equal(t, 1, strings.Count(msg, "missing required config fields"))
}

// TestValidate_PaymentProviderWhitelist — D4.2 Slice 2a. Provider
// values are whitelisted at all times (not just production); a
// misspelled `PAYMENT_PROVIDER=stipe` should fail loud, not silently
// fall through to mock.
func TestValidate_PaymentProviderWhitelist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		val     string
		wantErr bool
	}{
		{"mock_accepted", "mock", false},
		{"stripe_accepted", "stripe", false},
		{"empty_treated_as_mock", "", false},
		{"misspelled_stipe_rejected", "stipe", true},
		{"misspelled_strpe_rejected", "strpe", true},
		{"unknown_paypal_rejected", "paypal", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validBase()
			c.App.Env = "development" // bypass production-mode requirements
			c.Payment.Provider = tt.val
			// Round-4 review fix: when testing non-stripe providers
			// (mock / empty), clear Stripe credentials too — the new
			// staging-mock guard refuses the combination of mock-or-
			// empty Provider with present Stripe credentials. The
			// whitelist test is about Provider VALUE shape, not
			// credential presence; clearing keeps the assertion focused.
			if tt.val == "mock" || tt.val == "" {
				c.Payment.Stripe.APIKey = ""
				c.Payment.Stripe.WebhookSecret = ""
				c.Payment.WebhookSecret = ""
			}

			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "PAYMENT_PROVIDER")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidate_ProductionRequiresStripe — D4.2 Slice 2a. Production
// mode REJECTS Provider=mock (silent no-op-payments risk) and
// requires non-empty APIKey + WebhookSecret.
func TestValidate_ProductionRequiresStripe(t *testing.T) {
	t.Parallel()

	t.Run("production_with_mock_rejected", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.App.Env = "production"
		c.Payment.Provider = "mock"

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be 'stripe' in production")
		assert.Contains(t, err.Error(), "MockGateway is a dev-only adapter")
	})

	t.Run("production_with_stripe_no_apikey_rejected", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.App.Env = "production"
		c.Payment.Provider = "stripe"
		c.Payment.Stripe.APIKey = ""

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "STRIPE_API_KEY")
	})

	t.Run("production_with_stripe_no_webhook_secret_rejected", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.App.Env = "production"
		c.Payment.Provider = "stripe"
		c.Payment.Stripe.WebhookSecret = ""
		c.Payment.WebhookSecret = "" // also clear legacy fallback

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "STRIPE_WEBHOOK_SECRET")
	})

	t.Run("production_with_empty_provider_rejected", func(t *testing.T) {
		// Plan-review LOW #8: Provider="" + production-mode must
		// also be rejected (cleanenv's env-default fills "mock"
		// before Validate, but a `&Config{...}` literal that
		// bypasses env loading would have empty here).
		t.Parallel()
		c := validBase()
		c.App.Env = "production"
		c.Payment.Provider = ""

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be 'stripe' in production")
	})

	t.Run("production_with_legacy_webhook_secret_only_passes", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.App.Env = "production"
		c.Payment.Provider = "stripe"
		c.Payment.Stripe.WebhookSecret = ""
		c.Payment.WebhookSecret = "whsec_legacy_present" // backward-compat

		err := c.Validate()
		require.NoError(t, err,
			"legacy PAYMENT_WEBHOOK_SECRET should satisfy the validation "+
				"(LoadConfig promotes it to Stripe.WebhookSecret with a "+
				"deprecation warning; here we test the validator directly "+
				"sees either field as sufficient)")
	})
}

// TestValidate_StagingMockWithStripeKeysRejected — Round-4 multi-agent
// review fix. Production rejects Provider=mock above. The gap: a
// STAGING (or any non-production) deploy where the operator sets
// STRIPE_API_KEY=rk_live_*** + STRIPE_WEBHOOK_SECRET=whsec_*** but
// FORGETS PAYMENT_PROVIDER=stripe boots silently in mock mode — the
// app looks healthy, real keys sit unused, no money moves. Cross-field
// guard fires the moment Stripe credentials are present alongside a
// mock-or-empty provider, regardless of APP_ENV.
func TestValidate_StagingMockWithStripeKeysRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		appEnv         string
		provider       string
		apiKey         string
		webhookSecret  string
		wantErrContain string
	}{
		{
			name:           "staging_mock_with_stripe_apikey",
			appEnv:         "staging",
			provider:       "mock",
			apiKey:         "rk_live_xyz",
			webhookSecret:  "",
			wantErrContain: "Stripe credentials are present",
		},
		{
			name:           "staging_mock_with_webhook_secret",
			appEnv:         "staging",
			provider:       "mock",
			apiKey:         "",
			webhookSecret:  "whsec_xyz",
			wantErrContain: "Stripe credentials are present",
		},
		{
			name:           "development_empty_provider_with_stripe_keys",
			appEnv:         "development",
			provider:       "",
			apiKey:         "sk_test_xyz",
			webhookSecret:  "whsec_xyz",
			wantErrContain: "Stripe credentials are present",
		},
		{
			name:           "staging_mock_no_stripe_keys_passes",
			appEnv:         "staging",
			provider:       "mock",
			apiKey:         "",
			webhookSecret:  "",
			wantErrContain: "", // expect no error
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := validBase()
			c.App.Env = tc.appEnv
			c.Payment.Provider = tc.provider
			c.Payment.Stripe.APIKey = tc.apiKey
			c.Payment.Stripe.WebhookSecret = tc.webhookSecret
			c.Payment.WebhookSecret = "" // clear legacy fallback so it doesn't muddy the assertion

			err := c.Validate()
			if tc.wantErrContain == "" {
				require.NoError(t, err, "case should pass validation")
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrContain)
		})
	}
}

// TestValidate_ProductionRejectsStripeTestKey — D4.2 Slice 2a.
// Production with `sk_test_*` / `rk_test_*` keys silently runs against
// Stripe test mode (money never moves). Must be rejected unless
// STRIPE_ALLOW_TEST_KEY is explicitly set.
func TestValidate_ProductionRejectsStripeTestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		apiKey       string
		allowTestKey bool
		wantErr      bool
	}{
		{"sk_test_rejected", "sk_test_xxxxxx", false, true},
		{"rk_test_rejected", "rk_test_xxxxxx", false, true},
		{"sk_test_allowed_with_opt_in", "sk_test_xxxxxx", true, false},
		{"rk_test_allowed_with_opt_in", "rk_test_xxxxxx", true, false},
		{"sk_live_accepted", "sk_live_xxxxxx", false, false},
		{"rk_live_accepted", "rk_live_xxxxxx", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := validBase()
			c.App.Env = "production"
			c.Payment.Provider = "stripe"
			c.Payment.Stripe.APIKey = tt.apiKey
			c.Payment.Stripe.AllowTestKey = tt.allowTestKey

			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "STRIPE_ALLOW_TEST_KEY")
				return
			}
			require.NoError(t, err)
		})
	}
}

// PR #121 admin SSE stream validation tests. Pair with the
// review-round-1 fix that added JWTSecret + JWTMaxTTL guards to
// Validate().
func TestValidate_AdminStreamRejections(t *testing.T) {
	t.Run("zero_jwt_max_ttl_rejected_when_secret_set", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		// validBase() sets a valid 32-byte secret. With secret set,
		// JWTMaxTTL=0 must be rejected (review round 2 NEW-2: guard
		// only fires when the stream is actually enabled).
		c.AdminStream.JWTMaxTTL = 0

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "admin_stream.jwt_max_ttl")
		assert.Contains(t, err.Error(), "must be > 0 when JWT secret is set")
	})

	t.Run("zero_jwt_max_ttl_ignored_when_secret_empty", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		// With admin stream disabled (no secret), JWTMaxTTL is
		// irrelevant — the middleware 503s every request anyway.
		c.AdminStream.JWTSecret = ""
		c.AdminStream.JWTMaxTTL = 0

		err := c.Validate()
		require.NoError(t, err,
			"JWTMaxTTL=0 + empty secret should not block server startup (admin stream disabled)")
	})

	t.Run("short_jwt_secret_rejected", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.AdminStream.JWTSecret = "too-short"

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "admin_stream.jwt_secret")
		assert.Contains(t, err.Error(), "at least 32 bytes")
	})

	t.Run("empty_secret_allowed_in_dev", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.AdminStream.JWTSecret = "" // empty in non-production: endpoint 503s but config valid

		err := c.Validate()
		require.NoError(t, err,
			"empty JWT secret should be allowed in non-production (middleware 503s)")
	})

	t.Run("empty_secret_rejected_in_production", func(t *testing.T) {
		t.Parallel()
		c := validBase()
		c.App.Env = "production"
		c.AdminStream.JWTSecret = ""

		err := c.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "admin_stream.jwt_secret")
		assert.Contains(t, err.Error(), "required in production")
	})
}
