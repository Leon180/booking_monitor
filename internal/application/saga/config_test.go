package saga_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"booking_monitor/internal/application/saga"
)

// TestConfigValidate covers every rejection branch + the happy path.
// Each invalid case is a real misconfiguration shape an operator might
// produce (`SAGA_BATCH_SIZE=0` thinking "disable", a partial struct
// literal in a test that bypasses cleanenv defaults). Pinning these
// keeps a future refactor honest.
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := saga.Config{
		WatchdogInterval: 60 * time.Second,
		StuckThreshold:   60 * time.Second,
		MaxFailedAge:     24 * time.Hour,
		BatchSize:        100,
	}

	cases := []struct {
		name    string
		mutate  func(c *saga.Config)
		wantErr bool
		// errFragment is asserted to appear in err.Error() — pinned so
		// a future refactor that changes the "validate" wording would
		// surface as a test failure (not as silent operator confusion).
		errFragment string
	}{
		{name: "valid config passes", mutate: func(*saga.Config) {}, wantErr: false},

		{name: "WatchdogInterval=0 rejected",
			mutate: func(c *saga.Config) { c.WatchdogInterval = 0 },
			wantErr: true, errFragment: "WatchdogInterval"},

		{name: "WatchdogInterval negative rejected",
			mutate: func(c *saga.Config) { c.WatchdogInterval = -1 * time.Second },
			wantErr: true, errFragment: "WatchdogInterval"},

		{name: "StuckThreshold=0 rejected",
			mutate: func(c *saga.Config) { c.StuckThreshold = 0 },
			wantErr: true, errFragment: "StuckThreshold"},

		{name: "MaxFailedAge equal to StuckThreshold rejected",
			mutate: func(c *saga.Config) { c.MaxFailedAge = c.StuckThreshold },
			wantErr: true, errFragment: "MaxFailedAge"},

		{name: "MaxFailedAge less than StuckThreshold rejected",
			mutate: func(c *saga.Config) { c.MaxFailedAge = 30 * time.Second },
			wantErr: true, errFragment: "MaxFailedAge"},

		{name: "BatchSize=0 rejected",
			mutate: func(c *saga.Config) { c.BatchSize = 0 },
			wantErr: true, errFragment: "BatchSize"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Value copy — safe AS LONG AS saga.Config has no pointer
			// or slice fields. If a future field violates that, this
			// table-driven setup leaks mutations across subtests.
			c := valid
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, saga.ErrInvalidSagaConfig),
					"validation errors must wrap ErrInvalidSagaConfig so callers can errors.Is them")
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
