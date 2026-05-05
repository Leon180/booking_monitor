package observability

// metrics_init.go is the single coordinator for label pre-warming
// across the (now per-concern) metrics_*.go files.
//
// Why a single init() per package, not one per file:
//
//   The Go spec runs multiple init() within a package in
//   "presentation order" — implementation-defined (alphabetical via
//   `gc` today, but the spec doesn't guarantee it). Splitting the
//   init() across files makes a future "this metric assumes that
//   one is registered first" assumption silently breakable depending
//   on file-name sort. Label pre-warming is idempotent so this isn't
//   urgent today, but the centralized init() preserves the cosmetic
//   organization of the per-concern files (CP3b) without introducing
//   implementation-defined ordering. The CP3b roadmap entry in
//   docs/post_phase2_roadmap.md captures this rationale.
//
// All vars referenced below live in their per-concern split files:
//   - BookingsTotal, PageViewsTotal           → metrics_booking.go
//   - WorkerOrdersTotal, DLQMessagesTotal,
//     KafkaConsumerRetryTotal                 → metrics_worker.go
//   - CacheHitsTotal, CacheMissesTotal,
//     IdempotencyReplaysTotal                 → metrics_idempotency.go
//   - RedisXAddFailuresTotal,
//     RedisDLQRoutedTotal                     → metrics_redis_streams.go
//   - ReconResolvedTotal                      → metrics_recon.go
//   - SagaWatchdogResolvedTotal               → metrics_saga.go
//
// Cross-file references work because all files share `package observability`.

import "booking_monitor/internal/domain"

// init pre-initializes all label combinations so they appear in
// /metrics from startup.
func init() {
	for _, status := range []string{"success", "sold_out", "duplicate", "error"} {
		BookingsTotal.WithLabelValues(status)
	}
	for _, status := range []string{"success", "sold_out", "duplicate", "db_error", "malformed_message"} {
		WorkerOrdersTotal.WithLabelValues(status)
	}
	// DLQ topic labels stay inline strings — they're Kafka-side topic
	// names (with the .dlq suffix) and have no domain-side constant.
	// If those move to domain consts in a future PR, update here too.
	for _, topic := range []string{"order.created.dlq", "order.failed.dlq"} {
		for _, reason := range []string{"invalid_payload", "invalid_event", "max_retries"} {
			DLQMessagesTotal.WithLabelValues(topic, reason)
		}
	}
	// Use the domain constants for the canonical wire event types so
	// a typo here can't drift from the producer side. The consumer
	// retry counter watches the same topic strings the producer
	// publishes via the outbox.
	for _, topic := range []string{domain.EventTypeOrderCreated, domain.EventTypeOrderFailed} {
		KafkaConsumerRetryTotal.WithLabelValues(topic, "transient_processing_error")
	}
	// Pre-warm the DLQ stream label so it appears in /metrics at startup.
	// Today "dlq" is the only value written; future main-stream writers
	// will add their own label values here.
	RedisXAddFailuresTotal.WithLabelValues("dlq")
	// Pre-warm the DLQ-route reason labels so all five series exist in
	// /metrics from boot, even on a worker that hasn't yet seen any
	// failures. Keep these strings in sync with the const block in
	// internal/infrastructure/cache/redis_queue.go (DLQReason*).
	//
	// `malformed_parse` is retained as a pre-warm label even though the
	// post-fix code emits `malformed_reverted_legacy` /
	// `malformed_unrecoverable` instead — the alert rules reference the
	// older label and a renamed series would orphan them. New alerts
	// SHOULD branch on the more specific labels.
	for _, reason := range []string{
		"malformed_parse",
		"malformed_classified",
		"exhausted_retries",
		"malformed_reverted_legacy",
		"malformed_unrecoverable",
	} {
		RedisDLQRoutedTotal.WithLabelValues(reason)
	}
	// Pre-warm the cache labels so the series exist in /metrics before
	// the first lookup. Add new values here when new caches are
	// introduced. Keep the literal strings in sync with the
	// `cacheLabel*` consts in `internal/infrastructure/cache/*.go`.
	for _, cache := range []string{"idempotency", "ticket_type"} {
		CacheHitsTotal.WithLabelValues(cache)
		CacheMissesTotal.WithLabelValues(cache)
	}
	// Pre-warm the cache error labels too. Today only ticket_type
	// emits to this counter; idempotency uses the older dedicated
	// `idempotency_cache_get_errors_total` series. New caches should
	// use the labelled `cache_errors_total{cache,op}` and add their
	// pre-warm tuples here.
	for _, op := range []string{"get", "set", "marshal"} {
		CacheErrorsTotal.WithLabelValues("ticket_type", op)
	}
	// Pre-warm idempotency replay outcomes (N4) so all three series
	// exist in /metrics from boot. "legacy_match" should taper to ~0
	// within the 24h cache TTL after deploy — sustained non-zero
	// signals stuck migration; explicit pre-warm gives operators that
	// signal even before the first cache hit lands.
	for _, outcome := range []string{"match", "mismatch", "legacy_match"} {
		IdempotencyReplaysTotal.WithLabelValues(outcome)
	}

	// Pre-warm reconciler counter labels so the series exist in
	// /metrics from the first /metrics scrape, even before the
	// reconciler has resolved a single order. Lets dashboards show
	// "0 errors so far" instead of "no data" — a real distinction.
	for _, outcome := range []string{"charged", "declined", "not_found", "unknown", "max_age_exceeded", "transition_lost"} {
		ReconResolvedTotal.WithLabelValues(outcome)
	}
	// Pre-warm saga-watchdog outcomes (A5) for the same reason.
	// Sustained `compensator_error > 0` is the operator's signal
	// that the watchdog is hitting DB/Redis trouble re-driving
	// stuck-Failed orders; pre-warming makes "0 so far" visible.
	for _, outcome := range []string{"compensated", "already_compensated", "max_age_exceeded", "getbyid_error", "marshal_error", "compensator_error"} {
		SagaWatchdogResolvedTotal.WithLabelValues(outcome)
	}
	// Pre-warm sweeper-panic labels (PR-D fixup). The SweepGoroutinePanic
	// alert fires immediately on any non-zero increase, so the series
	// MUST exist in /metrics before the first panic — otherwise
	// `increase()` over a never-yet-emitted series returns no data
	// (not 0) and the alert can't evaluate. Bare WithLabelValues
	// matches the existing pre-warm pattern in this file (line 81+):
	// the act of looking up the labelled child registers the series.
	for _, sweeper := range []string{"recon", "inventory_drift", "saga_watchdog", "once_recon", "once_drift", "once_saga_watchdog"} {
		sweepGoroutinePanicsTotal.WithLabelValues(sweeper)
	}
	// Pre-warm inventory-drift direction labels (PR-D). Same rationale
	// as ReconResolvedTotal: dashboards should show "0 so far" not
	// "no data" before the first drift event.
	for _, direction := range []string{"cache_missing", "cache_high", "cache_low_excess"} {
		InventoryDriftDetectedTotal.WithLabelValues(direction)
	}
}
