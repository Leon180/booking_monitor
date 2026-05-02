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
		App:    config.AppConfig{},
		Server: config.ServerConfig{Port: "8080"},
		Postgres: config.PostgresConfig{
			DSN: "postgres://u:p@h/db?sslmode=disable",
		},
		Redis: config.RedisConfig{
			MaxConsecutiveReadErrors: 30,
			DLQRetention:             720 * time.Hour,
			InventoryShards:          1,
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
