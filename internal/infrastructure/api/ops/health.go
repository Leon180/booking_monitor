// Package ops is the HTTP boundary for OPERATIONAL endpoints — k8s
// liveness / readiness probes today, future N3 (SLO burn-rate) and
// admin endpoints later. Lives separately from booking/ so that:
//
//   - operational endpoints have their own URL contract (engine root,
//     not /api/v1) — k8s probe targets must not move with API versioning
//   - liveness vs readiness separation (k8s convention) is visible
//     in the package layout, not just at function level
//   - future auth changes that gate admin endpoints land here without
//     touching customer-facing booking code
package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/log"
)

// readinessProbeBudget bounds the WHOLE /readyz probe. 1s is the
// industry middle-ground (Stripe / Cloudflare cite ~1s for in-cluster
// probes): long enough to absorb a Redis Sentinel re-election or a
// brief PG pool spike, short enough that the probe cannot itself
// become a load source. Should be ≤ kubelet's probe timeout (default
// 1s) so per-dep timeouts never silently exceed the kubelet budget.
const readinessProbeBudget = 1 * time.Second

// dependencyCheck is one named ping function. Kept tiny on purpose —
// tests inject fake checks without needing sqlmock / redismock /
// embedded Kafka. The handler's only job is fan-out + aggregation,
// which is what we want to unit-test.
type dependencyCheck struct {
	name string
	ping func(ctx context.Context) error
}

// HealthHandler exposes /livez (process is up) and /readyz (process
// is up AND every required dependency answered within probeBudget).
// The two are deliberately separate per k8s conventions: liveness
// failure restarts the pod; readiness failure only removes it from
// the Service endpoints. Conflating them turns a Redis blip into a
// cluster-wide pod-restart cascade.
type HealthHandler struct {
	checks      []dependencyCheck
	probeBudget time.Duration
}

// NewHealthHandler wires the production checks: Postgres PingContext,
// Redis PING, and a Kafka broker dial. Tests use NewHealthHandlerForTest
// (export_test.go) to inject custom checks and shorten the budget.
func NewHealthHandler(db *sql.DB, rdb *redis.Client, cfg *config.Config) *HealthHandler {
	return &HealthHandler{
		checks: []dependencyCheck{
			{name: "postgres", ping: db.PingContext},
			{name: "redis", ping: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
			{name: "kafka", ping: kafkaBrokerPing(cfg.Kafka.Brokers)},
		},
		probeBudget: readinessProbeBudget,
	}
}

// Live always returns 200. The k8s liveness probe MUST NOT depend on
// downstream services — a Redis outage cannot be allowed to kill every
// pod via livenessProbe. If the process is running enough to answer
// HTTP, it's "alive"; readiness is a separate question.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// dependencyCheckResult is the per-dependency outcome of a readiness
// probe. `Error` is empty on success; on failure it carries the
// caller-safe message.
type dependencyCheckResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Ready returns 200 only if every dependency answered within
// readinessProbeBudget. Each check runs in its own goroutine so the
// total wall-clock is `max(checks)`, not `sum(checks)` — important
// when running behind a kubelet probe timeout.
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.probeBudget)
	defer cancel()

	results := h.runChecks(ctx)

	overall := true
	for _, r := range results {
		if !r.OK {
			overall = false
			break
		}
	}

	status := http.StatusOK
	if !overall {
		status = http.StatusServiceUnavailable
		// Warn so a flapping dependency surfaces without drowning real
		// errors. Dashboard / alerts read /metrics + the 503 body; the
		// log line is for post-mortem traceback.
		log.Warn(c.Request.Context(), "readiness probe failed", log.String("failures", summarizeFailures(results)))
	}

	c.JSON(status, gin.H{
		"status": statusText(overall),
		"checks": results,
	})
}

func statusText(ok bool) string {
	if ok {
		return "ok"
	}
	return "unavailable"
}

// summarizeFailures produces a flat "name=err; ..." string for the
// log line. Avoids a multiline log entry while keeping names + error
// texts visible at the top of the line.
func summarizeFailures(results []dependencyCheckResult) string {
	var b []byte
	for _, r := range results {
		if r.OK {
			continue
		}
		if len(b) > 0 {
			b = append(b, "; "...)
		}
		b = append(b, r.Name...)
		b = append(b, '=')
		b = append(b, r.Error...)
	}
	return string(b)
}

// runChecks fires every check in parallel under a shared ctx. Order
// in the returned slice mirrors h.checks so the JSON body and log
// summaries are stable across runs — easier to grep dashboards.
func (h *HealthHandler) runChecks(ctx context.Context) []dependencyCheckResult {
	results := make([]dependencyCheckResult, len(h.checks))
	var wg sync.WaitGroup
	wg.Add(len(h.checks))

	for i := range h.checks {
		go func(i int) {
			defer wg.Done()
			results[i] = check(h.checks[i].name, h.checks[i].ping(ctx))
		}(i)
	}

	wg.Wait()
	return results
}

func check(name string, err error) dependencyCheckResult {
	if err != nil {
		return dependencyCheckResult{Name: name, OK: false, Error: err.Error()}
	}
	return dependencyCheckResult{Name: name, OK: true}
}

// kafkaBrokerPing returns a ping closure that dials each broker in
// turn and reports OK as soon as one answers. segmentio/kafka-go
// discovers the rest of the cluster via metadata, so any reachable
// broker means the cluster is usable. All-broker-down is the only
// fail; an empty broker list is a deployment misconfiguration and
// fails fast (errNoKafkaBrokers).
//
// On all-broker-down we return errors.Join of every per-broker
// attempt (each wrapped with its addr) so /readyz body and the
// Warn log line tell operators *which* brokers failed and *how* —
// not just "the last one's error". Probe-only conn close errors are
// discarded (`_ = conn.Close()`) because a probe TCP conn is
// ephemeral; cleanup-failure can't change the success outcome and
// piping it into a logger here would couple health-check code to
// the log subsystem on the fast path.
func kafkaBrokerPing(brokers []string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if len(brokers) == 0 {
			return errNoKafkaBrokers
		}
		errs := make([]error, 0, len(brokers))
		for _, addr := range brokers {
			conn, err := kafka.DialContext(ctx, "tcp", addr)
			if err != nil {
				errs = append(errs, fmt.Errorf("broker %s: %w", addr, err))
				continue
			}
			_ = conn.Close()
			return nil
		}
		return errors.Join(errs...)
	}
}

// RegisterHealthRoutes wires /livez and /readyz at the engine root
// (NOT under /api/v1) — k8s probes target a stable path that
// shouldn't move with API versioning, and operators expect these
// at the root for grep-ability.
func RegisterHealthRoutes(r *gin.Engine, h *HealthHandler) {
	r.GET("/livez", h.Live)
	r.GET("/readyz", h.Ready)
}

// errNoKafkaBrokers fires when config has no brokers configured —
// deployment misconfiguration, not a transient outage; fail fast.
var errNoKafkaBrokers = errors.New("no kafka brokers configured")
