package messaging

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/segmentio/kafka-go"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

const (
	// Stage5IntakeGroupID is the Kafka consumer group ID for Stage 5's
	// async PG persistence worker. One process = one consumer; multiple
	// pods of the same Stage 5 binary share the group and Kafka
	// distributes partitions across them (horizontal scaling for free,
	// which is the whole point of choosing Kafka over Redis Stream for
	// the durability layer — XREADGROUP also supports this but loses
	// messages on Redis crash because Stream is in-memory).
	Stage5IntakeGroupID = "stage5-intake-group"

	// intakeIdleResetThreshold mirrors saga_consumer's idleResetThreshold.
	// After this much idle time the consumer-lag gauge is zeroed —
	// without this, a quiet system would freeze the gauge at the last
	// observed value and false-fire lag alerts long after traffic
	// stopped.
	intakeIdleResetThreshold = 30 * time.Second

	// intakeIdleResetTickInterval is how often the idle-reset goroutine
	// checks `lastMessageAt`.
	intakeIdleResetTickInterval = 5 * time.Second

	// intakeFetchErrorBackoff is the sleep between failed FetchMessage
	// calls when the broker is unreachable. Same shape as saga_consumer's
	// inline 1s sleep — keeps the log volume bounded under sustained
	// broker outage instead of busy-looping on the error path.
	intakeFetchErrorBackoff = time.Second

	// intakeRevertCompensationTimeout bounds the detached-context revert
	// call invoked after a terminal processor error. Mirrors the
	// 3s revertCompensationTimeout in service_kafka_intake.go — same
	// rationale (revert.lua is one EXISTS + INCRBY pipeline on a
	// healthy Redis; > 3s implies Redis is so degraded the drift
	// reconciler is the right fallback).
	intakeRevertCompensationTimeout = 3 * time.Second
)

// IntakeConsumer drains the Stage 5 `booking.intake.v5` Kafka topic
// and turns each message into a PG row via the existing
// worker.MessageProcessor. The processor does the UoW (DecrementTicket
// + Order.Create); the consumer's job is strictly transport adaptation:
//
//  1. FetchMessage (manual-commit mode — at-least-once semantics)
//  2. JSON-decode the Stage 5 IntakeMessage payload
//  3. Adapt to worker.QueuedBookingMessage
//  4. Call processor.Process
//  5. Classify the error; commit on terminal outcomes, leave
//     uncommitted on retriable transients (Kafka redelivers on rebalance)
//
// Error classification rationale:
//
//   - Malformed payload (JSON decode fail / missing required field) →
//     commit + skip. The message will never become valid via retry, so
//     redelivery is wasted work. The Redis inventory is already
//     deducted (Stage 5 pre-publish flow); the drift reconciler picks
//     up the leaked unit on its next sweep.
//   - Domain sentinel (ErrTicketTypeSoldOut, ErrUserAlreadyBought,
//     ErrInvalid*) → commit + skip. Same logic — retry won't change
//     the outcome.
//   - Anything else (DB transient, ctx deadline, unknown error) → do
//     NOT commit. Kafka rebalance will redeliver. This is the "safe
//     default" branch: we'd rather over-deliver than silently drop.
//
// What this consumer deliberately does NOT have (vs SagaConsumer):
//
//   - No durable retry counter. The SagaConsumer's Redis retry counter
//     exists because every compensation has bounded value — past N
//     retries it's certain the message is poison. Intake messages are
//     different: a transient PG outage is the most likely failure, and
//     rebalance-driven redelivery is the correct response (no caller
//     to apologise to — the API returned 202 long ago). Adding a DLQ
//     for intake before we've observed which failure modes actually
//     occur in prod would be premature.
//
// Compensation: on a TERMINAL processor error (sold-out, duplicate, or
// malformed-domain-invariant) the Stage 5 Lua's already-deducted Redis
// qty would otherwise leak until the drift reconciler picks it up. We
// proactively call RevertInventory on the terminal path so the leak
// window is sub-millisecond instead of sweep-interval-long — the drift
// reconciler is the SAFETY NET, not the primary recovery path.
type IntakeConsumer struct {
	reader        *kafka.Reader
	processor     worker.MessageProcessor
	inventory     domain.InventoryRepository
	log           *mlog.Logger
	lastMessageAt atomic.Int64 // UnixNano; 0 = never received a message
}

// NewIntakeConsumer constructs the Stage 5 intake consumer. The
// processor is injected (not a concrete dependency on the worker
// package) so tests can hand in a stub MessageProcessor without
// spinning up PG.
//
// StartOffset: FirstOffset matches SagaConsumer — on a fresh consumer
// group the reader replays from the beginning of the topic rather than
// silently discarding existing messages. In a production deploy the
// group has prior offsets committed, so this only affects the first
// boot.
func NewIntakeConsumer(
	cfg *config.KafkaConfig,
	processor worker.MessageProcessor,
	inventory domain.InventoryRepository,
	logger *mlog.Logger,
) *IntakeConsumer {
	scoped := logger.With(mlog.String("component", "stage5_intake_consumer"))

	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     cfg.Brokers,
		GroupID:     Stage5IntakeGroupID,
		Topic:       Stage5IntakeTopic,
		StartOffset: kafka.FirstOffset,
		MinBytes:    10e3,
		MaxBytes:    10e6,
	})

	return &IntakeConsumer{
		reader:    r,
		processor: processor,
		inventory: inventory,
		log:       scoped,
	}
}

// Start blocks the caller goroutine until ctx is cancelled. Spawns a
// companion idle-reset goroutine bounded by the SAME ctx — callers MUST
// cancel ctx for a clean shutdown to avoid leaking that goroutine. The
// production pattern is to spawn `go consumer.Start(runCtx)` once from
// installServer and cancel runCtx during OnStop.
func (c *IntakeConsumer) Start(ctx context.Context) error {
	c.log.Info(ctx, "stage5 intake consumer starting",
		mlog.String("topic", Stage5IntakeTopic),
		mlog.String("group", Stage5IntakeGroupID),
	)

	// Companion idle-reset goroutine — same shape as SagaConsumer.
	// Without this the lag gauge would stick at the last observed
	// value forever on a quiet system.
	go c.runIdleReset(ctx)

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			c.log.Error(ctx, "stage5 intake fetch failed", tag.Error(err))
			// Bounded sleep before retrying — the alternative is a
			// busy-loop on a sustained broker outage which would flood
			// the structured-log pipeline. Same bound SagaConsumer uses.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(intakeFetchErrorBackoff):
			}
			continue
		}

		c.lastMessageAt.Store(time.Now().UnixNano())

		c.handleMessage(ctx, msg)
	}
}

// handleMessage processes one Kafka message end-to-end: decode, adapt,
// process, classify, commit-or-skip. Extracted from Start so the
// per-message branching is testable without driving the FetchMessage
// loop (no need to mock kafka.Reader).
func (c *IntakeConsumer) handleMessage(ctx context.Context, msg kafka.Message) {
	intake, err := DecodeIntakeMessage(msg.Value)
	if err != nil {
		// Malformed payload — no amount of retry fixes this. Commit
		// the offset so the message is not redelivered, and log at
		// Error so an operator sees it (this is the signal that a
		// producer is publishing bad shapes — drift reconciler will
		// recover the leaked Redis inventory unit).
		c.log.Error(ctx, "stage5 intake message decode failed; committing to skip",
			tag.Error(err),
			tag.Partition(msg.Partition),
			tag.Offset(msg.Offset),
		)
		c.commitOrLog(ctx, msg)
		return
	}

	queued := intakeToQueued(intake, msg)
	if procErr := c.processor.Process(ctx, queued); procErr != nil {
		if isTerminalProcessError(procErr) {
			// Domain sentinel — retry will not change the outcome.
			// The Redis Lua already deducted qty at publish-time;
			// since the PG UoW rolled back (DecrementTicket /
			// Order.Create failed before commit), the Redis qty is
			// leaked. Revert it BEFORE committing the offset so the
			// inventory returns to the visible pool in sub-ms, not
			// sweep-interval-long. Drift reconciler is the safety
			// net for revert failures.
			c.compensateLeakedInventory(ctx, intake, queued, procErr)
			c.commitOrLog(ctx, msg)
			return
		}
		// Transient — leave the offset uncommitted so Kafka rebalance
		// redelivers. This is the safe default for the unclassified
		// branch: better to over-deliver than to silently drop.
		c.log.Error(ctx, "stage5 intake processor returned transient error; not committing for redelivery",
			tag.Error(procErr),
			tag.OrderID(queued.OrderID),
			tag.Partition(msg.Partition),
			tag.Offset(msg.Offset),
		)
		return
	}

	c.commitOrLog(ctx, msg)
}

// compensateLeakedInventory reverts the Redis qty deducted by the
// Stage 5 Lua publish-time hot path after the worker UoW rolled back
// on a terminal error. Detached context (NOT the caller's) so a
// shutdown-triggered ctx cancel doesn't immediately fail the revert
// and permanently leak inventory — mirrors the same pattern in
// service_kafka_intake.go's publish-failure path.
//
// compensationID = "order:<id>" matches revert.lua's SETNX key shape
// used by saga_compensator + stage5Compensator, so the idempotency
// guard short-circuits a duplicate revert if the drift reconciler
// later races to compensate the same order.
//
// On revert failure: log Error + count metric (drift reconciler will
// pick it up; no further escalation here because the offset is about
// to be committed anyway — a Kafka redelivery wouldn't help because
// the processor would just fail with the same terminal error and we'd
// be back here).
func (c *IntakeConsumer) compensateLeakedInventory(
	ctx context.Context,
	intake booking.IntakeMessage,
	queued *worker.QueuedBookingMessage,
	procErr error,
) {
	revertCtx, cancel := context.WithTimeout(context.Background(), intakeRevertCompensationTimeout)
	defer cancel()
	compensationID := "order:" + intake.OrderID.String()
	if revertErr := c.inventory.RevertInventory(revertCtx, intake.TicketTypeID, intake.Quantity, compensationID); revertErr != nil {
		c.log.Error(ctx, "stage5 intake terminal-error revert failed; drift reconciler will retry",
			tag.Error(procErr),
			mlog.NamedError("revert_error", revertErr),
			tag.OrderID(queued.OrderID),
			tag.TicketTypeID(queued.TicketTypeID),
		)
		return
	}
	c.log.Warn(ctx, "stage5 intake terminal error; inventory reverted",
		tag.Error(procErr),
		tag.OrderID(queued.OrderID),
		tag.TicketTypeID(queued.TicketTypeID),
	)
}

// commitOrLog commits the message's offset and logs (not fails) on
// commit error. Commit errors are operational concerns — the message
// has already been processed correctly; failing to commit means a
// future redelivery may run Process again. The worker's UoW is the
// place that must be idempotent against that (duplicate orders are
// blocked by the DB unique constraint on `orders.id`).
func (c *IntakeConsumer) commitOrLog(ctx context.Context, msg kafka.Message) {
	if err := c.reader.CommitMessages(ctx, msg); err != nil {
		c.log.Error(ctx, "stage5 intake commit offset failed",
			tag.Error(err),
			tag.Partition(msg.Partition),
			tag.Offset(msg.Offset),
		)
	}
}

// isTerminalProcessError classifies a worker.MessageProcessor error as
// terminal (no point in redelivery) vs transient (redelivery may
// succeed). Inverts the Stage 3/4 worker's `DefaultRetryPolicy`:
//
//	transient = retry budget consumes it  ==  Stage 5 leaves offset uncommitted
//	malformed = fast-path DLQ              ==  Stage 5 commits and skips
//
// Stage 5 ADDS three sentinels to the "malformed" set that
// DefaultRetryPolicy treats as transient:
//
//   - domain.ErrSoldOut + ErrTicketTypeSoldOut — Redis approved but PG
//     said no (drift). Redelivery cannot reverse the sold-out condition
//     and would just churn the consumer. Stage 3/4 retries because
//     the legacy `events.available_tickets` path could conceivably get
//     unstuck if the saga reverted concurrently; Stage 5's
//     `event_ticket_types.available_tickets` is the SoT and a sold-out
//     row stays sold-out until manual reset.
//   - domain.ErrUserAlreadyBought — DB unique-constraint violation.
//     Retry will hit the same constraint.
//
// All other "malformed input" sentinels are inherited from
// domain.IsMalformedOrderInput so the two classifiers stay in lockstep
// when new invariants get added to the Reservation factory.
func isTerminalProcessError(err error) bool {
	if domain.IsMalformedOrderInput(err) {
		return true
	}
	return errors.Is(err, domain.ErrSoldOut) ||
		errors.Is(err, domain.ErrTicketTypeSoldOut) ||
		errors.Is(err, domain.ErrUserAlreadyBought)
}

// intakeToQueued adapts a Stage 5 IntakeMessage (+ its Kafka envelope)
// to the application-layer worker.QueuedBookingMessage that the
// existing MessageProcessor consumes. Lives here (not in the worker
// package) because the input type lives in application/booking and the
// output type lives in application/worker — the adapter belongs at the
// transport boundary that owns both sides of the conversion.
//
// MessageID format: "p<partition>/o<offset>" matches the SagaConsumer
// log convention (tag.Partition + tag.Offset) so the same operator
// query pattern works across both consumers.
func intakeToQueued(im booking.IntakeMessage, kmsg kafka.Message) *worker.QueuedBookingMessage {
	return &worker.QueuedBookingMessage{
		MessageID:     "p" + strconv.Itoa(kmsg.Partition) + "/o" + strconv.FormatInt(kmsg.Offset, 10),
		OrderID:       im.OrderID,
		UserID:        im.UserID,
		EventID:       im.EventID,
		TicketTypeID:  im.TicketTypeID,
		Quantity:      im.Quantity,
		ReservedUntil: time.Unix(im.ReservedUntil, 0).UTC(),
		AmountCents:   im.AmountCents,
		Currency:      im.Currency,
	}
}

// runIdleReset zeros the consumer-lag gauge after intakeIdleResetThreshold
// of no-messages. Same shape as SagaConsumer.runIdleReset — the
// blocking FetchMessage in Start has no idle hook, so this companion
// is the only way to drive the gauge to zero on a quiet system.
//
// Currently a no-op for the gauge because IntakeConsumer doesn't yet
// emit a lag metric (Session 2 scope keeps the consumer minimal; the
// gauge wires in alongside the broader Stage 5 metrics in Session 3's
// benchmark surface). The lifecycle plumbing is here now so adding the
// metric later is a one-line change inside the if-branch.
func (c *IntakeConsumer) runIdleReset(ctx context.Context) {
	ticker := time.NewTicker(intakeIdleResetTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Hook reserved for the lag-gauge zero call when metrics
			// land in Session 3.
			_ = shouldResetLagIntake(c.lastMessageAt.Load(), time.Now(), intakeIdleResetThreshold)
		}
	}
}

// shouldResetLagIntake is the pure decision function for the idle
// reset. Lifted to a free function so a future test can drive synthetic
// time without dependency-injecting a clock interface (same pattern as
// shouldResetLag in saga_consumer.go).
//
// `lastUnixNano == 0` is the sentinel "consumer hasn't received any
// message yet since process start" — treat as idle.
func shouldResetLagIntake(lastUnixNano int64, now time.Time, threshold time.Duration) bool {
	if lastUnixNano == 0 {
		return true
	}
	return now.Sub(time.Unix(0, lastUnixNano)) > threshold
}

// Close shuts down the kafka.Reader. Mirrors IntakePublisher.Close's
// 10s bounded-close guard so OnStop can never hang on a slow broker.
func (c *IntakeConsumer) Close() error {
	done := make(chan error, 1)
	go func() { done <- c.reader.Close() }()

	select {
	case err := <-done:
		return err
	case <-time.After(kafkaCloseTimeout):
		return fmt.Errorf("IntakeConsumer.Close: timed out after %s", kafkaCloseTimeout)
	}
}
