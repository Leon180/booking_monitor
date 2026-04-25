package application

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

func TestOutboxRelay_ProcessBatch(t *testing.T) {
	ctx := mlog.NewContext(context.Background(), mlog.NewNop(), "")

	id1 := uuid.New()
	id2 := uuid.New()

	tests := []struct {
		name       string
		batchSize  int
		setupMocks func(*mocks.MockOutboxRepository, *mocks.MockEventPublisher)
	}{
		{
			name:      "Success - Publish and Mark Processed",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []domain.OutboxEvent{
					domain.ReconstructOutboxEvent(id1, "test.event", []byte("payload1"), domain.OutboxStatusPending, nil),
					domain.ReconstructOutboxEvent(id2, "test.event", []byte("payload2"), domain.OutboxStatusPending, nil),
				}

				// 1. ListPending
				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// 2. Publish & Mark Processed (Event 1)
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload1")).Return(nil)
				repo.EXPECT().MarkProcessed(ctx, id1).Return(nil)

				// 3. Publish & Mark Processed (Event 2)
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload2")).Return(nil)
				repo.EXPECT().MarkProcessed(ctx, id2).Return(nil)
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
				events := []domain.OutboxEvent{
					domain.ReconstructOutboxEvent(id1, "test.event", []byte("payload1"), domain.OutboxStatusPending, nil),
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
				events := []domain.OutboxEvent{
					domain.ReconstructOutboxEvent(id1, "test.event", []byte("payload1"), domain.OutboxStatusPending, nil),
				}

				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// Publish Success
				pub.EXPECT().Publish(ctx, "test.event", []byte("payload1")).Return(nil)

				// MarkProcessed Fails (Log and Continue)
				repo.EXPECT().MarkProcessed(ctx, id1).Return(errors.New("db update error"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRepo := mocks.NewMockOutboxRepository(ctrl)
			mockPub := mocks.NewMockEventPublisher(ctrl)
			mockLock := mocks.NewMockDistributedLock(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockRepo, mockPub)
			}

			// We test the private method processBatch directly to avoid timing issues with Run()
			relay := NewOutboxRelay(mockRepo, mockPub, tt.batchSize, mockLock, mlog.NewNop())
			relay.processBatch(ctx)
		})
	}
}
