package observability

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// ResetAdminStreamResourceGaugesForTest re-arms the registerOnce
// guard and unregisters the three previously-installed GaugeFunc
// callbacks from the default registry so a follow-up test can call
// RegisterAdminStreamResourceGauges with its own accessors and
// re-register cleanly.
//
// Test-only. Production code MUST NOT call this — the registerOnce
// guard exists precisely to prevent re-registration races.
//
// Why a function instead of a //go:build !production guard: the
// observability package has no production build tag in this codebase,
// and a test-only build tag would require duplicating it on every
// test file in the package. The cost-benefit of leaving this exported
// is low — it does not change production behavior, only adds a
// reset escape hatch for tests that need to verify the bool-return
// contract or the Warn-on-double-call behavior.
func ResetAdminStreamResourceGaugesForTest() {
	if AdminSSEEstimatedMemoryBytes != nil {
		prometheus.DefaultRegisterer.Unregister(AdminSSEEstimatedMemoryBytes)
		AdminSSEEstimatedMemoryBytes = nil
	}
	if AdminEventBusChannelHighWatermark != nil {
		prometheus.DefaultRegisterer.Unregister(AdminEventBusChannelHighWatermark)
		AdminEventBusChannelHighWatermark = nil
	}
	if AdminHubBroadcastHighWatermark != nil {
		prometheus.DefaultRegisterer.Unregister(AdminHubBroadcastHighWatermark)
		AdminHubBroadcastHighWatermark = nil
	}
	registerOnce = sync.Once{}
}
