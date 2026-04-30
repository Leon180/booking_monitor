package recon_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"booking_monitor/internal/application/recon"
)

// TestConfigValidate covers every rejection branch + the happy path.
// Each invalid case is a real misconfiguration shape an operator might
// produce (`RECON_BATCH_SIZE=0` thinking "disable", a partial struct
// literal in a test that bypasses cleanenv defaults). Pinning these
// keeps a future refactor honest.
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := recon.Config{
		SweepInterval:     120 * time.Second,
		ChargingThreshold: 120 * time.Second,
		GatewayTimeout:    10 * time.Second,
		MaxChargingAge:    24 * time.Hour,
		BatchSize:         100,
	}

	cases := []struct {
		name        string
		mutate      func(c *recon.Config)
		wantErr     bool
		errFragment string
	}{
		{name: "valid config passes", mutate: func(*recon.Config) {}, wantErr: false},

		{name: "SweepInterval=0 rejected",
			mutate: func(c *recon.Config) { c.SweepInterval = 0 },
			wantErr: true, errFragment: "SweepInterval"},

		{name: "SweepInterval negative rejected",
			mutate: func(c *recon.Config) { c.SweepInterval = -1 * time.Second },
			wantErr: true, errFragment: "SweepInterval"},

		{name: "ChargingThreshold=0 rejected",
			mutate: func(c *recon.Config) { c.ChargingThreshold = 0 },
			wantErr: true, errFragment: "ChargingThreshold"},

		{name: "GatewayTimeout=0 rejected",
			mutate: func(c *recon.Config) { c.GatewayTimeout = 0 },
			wantErr: true, errFragment: "GatewayTimeout"},

		{name: "MaxChargingAge equal to ChargingThreshold rejected",
			mutate: func(c *recon.Config) { c.MaxChargingAge = c.ChargingThreshold },
			wantErr: true, errFragment: "MaxChargingAge"},

		{name: "MaxChargingAge less than ChargingThreshold rejected",
			mutate: func(c *recon.Config) { c.MaxChargingAge = 60 * time.Second },
			wantErr: true, errFragment: "MaxChargingAge"},

		{name: "BatchSize=0 rejected",
			mutate: func(c *recon.Config) { c.BatchSize = 0 },
			wantErr: true, errFragment: "BatchSize"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Value copy — safe AS LONG AS recon.Config has no pointer
			// or slice fields. If a future field violates that, this
			// table-driven setup leaks mutations across subtests.
			c := valid
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, recon.ErrInvalidReconConfig),
					"validation errors must wrap ErrInvalidReconConfig so callers can errors.Is them")
				if tc.errFragment != "" {
					assert.Contains(t, err.Error(), tc.errFragment,
						"error message must mention the offending field for operator triage")
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
