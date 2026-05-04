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

// Test harness convention: when a unit test constructs a partial
// `Repositories{}` literal (only populating fields the
// service-under-test actually calls), be explicit about which fields
// are deliberately omitted vs. forgotten. Going forward, prefer
// passing a fresh mock for EVERY field even if no expectations are
// registered — gomock fails loudly on an unexpected call, which is
// strictly better than the nil-panic a partially-populated bundle
// would produce when a future refactor extends the service to touch
// a previously-unused field. Pre-D4.1 test sites that still pass
// `Repositories{Order: ..., Event: ..., Outbox: ...}` without a
// TicketType mock are tolerated only because the services in
// question (worker/payment/saga/recon) genuinely don't touch the
// ticket_type repo today; a future PR that DOES wire ticket-type
// access into any of them MUST update those harnesses in lockstep.

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
