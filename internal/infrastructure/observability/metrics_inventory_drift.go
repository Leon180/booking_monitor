package observability

// PR-D Inventory drift detector metrics. The application-side
// `recon.DriftMetrics` interface + the `bootstrap.NewPrometheusDriftMetrics()`
// adapter forward into these vars; recon application code never imports
// this package directly (CP2 layer-fix).
//
// Cache-truth architecture context: PR-A established that Redis is
// ephemeral and DB is source-of-truth (Makefile reset uses precise DEL,
// preserves stream + group). PR-B added rehydrate-on-startup. PR-C
// added the NOGROUP self-heal alert. This PR-D set closes the loop —
// detection of any drift that develops between the two stores during
// normal operation, so the operator gets paged BEFORE customers start
// hitting "sold out" while DB still shows inventory (or the inverse).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// InventoryDriftDetectedTotal is a per-direction counter — every event
// flagged by InventoryDriftDetector.checkOne increments the counter
// labelled by the failure mode. Labels:
//
//	"cache_missing"     — DB has inventory, Redis returns 0 (key absent).
//	                      Means rehydrate didn't run or was incomplete.
//	"cache_high"        — Redis > DB (drift < 0). Saga-compensation desync
//	                      or manual SetInventory beyond reset baseline.
//	                      Always anomalous regardless of magnitude.
//	"cache_low_excess"  — Redis < DB by more than DriftConfig.AbsoluteTolerance.
//	                      Steady-state drift is positive (in-flight bookings);
//	                      excess means worker isn't committing or recon
//	                      force-fail leaks inventory.
//
// Pairs with the alert `InventoryDriftDetected` — `increase[5m] > 0`
// fires for any sustained drift signal. `for: 5m` suppresses the
// transient in-flight blip from a single sweep.
//
// Cardinality: 3 labels × 1 metric = bounded. Per-event details go to
// logs (event_id), NOT metric labels — at 1k+ events the cardinality
// explosion would dominate Prometheus's memory.
var InventoryDriftDetectedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "inventory_drift_detected_total",
		Help: "Total drift-detection events flagged by the inventory drift detector, by direction (cache_missing | cache_high | cache_low_excess)",
	},
	[]string{"direction"},
)

// InventoryDriftedEventsCount is a point-in-time gauge of events
// flagged in the latest sweep. Set (not Add) — every sweep overwrites.
// 0 in the steady state; sustained > 0 means at least one event has
// persistent drift (the Counter above tells which direction).
var InventoryDriftedEventsCount = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "inventory_drifted_events_count",
		Help: "Events flagged by the latest drift sweep (point-in-time, set on each sweep)",
	},
)

// InventoryDriftListEventsErrorsTotal increments when the per-sweep
// EventRepository.ListAvailable query itself fails. Distinct from
// per-event cache failures (InventoryDriftCacheReadErrorsTotal) so
// dashboards can tell "drift detection itself is broken" from "Redis
// is flaky for some events". Mirrors ReconFindStuckErrorsTotal.
var InventoryDriftListEventsErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "inventory_drift_list_events_errors_total",
		Help: "Total ListAvailable query failures during drift sweep (DB outage, missing index, timeout)",
	},
)

// InventoryDriftCacheReadErrorsTotal increments on per-event Redis
// GET failures during a drift sweep. The sweep continues so other
// events can still be checked. A sustained rate alongside healthy
// `redis_pool_*` metrics suggests a hot-shard issue or a specific
// key contention rather than Redis-wide degradation.
var InventoryDriftCacheReadErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "inventory_drift_cache_read_errors_total",
		Help: "Total Redis GetInventory failures during drift sweep (sweep continues; other events still checked)",
	},
)

// InventoryDriftSweepDurationSeconds is a histogram of one full sweep
// (ListAvailable + per-event GET). At ~1ms per event in steady state
// (Redis local + LIMIT 1k), expect p99 well under 1s. p99 climbing
// suggests Redis pool exhaustion or a slow ListAvailable (DB).
var InventoryDriftSweepDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "inventory_drift_sweep_duration_seconds",
		Help:    "Wall-clock duration of one inventory drift detection sweep",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)
