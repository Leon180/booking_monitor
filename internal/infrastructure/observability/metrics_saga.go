package observability

// A5 Saga Watchdog metrics. Symmetric counterpart to recon: reconciler
// resolves stuck-Charging via gateway probes; watchdog resolves
// stuck-Failed via compensator re-drive. Counter / gauge / histogram
// naming mirrors the recon pattern so dashboards / alerts can be
// cookie-cuttered.
//
// The application-side `saga.Metrics` interface +
// `bootstrap.NewPrometheusSagaMetrics()` adapter forward into these
// vars; saga application code never imports this package directly
// (CP2 layer-fix).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SagaStuckFailedOrders is a gauge of orders currently in Failed
// state older than SAGA_STUCK_THRESHOLD. Reset to the latest count on
// every watchdog sweep (point-in-time, not cumulative). Pairs with
// the alert `saga_stuck_failed_orders > 0 for 10m` — sustained value
// = compensator falling behind or saga consumer is silently dropping
// events.
var SagaStuckFailedOrders = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "saga_stuck_failed_orders",
		Help: "Failed orders older than SAGA_STUCK_THRESHOLD, set on each watchdog sweep",
	},
)

// SagaWatchdogResolvedTotal counts watchdog resolution outcomes by
// label. Pre-warmed at startup so dashboards don't show "no data"
// for a label that genuinely had zero events.
//
// Outcomes (each maps to a distinct operator runbook so triage points
// at the right subsystem; pre-fixup-review a single `compensator_error`
// label conflated three failure modes):
//
//	"compensated"        — compensator successfully re-drove the
//	                       order from Failed → Compensated
//	"already_compensated" — race won by the saga consumer between
//	                       FindStuckFailed and the watchdog's
//	                       GetByID; row already Compensated. Benign.
//	"max_age_exceeded"   — order older than SAGA_MAX_FAILED_AGE; we
//	                       log + count + alert but do NOT auto-
//	                       transition. Operator investigates manually.
//	"getbyid_error"      — orderRepo.GetByID failed before we reached
//	                       the compensator. Operator should check DB
//	                       health, NOT Redis or compensator code.
//	"marshal_error"      — json.Marshal of synthesized OrderFailedEvent
//	                       failed. Theoretical for the fixed-shape
//	                       struct today; isolated label so a future
//	                       regression is visible.
//	"compensator_error"  — compensator.HandleOrderFailed returned an
//	                       error. Operator should check Redis revert
//	                       path + DB lock contention. Will retry next
//	                       sweep.
var SagaWatchdogResolvedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "saga_watchdog_resolved_total",
		Help: "Total saga watchdog resolution outcomes",
	},
	[]string{"outcome"},
)

// SagaWatchdogFindStuckErrorsTotal — per-sweep FindStuckFailed query
// failures. Same gap-closing role as ReconFindStuckErrorsTotal: a
// stale gauge alone can't distinguish "no orders are stuck" from
// "the watchdog itself is broken". This counter is the
// "watchdog-itself-broken" signal.
var SagaWatchdogFindStuckErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "saga_watchdog_find_stuck_errors_total",
		Help: "Total FindStuckFailed query failures (DB outage, missing index, timeout)",
	},
)

// SagaWatchdogResolveDurationSeconds — wall-clock for resolving one
// stuck-Failed order via the compensator. Pairs with the recon
// equivalent for symmetric tuning dashboards.
var SagaWatchdogResolveDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "saga_watchdog_resolve_duration_seconds",
		Help:    "Wall-clock duration of a single saga watchdog resolve operation",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)

// SagaWatchdogResolveAgeSeconds — age of a stuck-Failed order at the
// moment the watchdog picked it up. Drives SAGA_STUCK_THRESHOLD
// tuning: if p50 is consistently 90s while threshold is 60s, the
// watchdog is catching orders 30s after the threshold — likely the
// saga consumer's normal Failed→Compensated path is slow and we
// should INCREASE the threshold (avoid stealing from the consumer).
var SagaWatchdogResolveAgeSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "saga_watchdog_resolve_age_seconds",
		Help:    "Age of stuck-Failed orders at the moment the watchdog resolves them",
		Buckets: []float64{30, 60, 90, 120, 180, 300, 600, 1800, 3600, 21600, 86400}, // 30s … 24h
	},
)

// ──────────────────────────────────────────────────────────────────
// SAGA COMPENSATOR (Kafka-driven hot path) metrics — PR-D12.4
// ──────────────────────────────────────────────────────────────────
//
// Symmetric counterparts to the watchdog metrics above, but for
// the OTHER saga subsystem: the Kafka-driven consumer that reads
// `order.failed` events and runs the compensator. Pre-D12.4 this
// hot path was uninstrumented. The application-side
// `saga.CompensatorMetrics` interface forwards each method here
// via the `prometheusCompensatorMetrics` adapter in
// `internal/bootstrap/sweeper_adapters.go`.

// SagaCompensatorEventsTotal counts compensator outcomes by label.
// Pre-warmed in metrics_init.go so dashboards show 0 for outcomes
// that genuinely had zero events (vs "no data" before any traffic).
//
// Outcomes (each maps to a distinct operator runbook so triage
// points at the right subsystem; deliberately mirrors
// SagaWatchdogResolvedTotal's outcome-label discipline):
//
//	"compensated"           — full-path success: UoW committed +
//	                          Redis reverted
//	"already_compensated"   — Kafka at-least-once redelivery; the
//	                          UoW short-circuit at compensator.go's
//	                          OrderStatusCompensated check fired.
//	                          Benign but visible — sustained > 0
//	                          rate signals upstream is republishing
//	                          frequently
//	"path_c_skipped"        — `resolveTicketTypeID` returned
//	                          `uuid.Nil`. Multi-ticket-type future
//	                          OR rolling-upgrade legacy event with
//	                          missing `TicketTypeID`. UoW + Redis
//	                          revert both skipped; MarkCompensated
//	                          still applied. ALERT if > 0
//	"unmarshal_error"       — JSON unmarshal failed (poison
//	                          message). Consumer DLQs separately
//	                          via DLQMessagesTotal
//	"getbyid_error"         — `orderRepo.GetByID` failed inside
//	                          the UoW. DB outage / index drift
//	"list_ticket_type_error" — `ticketTypeRepo.ListByEventID` failed
//	                          during Path B legacy fallback. Same
//	                          DB-outage class as getbyid_error but
//	                          isolated label so triage can scope to
//	                          the rolling-upgrade fallback path
//	"incrementticket_error" — `TicketType.IncrementTicket` failed
//	                          inside the UoW. CHECK constraint
//	                          violation (over-increment) or DB
//	                          outage
//	"markcompensated_error" — `Order.MarkCompensated` failed
//	                          inside the UoW. State machine
//	                          rejection or DB outage
//	"redis_revert_error"    — `RevertInventory` failed (post-UoW)
//	                          on a FRESH compensation. Redis
//	                          outage; the UoW already committed
//	                          so the order is `compensated` in
//	                          PG but Redis qty is leaked.
//	                          Alert > 0
//	"already_compensated_redis_error"
//	                        — `RevertInventory` failed during the
//	                          IDEMPOTENT re-drive (DB was already
//	                          `compensated`; Redis revert is a
//	                          retry of a previous failure). The
//	                          SETNX guard means most operator
//	                          impact is benign noise; distinct
//	                          label so triage can scope to
//	                          re-drive failures vs fresh leaks
//	"context_error"         — UoW propagated context.Canceled
//	                          or context.DeadlineExceeded. Usually
//	                          shutdown noise; rate > 0 outside
//	                          deploys means timeouts at the saga
//	                          layer
//	"uow_infra_error"       — UoW returned an error not wrapped
//	                          by stepError (tx-begin failure,
//	                          connection-pool exhaustion, etc.)
//	                          AND not a context error. DB / pool
//	                          health concern. Distinct from
//	                          "unknown" so the alert can
//	                          differentiate real infra trouble
//	                          from classifier drift
//	"unknown"               — sentinel for the
//	                          default-fallthrough classifier
//	                          (Slice 2's `did_record` guard
//	                          fires on a return path that
//	                          didn't explicitly record). Reserved
//	                          for true classifier drift — should
//	                          ALWAYS be 0; alert if rate > 0
//	                          signals a code-path-missing-record
//	                          regression
var SagaCompensatorEventsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "saga_compensator_events_processed_total",
		Help: "Total saga compensator outcomes processed via the Kafka-driven order.failed consumer",
	},
	[]string{"outcome"},
)

// SagaCompensationLoopDuration is the end-to-end compensation
// latency histogram, measured from `events_outbox.created_at`
// (threaded onto `kafka.Message.Time` per Slice 0's data-path
// foundation) to the moment the compensator commits
// MarkCompensated. Includes:
//
//   - Outbox relay polling delay (default 500ms tick)
//   - Kafka write + broker round-trip
//   - Saga consumer FetchMessage + processing
//   - Compensator UoW (DB tx) + Redis revert
//
// Observed ONLY on the compensated success path; the
// `already_compensated` and `path_c_skipped` no-op paths are
// recorded in SagaCompensatorEventsTotal (with their respective
// outcome labels) but NOT in this histogram — sub-millisecond
// no-op durations would skew p50/p99 to the floor and hide real
// degradation.
//
// Bucket layout extends to 5 minutes (`300s`) per plan-review
// MEDIUM #7: incident-scale durations need visibility for
// `histogram_quantile` to return meaningful values during
// degraded states. Pre-D12.4 the watchdog's `.005…10s` buckets
// would have left every incident in `+Inf`.
var SagaCompensationLoopDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "saga_compensation_loop_duration_seconds",
		Help:    "End-to-end compensation latency from events_outbox.created_at to MarkCompensated commit. Observed on compensated success path only.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	},
)

// SagaCompensationConsumerLagSeconds is the saga consumer's
// lag-since-write gauge. Set per-FetchMessage to
// `time.Since(msg.Time)` for the most-recent message processed.
// The 30s-idle-reset goroutine in SagaConsumer (Slice 2) zeros
// this gauge if no message has been processed for > 30s, so a
// quiet-system stale-reading doesn't trigger false-positive
// alerts.
//
// IMPORTANT: this is a PERFORMANCE metric, NOT a LIVENESS
// metric. A crashed consumer leaves the gauge stale at the last
// value until Prometheus marks the target down (`up == 0` after
// `scrape_interval × 3`). The `SagaConsumerLagHigh` alert (Slice
// 3) MUST be gated on `up == 1` to prevent false-positive on
// crash. Liveness is observed via a separate `SagaConsumerDown`
// alert on `up == 0 for 2m`.
//
// Multi-replica deployments (future Phase 4): each replica sets
// the gauge independently. PromQL queries should use
// `max by (instance) (saga_compensation_consumer_lag_seconds)`
// to aggregate.
var SagaCompensationConsumerLagSeconds = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "saga_compensation_consumer_lag_seconds",
		Help: "Saga consumer lag-since-write of the most-recently-processed message. PERFORMANCE metric, not liveness — crashed consumer leaves gauge stale; gate alerts on up==1. For multi-replica deployments, aggregate via max by (instance).",
	},
)
