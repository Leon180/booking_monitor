package cache

import (
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Stream / consumer-group / DLQ keys + per-message field names are
// WIRE CONTRACTS between the API-side Lua producer (`deduct.lua`) and
// the worker consumer. A mismatch means silent split brain — one side
// writes to `orders:stream`, the other reads from `bookings:stream`,
// no error, no messages flow. Kept as const so they can't be
// independently overridden per process via env vars.
//
// Tunables (batch size, block timeout, retry budget, etc.) live in
// `config.WorkerConfig` instead — those ARE per-environment knobs.
const (
	streamKey = "orders:stream"
	groupName = "orders:group"
	dlqKey    = "orders:dlq"

	// XADD payload field names — must match the keys `deduct.lua`
	// writes via XADD. parseMessage / parsePending / moveToDLQ all use
	// these constants instead of inline literals.
	//   - fieldReservedUntil added in D3 (Pattern A reservation TTL).
	//   - fieldTicketTypeID + fieldAmountCents + fieldCurrency added in
	//     D4.1 (KKTIX 票種 + price snapshot at book time).
	// Producer (deduct.lua) and consumer (parseMessage) MUST stay
	// aligned — adding a field on one side without the other shifts
	// every D4.1 booking to DLQ via NewReservation invariant rejection.
	fieldOrderID       = "order_id"
	fieldUserID        = "user_id"
	fieldEventID       = "event_id"
	fieldQuantity      = "quantity"
	fieldReservedUntil = "reserved_until"
	fieldTicketTypeID  = "ticket_type_id"
	fieldAmountCents   = "amount_cents"
	fieldCurrency      = "currency"

	// DLQ payload extra fields — wire contract with whatever
	// downstream consumer reads `orders:dlq` for forensics. Same
	// const-vs-config rule as above: a downstream that expects
	// `original_id` cannot survive a producer that writes `orig_id`.
	fieldDLQOriginalID = "original_id"
	fieldDLQError      = "error"
	fieldDLQFailedAt   = "failed_at"

	// DLQ retention is now per-environment via cfg.Redis.DLQRetention
	// (env var REDIS_DLQ_RETENTION, default 720h = 30d). The MINID
	// time-based eviction policy (vs MAXLEN count-based) is preserved
	// — see config.go::RedisConfig.DLQRetention doc + PROJECT_SPEC
	// §6.8 for why MAXLEN would be wrong on a work-queue stream.

	// DLQ route reasons — Prometheus label values for
	// `redis_dlq_routed_total{reason=...}`. Kept in sync with the
	// pre-warm list in `internal/infrastructure/observability/metrics.go`
	// init(). Promote to const so a typo (e.g. "malformed_classifed")
	// can't drift a single emit-site away from the pre-warmed series.
	dlqReasonMalformedParse      = "malformed_parse"
	dlqReasonMalformedClassified = "malformed_classified"
	dlqReasonExhaustedRetries    = "exhausted_retries"
)

type redisOrderQueue struct {
	client                   *redis.Client
	inventoryRepo            domain.InventoryRepository
	logger                   *mlog.Logger
	metrics                  worker.QueueMetrics
	retryPolicy              worker.RetryPolicy
	consumerName             string
	maxConsecutiveReadErrors int
	streamReadCount          int
	streamBlockTimeout       time.Duration
	maxRetries               int
	retryBaseDelay           time.Duration
	failureTimeout           time.Duration
	pendingBlockTimeout      time.Duration
	readErrorBackoff         time.Duration
	dlqRetention             time.Duration
}

// NewRedisOrderQueue wires the order-stream consumer.
//
// retryPolicy decides per-error whether the retry budget should burn
// or short-circuit straight to compensation. nil → "always retry"
// (preserves the pre-policy behaviour for callers / tests that don't
// care about the malformed-input fast path). Production wires
// `worker.DefaultRetryPolicy()` via fx.
func NewRedisOrderQueue(
	client *redis.Client,
	inventoryRepo domain.InventoryRepository,
	logger *mlog.Logger,
	cfg *config.Config,
	metrics worker.QueueMetrics,
	retryPolicy worker.RetryPolicy,
) worker.OrderQueue {
	if retryPolicy == nil {
		retryPolicy = func(error) bool { return true }
	}
	return &redisOrderQueue{
		client:        client,
		inventoryRepo: inventoryRepo,
		logger: logger.With(
			mlog.String("component", "redis_order_queue"),
			mlog.String("worker_id", cfg.App.WorkerID),
		),
		metrics:                  metrics,
		retryPolicy:              retryPolicy,
		consumerName:             cfg.App.WorkerID,
		maxConsecutiveReadErrors: cfg.Redis.MaxConsecutiveReadErrors,
		streamReadCount:          cfg.Worker.StreamReadCount,
		streamBlockTimeout:       cfg.Worker.StreamBlockTimeout,
		maxRetries:               cfg.Worker.MaxRetries,
		retryBaseDelay:           cfg.Worker.RetryBaseDelay,
		failureTimeout:           cfg.Worker.FailureTimeout,
		pendingBlockTimeout:      cfg.Worker.PendingBlockTimeout,
		readErrorBackoff:         cfg.Worker.ReadErrorBackoff,
		dlqRetention:             cfg.Redis.DLQRetention,
	}
}

func (q *redisOrderQueue) EnsureGroup(ctx context.Context) error {
	// XGROUP CREATE orders:stream orders:group $ MKSTREAM
	err := q.client.XGroupCreateMkStream(ctx, streamKey, groupName, "$").Err()
	if err != nil {
		// strings.Contains is more robust than exact string equality across
		// Redis versions and proxies (closes action-list item L2).
		if strings.Contains(err.Error(), "BUSYGROUP") {
			return nil
		}
		return fmt.Errorf("XGroupCreateMkStream: %w", err)
	}
	return nil
}

func (q *redisOrderQueue) Subscribe(ctx context.Context, handler func(ctx context.Context, msg *worker.QueuedBookingMessage) error) error {
	// Consumer Name (unique per pod, using hostname or uuid would be better, but for single node "App" is fine)
	consumerName := q.consumerName

	// 1. Recover Pending Messages (PEL)
	// These are messages this consumer claimed but crashed before ACKing.
	if err := q.processPending(ctx, consumerName, handler); err != nil {
		q.logger.Error(ctx, "Failed to process pending messages during startup", tag.Error(err))
		// We log but continue, ensuring we at least process new messages.
	}

	// consecutiveErrors tracks persistent XReadGroup failures so a
	// durably-broken Redis exits the loop instead of spinning forever
	// — previously the subscribe loop would log "XReadGroup Error"
	// once per second indefinitely while the process looked alive to
	// k8s (no restart trigger).
	consecutiveErrors := 0

	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Block for cfg.Worker.StreamBlockTimeout waiting for messages.
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, ">"},
			Count:    int64(q.streamReadCount),
			Block:    q.streamBlockTimeout,
		}).Result()

		if err != nil {
			// redis.Nil means "no new messages within Block window" — a
			// SUCCESS case, not a failure.
			if errors.Is(err, redis.Nil) {
				consecutiveErrors = 0
				continue
			}

			consecutiveErrors++
			if consecutiveErrors >= q.maxConsecutiveReadErrors {
				return fmt.Errorf("XReadGroup: %d consecutive errors, last: %w", consecutiveErrors, err)
			}

			// Self-healing: If group is missing (e.g. after FLUSHALL), recreate it.
			//
			// On a successful EnsureGroup we reset `consecutiveErrors` so a
			// recurring-NOGROUP storm (e.g., FLUSHALL followed by slow
			// recreation, or a deliberate group reset by ops) doesn't burn
			// the read-error budget for a class of errors we ARE recovering
			// from. Without the reset, sustained NOGROUP would tip the
			// worker over the bailout threshold even though the self-heal
			// is succeeding every time — observable as premature pod
			// restarts during operational group resets.
			if strings.Contains(err.Error(), "NOGROUP") {
				// Loud signal: NOGROUP in healthy production should be
				// IMPOSSIBLE — the consumer group is created at startup
				// and Redis Streams never auto-deletes it. If we see
				// NOGROUP, something destroyed it (FLUSHALL, manual
				// XGROUP DESTROY, Redis crash without AOF). The counter
				// + alert rule (`ConsumerGroupRecreated` in alerts.yml)
				// ensure the operator hears about it; the structured
				// log provides the timestamp + worker_id for triage.
				//
				// IMPORTANT: the recreation uses `$` (current end of
				// stream), which means messages enqueued BETWEEN the
				// destruction and this recovery moment are silently
				// skipped. There is no `0` alternative that's correct
				// — `0` would replay all historical messages as
				// duplicates. The right answer is "don't let NOGROUP
				// happen", which the alert enforces.
				q.metrics.RecordConsumerGroupRecreated()
				q.logger.Warn(ctx, "XReadGroup Error: NOGROUP. Attempting to recreate group — messages enqueued before recovery may have been silently skipped",
					mlog.Int("consecutive_errors", consecutiveErrors))
				if ensureErr := q.EnsureGroup(ctx); ensureErr != nil {
					q.logger.Error(ctx, "Failed to recreate group", tag.Error(ensureErr))
				} else {
					// Self-heal worked — counter must reset so the next
					// XReadGroup attempt starts from a clean slate.
					consecutiveErrors = 0
				}
				if !q.sleepCtx(ctx, q.readErrorBackoff) {
					return ctx.Err()
				}
				continue
			}

			// Log error and sleep briefly
			q.logger.Error(ctx, "XReadGroup Error",
				tag.Error(err), mlog.Int("consecutive_errors", consecutiveErrors))
			if !q.sleepCtx(ctx, q.readErrorBackoff) {
				return ctx.Err()
			}
			continue
		}

		// Reset on any successful read (including empty stream batches).
		consecutiveErrors = 0

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				orderMsg, err := parseMessage(msg)
				if err != nil {
					q.logger.Error(ctx, "Malformed message — routing to DLQ",
						tag.Error(err), tag.MsgID(msg.ID))
					// If DLQ write fails, leave message in PEL; it'll
					// be retried on the next cycle. Better to grow PEL
					// under DLQ outage than silently lose the trace.
					if dlqErr := q.moveToDLQ(ctx, msg, err); dlqErr != nil {
						continue
					}
					q.metrics.RecordDLQRoute(dlqReasonMalformedParse)
					q.ackOrLog(ctx, msg.ID)
					continue
				}

				// Process with Retry. `malformed` is true when the
				// retry policy short-circuited the budget (deterministic
				// failure → DLQ fast-path); false on either budget
				// exhaustion or ctx-cancel mid-backoff.
				err, malformed := q.processWithRetry(ctx, handler, orderMsg)
				if err != nil {
					// handleFailure returns false when compensation failed
					// — leave in PEL for retry.
					if q.handleFailure(ctx, orderMsg, msg, err) {
						reason := dlqReasonExhaustedRetries
						if malformed {
							reason = dlqReasonMalformedClassified
						}
						q.metrics.RecordDLQRoute(reason)
						q.ackOrLog(ctx, msg.ID)
					}
				} else {
					q.ackOrLog(ctx, msg.ID)
				}
			}
		}
	}
}

// sleepCtx sleeps for d, returning false if ctx was cancelled mid-wait
// (callers use that to bail out promptly rather than burn the rest of
// the sleep). Replaces bare `time.Sleep(...)` calls in error-recovery
// paths so shutdown signals propagate without delay.
func (q *redisOrderQueue) sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ackOrLog ACKs a message and logs any ACK failure. A failed ACK leaves the
// message in the Pending Entries List (PEL) and will be re-processed on
// the next processPending cycle — but we MUST log so silent redelivery
// is operator-visible.
func (q *redisOrderQueue) ackOrLog(ctx context.Context, msgID string) {
	if err := q.client.XAck(ctx, streamKey, groupName, msgID).Err(); err != nil {
		q.metrics.RecordXAckFailure()
		q.logger.Error(ctx, "XAck failed — message will be re-delivered via PEL",
			tag.Error(err), tag.MsgID(msgID))
	}
}

// processWithRetry returns (err, malformed). When err is non-nil,
// `malformed` distinguishes the two reasons we'd surface the message
// to handleFailure → DLQ:
//   - malformed=true:  retry policy declared the error deterministic
//                      (fast-path bypass). DLQ reason is "malformed_classified".
//   - malformed=false: budget exhausted on a transient error, OR ctx
//                      cancelled mid-backoff. DLQ reason is "exhausted_retries"
//                      (or no DLQ if ctx cancelled — see Subscribe).
//
// The caller uses the bool to label the `redis_dlq_routed_total` metric.
// Returning the bool from here (rather than re-evaluating
// `q.retryPolicy(err)` at the call site) keeps the policy a one-shot
// observation per message — desirable because the policy implementation
// is not strictly required to be idempotent.
func (q *redisOrderQueue) processWithRetry(ctx context.Context, handler func(ctx context.Context, msg *worker.QueuedBookingMessage) error, msg *worker.QueuedBookingMessage) (error, bool) {
	var lastErr error

	for i := 0; i < q.maxRetries; i++ {
		err := handler(ctx, msg)
		if err == nil {
			return nil, false
		}
		lastErr = err
		// Application-supplied retry policy decides whether this error
		// is worth burning a budget slot. Deterministic-failure errors
		// (e.g. NewOrder invariant violations under the default policy)
		// short-circuit straight to DLQ via handleFailure without
		// waiting out the backoff. Transient errors fall through to
		// the backoff and the next attempt.
		if !q.retryPolicy(err) {
			return err, true
		}
		// Backoff (linear: attempt N waits N * RetryBaseDelay) while
		// honoring ctx cancellation so shutdown is prompt.
		if !q.sleepCtx(ctx, time.Duration(i+1)*q.retryBaseDelay) {
			return ctx.Err(), false
		}
	}
	return lastErr, false
}

// handleFailure runs the compensation path for an exhausted-retry message:
// revert Redis inventory (the Lua deduct fired during API ingress) then
// record the failure in the DLQ. Returns true when BOTH compensation
// steps succeed and the caller may ACK; returns false to leave the
// message in the PEL so the next consumer cycle retries compensation.
//
// Why retry-via-PEL instead of ACK-and-alert: RevertInventory is
// idempotent (revert.lua + msgID SETNX), so PEL reclaim is safe. If we
// ACKed on revert failure, Redis inventory would stay permanently
// under-counted — the user visible symptom is tickets appearing sold
// when they aren't. Operator alerting on PEL length is strictly
// cheaper than chasing inventory drift after the fact.
func (q *redisOrderQueue) handleFailure(ctx context.Context, orderMsg *worker.QueuedBookingMessage, rawMsg redis.XMessage, err error) bool {
	// Background context with timeout: compensation MUST run even if the
	// parent ctx was cancelled mid-processing. Budget configurable via
	// `cfg.Worker.FailureTimeout`.
	bgCtx, cancel := context.WithTimeout(context.Background(), q.failureTimeout)
	defer cancel()

	if revertErr := q.inventoryRepo.RevertInventory(bgCtx, orderMsg.EventID, orderMsg.Quantity, rawMsg.ID); revertErr != nil {
		q.metrics.RecordRevertFailure()
		q.logger.Error(ctx, "RevertInventory failed — leaving message in PEL for retry",
			tag.Error(revertErr),
			tag.MsgID(rawMsg.ID),
			tag.EventID(orderMsg.EventID),
			tag.Quantity(orderMsg.Quantity),
		)
		return false
	}

	if dlqErr := q.moveToDLQ(bgCtx, rawMsg, err); dlqErr != nil {
		// Inventory was reverted (idempotent), only DLQ write failed.
		// PEL retry will re-run the whole path — RevertInventory will
		// noop on the second pass, DLQ will be retried.
		q.logger.Error(ctx, "moveToDLQ failed after successful revert — leaving message in PEL for retry",
			tag.Error(dlqErr),
			tag.MsgID(rawMsg.ID),
		)
		return false
	}

	return true
}

// moveToDLQ writes the original message plus failure metadata to the
// DLQ stream. Returns the XAdd error so callers can decide whether to
// ACK (and lose the trace) or leave the message in PEL for retry.
// Historical behaviour of "log and fall through" meant a DLQ outage
// permanently lost failure traces — now the caller is in the loop.
func (q *redisOrderQueue) moveToDLQ(ctx context.Context, msg redis.XMessage, err error) error {
	values := map[string]interface{}{
		fieldDLQOriginalID: msg.ID,
		fieldDLQError:      err.Error(),
		fieldDLQFailedAt:   time.Now().Format(time.RFC3339),
	}
	// Copy original values
	for k, v := range msg.Values {
		values[k] = v
	}

	// MinID = "<NOW - dlqRetention>-0" tells Redis to evict any DLQ
	// entry older than that timestamp on this XADD. Approx=true
	// asks Redis to use the cheaper macro-node-boundary trim instead
	// of exact MINID; trade-off is the trim may leave a few extra
	// entries past the boundary, which is fine for DLQ retention.
	//
	// Stream IDs are `<ms-since-epoch>-<seq>`; UnixMilli matches.
	//
	// Clamp to 0 if the host clock is so badly drifted that
	// (now - 30d) goes negative (pre-1970). A negative MinID is
	// undocumented Redis behavior and may be parsed as 0 (= trim
	// everything) — clamping to 0 means "trim everything older than
	// epoch" which is the same intent without the ambiguity. Real
	// hosts won't hit this, but the cost of the check is one
	// comparison; the cost of guessing wrong is total DLQ wipe.
	cutoffMs := time.Now().Add(-q.dlqRetention).UnixMilli()
	if cutoffMs < 0 {
		cutoffMs = 0
	}
	cutoffID := fmt.Sprintf("%d-0", cutoffMs)

	if addErr := q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqKey,
		Values: values,
		MinID:  cutoffID,
		Approx: true,
	}).Err(); addErr != nil {
		q.metrics.RecordXAddFailure("dlq")
		q.logger.Error(ctx, "XAdd to DLQ failed",
			tag.Error(addErr),
			mlog.String("original_id", msg.ID),
			mlog.String("dlq", dlqKey),
		)
		return fmt.Errorf("moveToDLQ XAdd: %w", addErr)
	}
	return nil
}

func parseMessage(msg redis.XMessage) (*worker.QueuedBookingMessage, error) {
	// Values is map[string]interface{}. The Lua producer writes
	// order_id / user_id / event_id / quantity as strings; we reject
	// anything malformed so silent zero-UUID / ID=0 records never
	// reach the worker.

	orderIDStr, ok := msg.Values[fieldOrderID].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldOrderID)
	}

	userIDStr, ok := msg.Values[fieldUserID].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldUserID)
	}

	eventIDStr, ok := msg.Values[fieldEventID].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldEventID)
	}

	qtyStr, ok := msg.Values[fieldQuantity].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldQuantity)
	}

	reservedUntilStr, ok := msg.Values[fieldReservedUntil].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldReservedUntil)
	}

	// D4.1 fields — present on every message produced by the
	// post-D4.1 deduct.lua. Their absence indicates either (a) the
	// stream still holds in-flight pre-D4.1 messages from a rolling
	// upgrade, or (b) a producer-side wire-format regression. Both
	// cases are deterministic-failure → DLQ.
	//
	// Routing pedantry: parseMessage errors do NOT pass through the
	// `domain.IsMalformedOrderInput` classifier (that's only for
	// errors raised inside `orderMessageProcessor.Process` via
	// `domain.NewReservation` invariants). parseMessage is the layer
	// BEFORE the processor — when it fails, the queue's Subscribe
	// loop catches the error and routes directly to
	// `moveToDLQ(dlqReasonMalformedParse)` on the first attempt. No
	// retry budget is burned. The two label families are
	// operationally distinct:
	//   - dlqReasonMalformedParse:      wire-format failure (queue boundary)
	//   - dlqReasonMalformedClassified: domain invariant rejection (handler)
	ticketTypeIDStr, ok := msg.Values[fieldTicketTypeID].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldTicketTypeID)
	}
	amountCentsStr, ok := msg.Values[fieldAmountCents].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldAmountCents)
	}
	currency, ok := msg.Values[fieldCurrency].(string)
	if !ok {
		return nil, fmt.Errorf("missing %s", fieldCurrency)
	}

	// OrderID is a UUID v7 string minted in the API handler since
	// PR #47 (BookingService.BookTicket). Reject zero UUID / malformed
	// at the queue boundary so the worker can rely on a valid id.
	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldOrderID, orderIDStr, err)
	}
	if orderID == uuid.Nil {
		return nil, fmt.Errorf("invalid %s: zero UUID", fieldOrderID)
	}

	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldUserID, userIDStr, err)
	}
	// EventID is a UUID v7 string in the stream message (since PR 34).
	// uuid.Parse handles canonical 36-char form; producer-side
	// (deduct.lua via Redis) emits exactly that.
	eventID, err := uuid.Parse(eventIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldEventID, eventIDStr, err)
	}
	qty, err := strconv.Atoi(qtyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldQuantity, qtyStr, err)
	}
	// reserved_until is unix seconds (UTC). Reject "0" / negative — Lua
	// emits the value verbatim as time.Time{}.UTC().Unix() returns
	// -62135596800 for the zero time and BookingService rejects that
	// upstream, so seeing it here means a producer regression.
	reservedUnix, err := strconv.ParseInt(reservedUntilStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldReservedUntil, reservedUntilStr, err)
	}
	if reservedUnix <= 0 {
		return nil, fmt.Errorf("invalid %s: %d (must be a positive unix timestamp)", fieldReservedUntil, reservedUnix)
	}

	// D4.1 — ticket_type_id is a UUID v7 (caller-generated by the
	// admin / event service). Reject zero UUID at the boundary so the
	// DLQ entry is labelled `malformed_parse` (wire-format bug class)
	// rather than `malformed_classified` (domain invariant rejection
	// class) — operationally distinct: a `malformed_parse` spike on a
	// rolling upgrade is the expected transitional drain of pre-D4.1
	// PEL entries; a `malformed_classified` spike is a producer
	// regression worth paging on.
	ticketTypeID, err := uuid.Parse(ticketTypeIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldTicketTypeID, ticketTypeIDStr, err)
	}
	if ticketTypeID == uuid.Nil {
		return nil, fmt.Errorf("invalid %s: zero UUID", fieldTicketTypeID)
	}
	// amount_cents is an int64 string emitted by the Lua producer (Lua
	// numbers serialise via tostring; for an integer-valued IEEE-754
	// double that's the canonical decimal form). Reject anything
	// non-positive — domain factory enforces the same invariant, but
	// catching at the boundary spares the tx open + roll-back.
	amountCents, err := strconv.ParseInt(amountCentsStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", fieldAmountCents, amountCentsStr, err)
	}
	if amountCents <= 0 {
		return nil, fmt.Errorf("invalid %s: %d (must be positive)", fieldAmountCents, amountCents)
	}
	// currency is already lowercase 3-letter at this point (the domain
	// factory normalised it before the Lua call). We don't re-validate
	// the shape here — domain.NewReservation will reject anything
	// malformed. Empty-string is a wire-format regression though, so
	// catch it.
	if currency == "" {
		return nil, fmt.Errorf("invalid %s: empty", fieldCurrency)
	}

	return &worker.QueuedBookingMessage{
		MessageID:     msg.ID, // redis.XMessage.ID — opaque transport handle for ACK
		OrderID:       orderID,
		UserID:        userID,
		EventID:       eventID,
		TicketTypeID:  ticketTypeID,
		Quantity:      qty,
		ReservedUntil: time.Unix(reservedUnix, 0).UTC(),
		AmountCents:   amountCents,
		Currency:      currency,
	}, nil
}

// processPending fetches and processes messages from the Pending Entries List (PEL).
func (q *redisOrderQueue) processPending(ctx context.Context, consumerName string, handler func(ctx context.Context, msg *worker.QueuedBookingMessage) error) error {
	q.logger.Info(ctx, "Checking for pending messages (PEL)...")

	for {
		// XREADGROUP with ID "0" fetches pending messages for this consumer.
		// Block budget honours shutdown signals even when the PEL is empty
		// (addresses action-list M9).
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, "0"}, // "0" = Pending messages
			Count:    int64(q.streamReadCount),
			Block:    q.pendingBlockTimeout,
		}).Result()

		if err != nil {
			if errors.Is(err, redis.Nil) {
				return nil
			}
			// Symmetric NOGROUP handling with the main Subscribe loop:
			// if the consumer group disappeared between worker boot
			// (EnsureGroup at startup) and processPending (called as
			// the first action of Subscribe), the PEL recovery path
			// hits NOGROUP first. Without this branch, the metric
			// would only fire on the SECOND pass (when Subscribe's
			// main loop hits the same error). Increment here so the
			// `ConsumerGroupRecreated` alert covers BOTH entry points.
			//
			// We don't recreate the group here — return the error so
			// Subscribe logs "Failed to process pending messages" and
			// continues to the main loop, which will recreate. The
			// metric is the load-bearing signal; the recreation order
			// doesn't matter operationally.
			if strings.Contains(err.Error(), "NOGROUP") {
				q.metrics.RecordConsumerGroupRecreated()
			}
			return fmt.Errorf("processPending XReadGroup: %w", err)
		}

		if len(streams) == 0 || len(streams[0].Messages) == 0 {
			q.logger.Info(ctx, "No more pending messages to recover.")
			return nil
		}

		stream := streams[0]
		q.logger.Info(ctx, "Recovering pending messages", mlog.Int("count", len(stream.Messages)))

		for _, msg := range stream.Messages {
			orderMsg, err := parseMessage(msg)
			if err != nil {
				q.logger.Error(ctx, "Malformed pending message", tag.MsgID(msg.ID), tag.Error(err))
				// DLQ unreachable → leave in PEL; next recovery cycle retries.
				if dlqErr := q.moveToDLQ(ctx, msg, err); dlqErr != nil {
					continue
				}
				q.metrics.RecordDLQRoute(dlqReasonMalformedParse)
				q.ackOrLog(ctx, msg.ID)
				continue
			}

			// Process with Retry — see Subscribe for the malformed-bool semantics.
			err, malformed := q.processWithRetry(ctx, handler, orderMsg)
			if err != nil {
				// handleFailure returns false when compensation failed;
				// leaving in PEL means next cycle retries revert + DLQ.
				if q.handleFailure(ctx, orderMsg, msg, err) {
					reason := dlqReasonExhaustedRetries
					if malformed {
						reason = dlqReasonMalformedClassified
					}
					q.metrics.RecordDLQRoute(reason)
					q.ackOrLog(ctx, msg.ID)
				}
			} else {
				q.ackOrLog(ctx, msg.ID)
				q.logger.Info(ctx, "Recovered and processed pending message", tag.MsgID(msg.ID))
			}
		}
	}
}
