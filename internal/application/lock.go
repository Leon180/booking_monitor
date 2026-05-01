package application

import "context"

// DistributedLock is the application-layer port for "ensure only a
// single instance of a distributed component is active." Lives in
// `application` because the only consumer is `OutboxRelay` (an
// application service); the implementation
// (`internal/infrastructure/persistence/postgres.NewPostgresDistributedLock`)
// is the boundary adapter.
//
// Previously lived in `internal/domain/`. Moved here in CP2.5 because
// it has no domain-rule semantics — it's a pure infrastructure-port
// shape with a 2-method interface that any leader-election primitive
// can satisfy. Per the project's coding-style rule "accept interfaces"
// + the Phase 2 checkpoint architecture cleanup theme, ports belong
// near their consumers.
//
//go:generate mockgen -destination=../mocks/mock_lock.go -package=mocks booking_monitor/internal/application DistributedLock
type DistributedLock interface {
	// TryLock attempts to acquire the lock using the provided lock ID.
	// It returns true if the lock was successfully acquired, or false if
	// another instance currently holds it. It does not block.
	TryLock(ctx context.Context, lockID int64) (bool, error)

	// Unlock releases a previously acquired lock.
	Unlock(ctx context.Context, lockID int64) error
}
