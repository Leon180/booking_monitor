package application

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/mocks"
	"booking_monitor/pkg/logger"

	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func TestOutboxRelay_ProcessBatch(t *testing.T) {
	// Setup logger
	nopLogger := zap.NewNop().Sugar()
	ctx := logger.WithCtx(context.Background(), nopLogger)

	tests := []struct {
		name       string
		batchSize  int
		setupMocks func(*mocks.MockOutboxRepository, *mocks.MockEventPublisher)
	}{
		{
			name:      "Success - Publish and Mark Processed",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []*domain.OutboxEvent{
					{ID: 1, EventType: "test.event", Payload: []byte("payload1")},
					{ID: 2, EventType: "test.event", Payload: []byte("payload2")},
				}

				// 1. ListPending
				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// 2. Publish & Mark Processed (Event 1)
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload1")).Return(nil)
				repo.EXPECT().MarkProcessed(ctx, 1).Return(nil)

				// 3. Publish & Mark Processed (Event 2)
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload2")).Return(nil)
				repo.EXPECT().MarkProcessed(ctx, 2).Return(nil)
			},
		},
		{
			name:      "Repo Error - ListPending Fails",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				repo.EXPECT().ListPending(ctx, 10).Return(nil, errors.New("db error"))
			},
		},
		{
			name:      "Publisher Error - Skip MarkProcessed",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []*domain.OutboxEvent{
					{ID: 1, EventType: "test.event", Payload: []byte("payload1")},
				}

				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// Publish Fails
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload1")).Return(errors.New("kafka error"))

				// MarkProcessed Should NOT be called
			},
		},
		{
			name:      "Repo Error - MarkProcessed Fails",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []*domain.OutboxEvent{
					{ID: 1, EventType: "test.event", Payload: []byte("payload1")},
				}

				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// Publish Success
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload1")).Return(nil)

				// MarkProcessed Fails (Log and Continue)
				repo.EXPECT().MarkProcessed(ctx, 1).Return(errors.New("db update error"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRepo := mocks.NewMockOutboxRepository(ctrl)
			mockPub := mocks.NewMockEventPublisher(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockRepo, mockPub)
			}

			// We test the private method processBatch directly to avoid timing issues with Run()
			relay := NewOutboxRelay(mockRepo, mockPub, tt.batchSize)
			relay.processBatch(ctx)
		})
	}
}
