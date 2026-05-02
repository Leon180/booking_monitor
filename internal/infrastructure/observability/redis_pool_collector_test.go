package observability

import (
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedisPoolCollector_DescribesAllMetrics pins the contract that the
// collector publishes all 9 metrics from go-redis PoolStats. A future
// go-redis upgrade that adds new fields to pool.Stats won't auto-leak
// into our metric set — adding a new metric is a deliberate change.
func TestRedisPoolCollector_DescribesAllMetrics(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	c := NewRedisPoolCollector(rdb)

	ch := make(chan *prometheus.Desc, 16)
	go func() {
		c.Describe(ch)
		close(ch)
	}()

	got := make(map[string]bool)
	for d := range ch {
		got[d.String()] = true
	}

	// One descriptor per metric the collector publishes — pinned by name
	// so a future field added to pool.Stats requires an explicit edit
	// here too.
	wantNames := []string{
		"redis_client_pool_total_conns",
		"redis_client_pool_idle_conns",
		"redis_client_pool_stale_conns",
		"redis_client_pool_hits_total",
		"redis_client_pool_misses_total",
		"redis_client_pool_timeouts_total",
		"redis_client_pool_wait_count_total",
		"redis_client_pool_wait_duration_seconds_total",
		"redis_client_pool_unusable_total",
	}

	assert.Len(t, got, len(wantNames), "Describe must emit exactly %d descriptors", len(wantNames))
	for _, name := range wantNames {
		found := false
		for desc := range got {
			if strings.Contains(desc, `"`+name+`"`) {
				found = true
				break
			}
		}
		assert.True(t, found, "missing metric: %s", name)
	}
}

// TestRedisPoolCollector_CollectAfterPing verifies that after an actual
// Redis call (which forces the pool to allocate a connection), the
// counters move in the expected direction. This is the only way to
// distinguish "collector wired correctly" from "collector emits zeros
// for everything regardless of state" — a regression we lost once
// before in the DB pool collector when the wrong field was scanned.
func TestRedisPoolCollector_CollectAfterPing(t *testing.T) {
	t.Parallel()

	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// Drive the pool — Ping forces connection creation + return to pool.
	require.NoError(t, rdb.Ping(t.Context()).Err())

	c := NewRedisPoolCollector(rdb)
	values := snapshotMetrics(t, c)

	// total_conns must be >=1 after a Ping.
	assert.GreaterOrEqual(t, values["redis_client_pool_total_conns"], 1.0,
		"Ping should leave at least one connection in the pool")

	// hits OR misses must have moved (the first call always misses,
	// subsequent calls within the same pool-lifetime hit).
	hits := values["redis_client_pool_hits_total"]
	misses := values["redis_client_pool_misses_total"]
	assert.GreaterOrEqual(t, hits+misses, 1.0,
		"Ping must leave evidence in hits OR misses")

	// timeouts MUST stay at 0 in the absence of contention. A non-zero
	// reading on a fresh pool with no concurrent load would indicate
	// a wiring bug.
	assert.Equal(t, 0.0, values["redis_client_pool_timeouts_total"],
		"no timeouts expected on an idle pool")
}

// snapshotMetrics gathers the collector once into an isolated registry
// and returns a name→value map. Replaces an earlier per-metric helper
// that summed Counter and Gauge proto fields together — that worked by
// proto-default-zero coincidence, but was opaque enough to confuse the
// next reader. Reading the proto type explicitly (`MetricType_GAUGE`
// vs `MetricType_COUNTER`) makes the extraction grep-able.
func snapshotMetrics(t *testing.T, c prometheus.Collector) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))

	families, err := reg.Gather()
	require.NoError(t, err)

	out := make(map[string]float64, len(families))
	for _, mf := range families {
		if len(mf.Metric) == 0 {
			continue
		}
		m := mf.Metric[0]
		switch mf.GetType() {
		case dto.MetricType_GAUGE:
			out[mf.GetName()] = m.GetGauge().GetValue()
		case dto.MetricType_COUNTER:
			out[mf.GetName()] = m.GetCounter().GetValue()
		default:
			t.Fatalf("unexpected metric type %v for %q — collector should only emit gauge or counter", mf.GetType(), mf.GetName())
		}
	}
	return out
}
