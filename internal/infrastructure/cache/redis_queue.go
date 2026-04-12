package cache

import (
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	streamKey = "orders:stream"
	groupName = "orders:group"
	dlqKey    = "orders:dlq"
)

type redisOrderQueue struct {
	client        *redis.Client
	inventoryRepo domain.InventoryRepository
	logger        *zap.SugaredLogger
	consumerName  string
}

func NewRedisOrderQueue(client *redis.Client, inventoryRepo domain.InventoryRepository, logger *zap.SugaredLogger, cfg *config.Config) domain.OrderQueue {
	return &redisOrderQueue{
		client:        client,
		inventoryRepo: inventoryRepo,
		logger:        logger,
		consumerName:  cfg.App.WorkerID,
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
		q.logger.Errorw("Failed to process pending messages during startup", "error", err)
		// We log but continue, ensuring we at least process new messages.
	}

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
			if errors.Is(err, redis.Nil) {
				continue
			}

			// Self-healing: If group is missing (e.g. after FLUSHALL), recreate it.
			if strings.Contains(err.Error(), "NOGROUP") {
				q.logger.Warn("XReadGroup Error: NOGROUP. Attempting to recreate group...")
				if ensureErr := q.EnsureGroup(ctx); ensureErr != nil {
					q.logger.Errorw("Failed to recreate group", "error", ensureErr)
				}
				time.Sleep(1 * time.Second)
				continue
			}

			// Log error and sleep briefly
			q.logger.Errorw("XReadGroup Error", "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				orderMsg, err := parseMessage(msg)
				if err != nil {
					q.logger.Errorw("Malformed message — routing to DLQ",
						"error", err, "msg_id", msg.ID)
					q.moveToDLQ(ctx, msg, err)
					q.ackOrLog(ctx, msg.ID)
					continue
				}

				// Process with Retry
				if err := q.processWithRetry(ctx, handler, orderMsg); err != nil {
					// Exhausted retries -> DLQ + Compensate
					q.handleFailure(ctx, orderMsg, msg, err)
				} else {
					// Success -> Ack
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
		q.logger.Errorw("XAck failed — message will be re-delivered via PEL",
			"error", err, "msg_id", msgID)
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

func (q *redisOrderQueue) handleFailure(ctx context.Context, orderMsg *domain.OrderMessage, rawMsg redis.XMessage, err error) {
	// 1. Compensate Inventory
	// We use background context with timeout because we must ensure compensation happens even if request ctx is cancelled
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if revertErr := q.inventoryRepo.RevertInventory(bgCtx, orderMsg.EventID, orderMsg.Quantity, rawMsg.ID); revertErr != nil {
		q.logger.Errorw("RevertInventory failed during handleFailure",
			"error", revertErr, "event_id", orderMsg.EventID, "quantity", orderMsg.Quantity)
	}

	// 2. Move to DLQ
	q.moveToDLQ(bgCtx, rawMsg, err)

	// 3. Ack original message so we don't process it again
	q.ackOrLog(bgCtx, rawMsg.ID)
}

func (q *redisOrderQueue) moveToDLQ(ctx context.Context, msg redis.XMessage, err error) {
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
		q.logger.Errorw("XAdd to DLQ failed — failure trace lost",
			"error", addErr, "original_id", msg.ID, "dlq", dlqKey)
	}
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
	q.logger.Info("Checking for pending messages (PEL)...")

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
			q.logger.Info("No more pending messages to recover.")
			return nil
		}

		stream := streams[0]
		q.logger.Infow("Recovering pending messages", "count", len(stream.Messages))

		for _, msg := range stream.Messages {
			orderMsg, err := parseMessage(msg)
			if err != nil {
				q.logger.Errorw("Malformed pending message", "id", msg.ID, "error", err)
				q.moveToDLQ(ctx, msg, err)
				q.ackOrLog(ctx, msg.ID)
				continue
			}

			// Process with Retry
			if err := q.processWithRetry(ctx, handler, orderMsg); err != nil {
				q.handleFailure(ctx, orderMsg, msg, err)
			} else {
				// Success
				q.ackOrLog(ctx, msg.ID)
				q.logger.Infow("Recovered and processed pending message", "msg_id", msg.ID)
			}
		}
	}
}
