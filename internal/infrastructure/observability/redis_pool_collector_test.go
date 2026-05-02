package observability

import (
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

	// total_conns must be >=1 after a Ping.
	totalConns := testutil.ToFloat64(metricFromCollector(t, c, "redis_client_pool_total_conns"))
	assert.GreaterOrEqual(t, totalConns, 1.0, "Ping should leave at least one connection in the pool")

	// hits OR misses must have moved (the first call always misses,
	// subsequent calls within the same pool-lifetime hit).
	hits := testutil.ToFloat64(metricFromCollector(t, c, "redis_client_pool_hits_total"))
	misses := testutil.ToFloat64(metricFromCollector(t, c, "redis_client_pool_misses_total"))
	assert.GreaterOrEqual(t, hits+misses, 1.0, "Ping must leave evidence in hits OR misses")

	// timeouts MUST stay at 0 in the absence of contention. A non-zero
	// reading on a fresh pool with no concurrent load would indicate
	// a wiring bug.
	timeouts := testutil.ToFloat64(metricFromCollector(t, c, "redis_client_pool_timeouts_total"))
	assert.Equal(t, 0.0, timeouts, "no timeouts expected on an idle pool")
}

// metricFromCollector is a small testutil helper that registers the
// collector against an isolated registry and pulls one named gauge or
// counter back as a prometheus.Collector for testutil.ToFloat64.
//
// We can't pass the RedisPoolCollector directly to testutil.ToFloat64
// because that helper wants a *single-metric* collector; ours emits 9.
// Routing through a dedicated registry + Gather + filter gives us the
// per-metric extraction.
func metricFromCollector(t *testing.T, c prometheus.Collector, name string) prometheus.Collector {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range families {
		if mf.GetName() == name {
			// Wrap the single-metric value in a tiny in-memory collector
			// so testutil.ToFloat64 can read it.
			g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: "test extract"})
			g.Set(mf.Metric[0].GetCounter().GetValue() + mf.Metric[0].GetGauge().GetValue())
			return g
		}
	}
	t.Fatalf("metric %q not found in collector output", name)
	return nil
}
