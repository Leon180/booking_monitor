package observability

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

// readGaugeFuncValue extracts the current value from a GaugeFunc by
// asking it to Write into a Metric proto. This is the canonical way
// to inspect a registered Prometheus metric without scraping /metrics.
func readGaugeFuncValue(t *testing.T, g prometheus.GaugeFunc) int64 {
	t.Helper()
	if g == nil {
		t.Fatal("GaugeFunc is nil")
	}
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("GaugeFunc.Write: %v", err)
	}
	if m.Gauge == nil || m.Gauge.Value == nil {
		t.Fatal("GaugeFunc.Write returned nil Gauge / Value")
	}
	return int64(*m.Gauge.Value)
}

func TestRegisterAdminStreamResourceGauges_FirstCallReturnsTrue(t *testing.T) {
	ResetAdminStreamResourceGaugesForTest()
	t.Cleanup(ResetAdminStreamResourceGaugesForTest)

	ok := RegisterAdminStreamResourceGauges(AdminStreamResourceSources{
		ActiveClients:         func() int64 { return 0 },
		ClientSendCapacity:    100,
		AvgMsgSize:            256,
		BusChannelHighWater:   func() int64 { return 0 },
		HubBroadcastHighWater: func() int64 { return 0 },
	})

	if !ok {
		t.Fatal("first RegisterAdminStreamResourceGauges call returned false; expected true (sync.Once primed by an earlier test or reset failed)")
	}
	if AdminSSEEstimatedMemoryBytes == nil {
		t.Error("AdminSSEEstimatedMemoryBytes is nil after registration")
	}
	if AdminEventBusChannelHighWatermark == nil {
		t.Error("AdminEventBusChannelHighWatermark is nil after registration")
	}
	if AdminHubBroadcastHighWatermark == nil {
		t.Error("AdminHubBroadcastHighWatermark is nil after registration")
	}
}

func TestRegisterAdminStreamResourceGauges_SecondCallReturnsFalse(t *testing.T) {
	ResetAdminStreamResourceGaugesForTest()
	t.Cleanup(ResetAdminStreamResourceGaugesForTest)

	srcA := AdminStreamResourceSources{
		ActiveClients:         func() int64 { return 1 },
		ClientSendCapacity:    100,
		AvgMsgSize:            256,
		BusChannelHighWater:   func() int64 { return 11 },
		HubBroadcastHighWater: func() int64 { return 21 },
	}
	srcB := AdminStreamResourceSources{
		ActiveClients:         func() int64 { return 999 },
		ClientSendCapacity:    100,
		AvgMsgSize:            256,
		BusChannelHighWater:   func() int64 { return 999 },
		HubBroadcastHighWater: func() int64 { return 999 },
	}

	if ok := RegisterAdminStreamResourceGauges(srcA); !ok {
		t.Fatal("first call returned false")
	}
	if ok := RegisterAdminStreamResourceGauges(srcB); ok {
		t.Fatal("second call returned true; expected false — sync.Once guard regressed")
	}

	// Verify that the first-call accessors are still wired (HWM=11, not 999).
	// Use a synthetic Prometheus metric Write to inspect the registered value.
	gotBusHWM := readGaugeFuncValue(t, AdminEventBusChannelHighWatermark)
	if gotBusHWM != 11 {
		t.Errorf("bus HWM gauge bound to wrong accessor: got %d, want 11 (first call's value) — second call silently overrode the first", gotBusHWM)
	}
}
