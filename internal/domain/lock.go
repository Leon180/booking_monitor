package domain

import "context"

// DistributedLock represents an exclusive lock mechanism to ensure only
// a single instance of a distributed component is active at any given time.
//
//go:generate mockgen -destination=../mocks/mock_lock.go -package=mocks booking_monitor/internal/domain DistributedLock
type DistributedLock interface {
	// TryLock attempts to acquire the lock using the provided lock ID.
	// It returns true if the lock was successfully acquired, or false if
	// another instance currently holds it. It does not block.
	TryLock(ctx context.Context, lockID int64) (bool, error)

	// Unlock releases a previously acquired lock.
	Unlock(ctx context.Context, lockID int64) error
}
