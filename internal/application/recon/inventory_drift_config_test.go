package recon_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"booking_monitor/internal/application/recon"
)

// TestDriftConfigValidate covers every rejection branch and the happy
// path. Same pinning shape as TestConfigValidate — every misconfiguration
// an operator might produce should fail at startup, not silently sweep
// with broken tunables.
func TestDriftConfigValidate(t *testing.T) {
	t.Parallel()

	valid := recon.DriftConfig{
		SweepInterval:     60 * time.Second,
		AbsoluteTolerance: 100,
	}

	cases := []struct {
		name        string
		mutate      func(c *recon.DriftConfig)
		wantErr     bool
		errFragment string
	}{
		{name: "valid config passes", mutate: func(*recon.DriftConfig) {}, wantErr: false},

		{name: "SweepInterval=0 rejected",
			mutate:  func(c *recon.DriftConfig) { c.SweepInterval = 0 },
			wantErr: true, errFragment: "SweepInterval"},

		{name: "SweepInterval negative rejected",
			mutate:  func(c *recon.DriftConfig) { c.SweepInterval = -1 * time.Second },
			wantErr: true, errFragment: "SweepInterval"},

		{name: "AbsoluteTolerance=0 allowed (strict mode for tests / idle prod)",
			mutate:  func(c *recon.DriftConfig) { c.AbsoluteTolerance = 0 },
			wantErr: false},

		{name: "AbsoluteTolerance negative rejected",
			mutate:  func(c *recon.DriftConfig) { c.AbsoluteTolerance = -1 },
			wantErr: true, errFragment: "AbsoluteTolerance"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := valid
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr {
				assert.Error(t, err)
				assert.True(t, errors.Is(err, recon.ErrInvalidDriftConfig),
					"every error must wrap ErrInvalidDriftConfig so callers can branch with errors.Is")
				assert.Contains(t, err.Error(), tc.errFragment,
					"error message must name the offending field")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
