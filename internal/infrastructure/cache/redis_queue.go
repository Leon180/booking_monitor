package cache

import (
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

	"github.com/redis/go-redis/v9"
)

const (
	streamKey = "orders:stream"
	groupName = "orders:group"
	dlqKey    = "orders:dlq"

	// maxConsecutiveReadErrors bounds how long Subscribe tolerates a
	// broken Redis before giving up and letting the caller restart us.
	// At the current 2s block + 1s sleep cadence, 30 errors ≈ 90s of
	// persistent failure — long enough to ride out a brief blip, short
	// enough that k8s can restart the pod before the booking backlog
	// becomes unrecoverable.
	maxConsecutiveReadErrors = 30
)

type redisOrderQueue struct {
	client        *redis.Client
	inventoryRepo domain.InventoryRepository
	logger        *mlog.Logger
	consumerName  string
}

func NewRedisOrderQueue(client *redis.Client, inventoryRepo domain.InventoryRepository, logger *mlog.Logger, cfg *config.Config) domain.OrderQueue {
	return &redisOrderQueue{
		client:        client,
		inventoryRepo: inventoryRepo,
		logger: logger.With(
			mlog.String("component", "redis_order_queue"),
			mlog.String("worker_id", cfg.App.WorkerID),
		),
		consumerName: cfg.App.WorkerID,
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

func (q *redisOrderQueue) Subscribe(ctx context.Context, handler func(ctx context.Context, msg *domain.OrderMessage) error) error {
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

		// Block for 2 seconds waiting for messages
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, ">"},
			Count:    10,
			Block:    2 * time.Second,
		}).Result()

		if err != nil {
			// redis.Nil means "no new messages within Block window" — a
			// SUCCESS case, not a failure.
			if errors.Is(err, redis.Nil) {
				consecutiveErrors = 0
				continue
			}

			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveReadErrors {
				return fmt.Errorf("XReadGroup: %d consecutive errors, last: %w", consecutiveErrors, err)
			}

			// Self-healing: If group is missing (e.g. after FLUSHALL), recreate it.
			if strings.Contains(err.Error(), "NOGROUP") {
				q.logger.Warn(ctx, "XReadGroup Error: NOGROUP. Attempting to recreate group...",
					mlog.Int("consecutive_errors", consecutiveErrors))
				if ensureErr := q.EnsureGroup(ctx); ensureErr != nil {
					q.logger.Error(ctx, "Failed to recreate group", tag.Error(ensureErr))
				}
				time.Sleep(1 * time.Second)
				continue
			}

			// Log error and sleep briefly
			q.logger.Error(ctx, "XReadGroup Error",
				tag.Error(err), mlog.Int("consecutive_errors", consecutiveErrors))
			time.Sleep(1 * time.Second)
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
					q.ackOrLog(ctx, msg.ID)
					continue
				}

				// Process with Retry
				if err := q.processWithRetry(ctx, handler, orderMsg); err != nil {
					// Exhausted retries. handleFailure returns false
					// when compensation failed — leave in PEL for retry.
					if q.handleFailure(ctx, orderMsg, msg, err) {
						q.ackOrLog(ctx, msg.ID)
					}
				} else {
					q.ackOrLog(ctx, msg.ID)
				}
			}
		}
	}
}

// ackOrLog ACKs a message and logs any ACK failure. A failed ACK leaves the
// message in the Pending Entries List (PEL) and will be re-processed on
// the next processPending cycle — but we MUST log so silent redelivery
// is operator-visible.
func (q *redisOrderQueue) ackOrLog(ctx context.Context, msgID string) {
	if err := q.client.XAck(ctx, streamKey, groupName, msgID).Err(); err != nil {
		q.logger.Error(ctx, "XAck failed — message will be re-delivered via PEL",
			tag.Error(err), tag.MsgID(msgID))
	}
}

func (q *redisOrderQueue) processWithRetry(ctx context.Context, handler func(ctx context.Context, msg *domain.OrderMessage) error, msg *domain.OrderMessage) error {
	const maxRetries = 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		if err := handler(ctx, msg); err == nil {
			return nil
		} else {
			lastErr = err
			// Backoff while honoring ctx cancellation so shutdown is prompt
			// (addresses action-list item M8).
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i+1) * 100 * time.Millisecond):
			}
		}
	}
	return lastErr
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
func (q *redisOrderQueue) handleFailure(ctx context.Context, orderMsg *domain.OrderMessage, rawMsg redis.XMessage, err error) bool {
	// Background context with timeout: compensation MUST run even if the
	// parent ctx was cancelled mid-processing.
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if revertErr := q.inventoryRepo.RevertInventory(bgCtx, orderMsg.EventID, orderMsg.Quantity, rawMsg.ID); revertErr != nil {
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
		"original_id": msg.ID,
		"error":       err.Error(),
		"failed_at":   time.Now().Format(time.RFC3339),
	}
	// Copy original values
	for k, v := range msg.Values {
		values[k] = v
	}

	if addErr := q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqKey,
		Values: values,
	}).Err(); addErr != nil {
		q.logger.Error(ctx, "XAdd to DLQ failed",
			tag.Error(addErr),
			mlog.String("original_id", msg.ID),
			mlog.String("dlq", dlqKey),
		)
		return fmt.Errorf("moveToDLQ XAdd: %w", addErr)
	}
	return nil
}

func parseMessage(msg redis.XMessage) (*domain.OrderMessage, error) {
	// Values is map[string]interface{}. The Lua producer writes user_id /
	// event_id / quantity as strings; we reject anything that isn't a
	// well-formed integer so silent ID=0 records never reach the worker.

	userIDStr, ok := msg.Values["user_id"].(string)
	if !ok {
		return nil, fmt.Errorf("missing user_id")
	}

	eventIDStr, ok := msg.Values["event_id"].(string)
	if !ok {
		return nil, fmt.Errorf("missing event_id")
	}

	qtyStr, ok := msg.Values["quantity"].(string)
	if !ok {
		return nil, fmt.Errorf("missing quantity")
	}

	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
	}
	eventID, err := strconv.Atoi(eventIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid event_id %q: %w", eventIDStr, err)
	}
	qty, err := strconv.Atoi(qtyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid quantity %q: %w", qtyStr, err)
	}

	return &domain.OrderMessage{
		ID:       msg.ID,
		UserID:   userID,
		EventID:  eventID,
		Quantity: qty,
	}, nil
}

// processPending fetches and processes messages from the Pending Entries List (PEL).
func (q *redisOrderQueue) processPending(ctx context.Context, consumerName string, handler func(ctx context.Context, msg *domain.OrderMessage) error) error {
	q.logger.Info(ctx, "Checking for pending messages (PEL)...")

	for {
		// XREADGROUP with ID "0" fetches pending messages for check consumer.
		// Block:100ms instead of 0 so the call honors shutdown signals
		// even when the stream is empty (addresses action-list M9).
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, "0"}, // "0" = Pending messages
			Count:    10,
			Block:    100 * time.Millisecond,
		}).Result()

		if err != nil {
			if errors.Is(err, redis.Nil) {
				return nil
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
				q.ackOrLog(ctx, msg.ID)
				continue
			}

			// Process with Retry
			if err := q.processWithRetry(ctx, handler, orderMsg); err != nil {
				// handleFailure returns false when compensation failed;
				// leaving in PEL means next cycle retries revert + DLQ.
				if q.handleFailure(ctx, orderMsg, msg, err) {
					q.ackOrLog(ctx, msg.ID)
				}
			} else {
				q.ackOrLog(ctx, msg.ID)
				q.logger.Info(ctx, "Recovered and processed pending message", tag.MsgID(msg.ID))
			}
		}
	}
}
