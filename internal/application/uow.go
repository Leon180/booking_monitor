package application

import (
	"context"

	"booking_monitor/internal/domain"
)

// Repositories bundles the per-aggregate domain ports that participate
// in a multi-aggregate transaction. The bundle exists at the application
// layer (not domain) because cross-aggregate transactional coordination
// is a use-case concern: per Vernon (IDDD) one aggregate is one
// consistency boundary, and joining several aggregates in a single tx
// is an application-level concession (e.g. transactional outbox).
type Repositories struct {
	Order      domain.OrderRepository
	Event      domain.EventRepository
	Outbox     domain.OutboxRepository
	TicketType domain.TicketTypeRepository
}

//go:generate mockgen -source=uow.go -destination=../mocks/uow_mock.go -package=mocks

// UnitOfWork opens a transaction, builds a Repositories bundle whose
// methods all run within that tx, and hands the bundle to fn.
//
// Contract:
//   - fn returning nil           -> commit
//   - fn returning a non-nil err -> rollback (err returned verbatim)
//   - rollback errors are logged by the implementation; never overwrite
//     the original fn error
//
// The Repositories handed to fn MUST NOT escape the closure — caching
// it (e.g. assigning to a longer-lived field) lets a tx-bound repo leak
// to a concurrent caller, the foot-gun documented in Angus Morrison's
// atomic-repositories analysis. The implementation builds a fresh
// bundle per Do invocation; consumers keep that property by treating
// `repos` as request-scoped.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(repos *Repositories) error) error
}
