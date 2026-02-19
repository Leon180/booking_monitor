package cache

import (
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"context"
	"fmt"
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
		if err.Error() == "BUSYGROUP Consumer Group name already exists" {
			return nil
		}
		return err
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
			if err == redis.Nil {
				continue
			}

			// Self-healing: If group is missing (e.g. after FLUSHALL), recreate it.
			if err.Error() == "NOGROUP No such key 'orders:stream' or consumer group 'orders:group' in XREADGROUP with GROUP option" {
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
					// Malformed message? Move to DLQ immediately without compensation?
					// Or just Log and Ack to skip?
					// For safety, let's DLQ.
					q.moveToDLQ(ctx, msg, err)
					q.client.XAck(ctx, streamKey, groupName, msg.ID)
					continue
				}

				// Process with Retry
				if err := q.processWithRetry(ctx, handler, orderMsg); err != nil {
					// Exhausted retries -> DLQ + Compensate
					q.handleFailure(ctx, orderMsg, msg, err)
				} else {
					// Success -> Ack
					q.client.XAck(ctx, streamKey, groupName, msg.ID)
				}
			}
		}
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
			// Exponential backoff or constant
			time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
		}
	}
	return lastErr
}

func (q *redisOrderQueue) handleFailure(ctx context.Context, orderMsg *domain.OrderMessage, rawMsg redis.XMessage, err error) {
	// 1. Compensate Inventory
	// We use background context because we must ensure compensation happens even if request ctx is cancelled
	bgCtx := context.Background()
	_ = q.inventoryRepo.RevertInventory(bgCtx, orderMsg.EventID, orderMsg.Quantity)

	// 2. Move to DLQ
	q.moveToDLQ(bgCtx, rawMsg, err)

	// 3. Ack original message so we don't process it again
	q.client.XAck(bgCtx, streamKey, groupName, rawMsg.ID)
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

	q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqKey,
		Values: values,
	})
}

func parseMessage(msg redis.XMessage) (*domain.OrderMessage, error) {
	// Values is map[string]interface{}
	// "user_id", "event_id", "quantity"
	// Lua script sends them as strings usually

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

	var userID, eventID, qty int
	fmt.Sscanf(userIDStr, "%d", &userID)
	fmt.Sscanf(eventIDStr, "%d", &eventID)
	fmt.Sscanf(qtyStr, "%d", &qty)

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
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, "0"}, // "0" = Pending messages
			Count:    10,
			Block:    0,
		}).Result()

		if err != nil {
			return err
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
				q.client.XAck(ctx, streamKey, groupName, msg.ID)
				continue
			}

			// Process with Retry
			if err := q.processWithRetry(ctx, handler, orderMsg); err != nil {
				q.handleFailure(ctx, orderMsg, msg, err)
			} else {
				// Success
				q.client.XAck(ctx, streamKey, groupName, msg.ID)
				q.logger.Infow("Recovered and processed pending message", "msg_id", msg.ID)
			}
		}
	}
}
