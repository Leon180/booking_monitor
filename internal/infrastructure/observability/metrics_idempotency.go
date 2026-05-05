package observability

// Idempotency + cache metrics: hit/miss counters across all caches,
// dedicated infra-error counter for the idempotency cache, and the
// replay-outcome counter (Stripe-style fingerprint match / mismatch /
// legacy entries).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CacheHitsTotal / CacheMissesTotal track every cache lookup that
// distinguishes a hit from a miss. Labelled by `cache` so we can
// scale to multiple caches later (today only "idempotency" reads
// before-or-after; future shapes — event detail cache, user
// session — get their own label values).
//
// Hit-rate alerting is the primary use:
//
//   rate(cache_hits_total[5m]) /
//   (rate(cache_hits_total[5m]) + rate(cache_misses_total[5m]))
//
// A sustained drop in this ratio is the canonical "cache cold" /
// "cache wrong-keyed" / "cache being bypassed" signal — none of
// which surface in latency or error metrics until they're severe.
var CacheHitsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cache_hits_total",
		Help: "Total number of cache lookups that returned a cached value, labelled by cache name",
	},
	[]string{"cache"},
)

var CacheMissesTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cache_misses_total",
		Help: "Total number of cache lookups that did not find a cached value, labelled by cache name",
	},
	[]string{"cache"},
)

// IdempotencyCacheGetErrorsTotal counts infrastructure failures on
// the idempotency cache GET path (Redis down, unmarshal fail).
// Distinct from cache_hits/misses on purpose: an infra error must
// NOT inflate the miss rate, but it also must NOT vanish from
// observability — a sustained non-zero rate means idempotency
// guarantees are SUSPENDED for incoming requests during the
// outage (the handler fails open to preserve availability;
// duplicate-charge protection downgrades to whatever DB-level
// uniqueness constraints exist).
//
// Page-worthy: rate > 0 sustained for 1m means a Redis outage
// affecting a financial-correctness control. The companion
// runbook in monitoring.md explains operator response.
//
// Naming history: this counter pre-dates the generic
// `CacheErrorsTotal{cache,op}` introduced for the ticket_type cache.
// Kept on its own series for backward compatibility with existing
// alert rules; new caches use the labelled counter below.
var IdempotencyCacheGetErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "idempotency_cache_get_errors_total",
		Help: "Total number of idempotency cache GET infra failures (Redis down, unmarshal). Sustained non-zero means idempotency protection is suspended.",
	},
)

// CacheErrorsTotal counts infrastructure failures on the cache layer,
// labelled by `cache` (which cache) and `op` (which operation).
// Generalisation of IdempotencyCacheGetErrorsTotal: the per-cache
// dedicated counter pattern doesn't scale once you have N caches with
// 3+ failure modes each (get / set / marshal). One labelled series
// covers them cleanly.
//
// Why split from `cache_hits_total` / `cache_misses_total`: an infra
// error must NOT inflate the miss rate during a Redis outage —
// otherwise a Redis blip looks like a cache-cold spike on the hit-rate
// dashboard, masking the real cause (Redis itself).
//
// `op` label values:
//
//   - "get"     — Redis GET failure (network, AUTH, OOM)
//   - "set"     — Redis SET failure (same shape as get; different op
//                 because operator runbooks differ — SET stalls
//                 specifically point at maxmemory policy)
//   - "marshal" — JSON marshal of the cache entry failed. Theoretical
//                 for a fixed-shape DTO, but a future field-add could
//                 trip it; surfacing the metric makes the regression
//                 immediately visible instead of "p95 mysteriously
//                 climbed".
//
// Page-worthy: rate("get" or "set") > 0 sustained for 1m means a
// Redis outage degrading the affected cache. Marshal errors should
// be 0 in steady state; rate > 0 means a code regression.
var CacheErrorsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cache_errors_total",
		Help: "Total number of cache infra failures, labelled by cache name + operation (get / set / marshal). Distinct from cache_misses to keep miss rate uncontaminated by Redis outages.",
	},
	[]string{"cache", "op"},
)

// IdempotencyReplaysTotal tracks the outcome of every Idempotency-
// Key cache HIT (added in N4 / PR-fingerprint). The `outcome`
// label values are intentionally narrow:
//
//   - "match"        — same key + same body → cached response replayed
//   - "mismatch"     — same key + different body → 409 Conflict
//                      (Stripe-style; client error, but worth
//                      tracking because a sustained mismatch rate
//                      means a misbehaving client is reusing keys
//                      across logically-distinct requests)
//   - "legacy_match" — cached entry has no fingerprint (pre-N4
//                      data); replayed + fingerprint written back
//                      lazily. Should taper to ~0 within the 24h
//                      cache TTL after N4 deploys; sustained
//                      non-zero means something is keeping the
//                      pre-N4 wire format alive
//
// This is a programmer-error / migration-progress signal, NOT a
// page-worthy metric — no alert wired today. Operators can dashboard
// the rate by outcome to spot client misuse or migration stalls.
var IdempotencyReplaysTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "idempotency_replays_total",
		Help: "Total number of Idempotency-Key cache hits, labelled by outcome (match / mismatch / legacy_match)",
	},
	[]string{"outcome"},
)
