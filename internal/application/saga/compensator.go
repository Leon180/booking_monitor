package saga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// stepError is a typed error wrapper used by the compensator's
// UoW closure to mark which step failed, so the outer
// HandleOrderFailed can classify the outcome label without
// fragile string matching against `fmt.Errorf` messages. The
// `step` value is one of: "getbyid", "list_ticket_type",
// "incrementticket", "markcompensated". Mapped to the
// `<step>_error` outcome label by `outcomeForStepError` below.
// Steps: "getbyid", "list_ticket_type", "incrementticket",
// "markcompensated", "record_completion".
//
// Wraps the underlying error so callers using `errors.Is` /
// `errors.As` against domain sentinels still work end-to-end.
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return fmt.Sprintf("compensator step %q: %v", e.step, e.err) }
func (e *stepError) Unwrap() error { return e.err }

// outcomeForStepError maps a stepError's step name to the
// outcome label string. Returns "" if the error is not a
// stepError (caller falls through to "unknown").
func outcomeForStepError(err error) string {
	var stepE *stepError
	if errors.As(err, &stepE) {
		return stepE.step + "_error"
	}
	return ""
}

type Compensator interface {
	// HandleOrderFailed processes a single order.failed event.
	// `originatedAt` is `events_outbox.created_at` threaded through
	// `kafka.Message.Time` by the saga consumer — used as the
	// histogram's start time so `ObserveLoopDuration` measures
	// END-TO-END compensation latency (outbox poll delay + Kafka
	// round-trip + UoW + Redis revert), not just the compensator's
	// own work. Tests + non-Kafka callers can pass `time.Now()` to
	// get compensator-only latency.
	HandleOrderFailed(ctx context.Context, payload []byte, originatedAt time.Time) error
}

type compensator struct {
	inventoryRepo domain.InventoryRepository
	sagaCompRepo  domain.SagaCompensationRepository // non-tx, long-lived; used outside UoW
	uow           application.UnitOfWork
	log           *mlog.Logger
	metrics       CompensatorMetrics
}

// NewCompensator takes the logger as an explicit dependency rather
// than reaching for zap's globals, matching the pattern used by
// WorkerService and keeping tests deterministic (L7).
//
// All DB work happens inside uow.Do so the per-aggregate repos are
// resolved off the closure parameter; the Redis revert outside the
// tx still uses the long-lived inventoryRepo.
//
// sagaCompRepo is the NON-TX long-lived handle used outside the UoW
// for WasRedisReverted / MarkRedisReverted. The tx-bound clone
// (repos.SagaCompletion inside Do) is used only for RecordCompletion.
//
// PR-D12.4 added the `metrics CompensatorMetrics` parameter.
// Metrics are injected DIRECTLY (not via a decorator) because three
// of HandleOrderFailed's return paths (compensated /
// already_compensated / path_c_skipped) all return `nil`,
// indistinguishable from outside. The compensator must self-record
// its outcome before each return.
func NewCompensator(
	inventoryRepo domain.InventoryRepository,
	sagaCompRepo domain.SagaCompensationRepository,
	uow application.UnitOfWork,
	logger *mlog.Logger,
	metrics CompensatorMetrics,
) Compensator {
	return &compensator{
		inventoryRepo: inventoryRepo,
		sagaCompRepo:  sagaCompRepo,
		uow:           uow,
		log:           logger.With(mlog.String("component", "saga_compensator")),
		metrics:       metrics,
	}
}

// HandleOrderFailed processes one order.failed event from Kafka and
// runs the compensation (PG IncrementTicket + Redis revert.lua +
// MarkCompensated). PR-D12.4 instruments every return path:
//
//   - Each return point explicitly calls `record(outcome)` BEFORE
//     returning. The `did_record` sentinel + closing `defer` records
//     "unknown" if any path forgets — caught at /metrics scrape time
//     by the SagaCompensatorClassifierDrift alert (Slice 3).
//   - The histogram (`ObserveLoopDuration`) is observed ONLY on the
//     fully-compensated success path (full-path UoW + Redis revert).
//     The `already_compensated` and `path_c_skipped` paths are no-ops
//     latency-wise and would skew p50/p99 to the floor if observed.
//   - Outcome classification on UoW failure uses typed `*stepError`
//     wrappers populated by the closure — NOT string matching against
//     `fmt.Errorf` messages, which is fragile across refactors.
//   - `originatedAt` is the histogram start point. Threaded from
//     `events_outbox.created_at` by Slice 0's data-path foundation;
//     measures END-TO-END latency (outbox polling + Kafka + UoW +
//     Redis), not just compensator work.
//   - UoW infrastructure errors (tx-begin failure, ctx cancellation)
//     get distinct labels (`uow_infra_error` / `context_error`)
//     instead of conflating with the classifier-drift `"unknown"`
//     sentinel — H1 from Slice 2 silent-failure review.
func (s *compensator) HandleOrderFailed(ctx context.Context, payload []byte, originatedAt time.Time) error {
	var didRecord bool
	defer func() {
		if !didRecord {
			// Default-fallthrough sentinel. A future refactor that
			// adds a return path without `record(...)` lands here;
			// the SagaCompensatorClassifierDrift alert fires on
			// `rate(...{outcome="unknown"}[5m]) > 0`. Distinct from
			// `uow_infra_error` / `context_error` (which ARE recorded
			// on the unwrapped-UoW-error paths below).
			s.metrics.RecordEventProcessed("unknown")
		}
	}()
	record := func(outcome string) {
		s.metrics.RecordEventProcessed(outcome)
		didRecord = true
	}

	var event application.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.Error(ctx, "failed to unmarshal event",
			tag.Error(err),
			mlog.ByteString("payload", payload),
		)
		record("unmarshal_error")
		return fmt.Errorf("compensator.HandleOrderFailed unmarshal: %w", err)
	}

	s.log.Info(ctx, "rolling back inventory for failed order",
		tag.OrderID(event.OrderID),
		tag.EventID(event.EventID),
		tag.TicketTypeID(event.TicketTypeID),
		tag.Quantity(event.Quantity),
	)

	// compensationID is shared by the UoW closure (RecordCompletion)
	// and the post-UoW Redis revert path (WasRedisReverted / MarkRedisReverted).
	compensationID := fmt.Sprintf("order:%s", event.OrderID)

	// 1. Rollback PostgreSQL Inventory & Ensure Idempotency via UoW
	ticketTypeIDForRevert := event.TicketTypeID
	var wasAlreadyCompensated bool
	errUow := s.uow.Do(ctx, func(repos *application.Repositories) error {
		order, err := repos.Order.GetByID(ctx, event.OrderID)
		if err != nil {
			return &stepError{step: "getbyid", err: fmt.Errorf("orderRepo.GetByID order_id=%s: %w", event.OrderID, err)}
		}
		if order.Status() == domain.OrderStatusCompensated {
			// Idempotent DB-side short-circuit. We still need a resolved
			// ticket_type_id for the Redis revert below: a previous
			// delivery may have marked the order compensated and then
			// failed on RevertInventory. Retrying only the Redis side is
			// safe because revert.lua is idempotent on compensationID.
			//
			// For modern payloads event.TicketTypeID is already set; for
			// legacy rolling-upgrade payloads we must re-run the fallback
			// resolution so ticketTypeIDForRevert is non-zero.
			wasAlreadyCompensated = true
			ticketTypeID, _, err := s.resolveTicketTypeID(ctx, repos, event)
			if err != nil {
				return &stepError{step: "list_ticket_type", err: err}
			}
			// PR #127 B7: discard `found` here. On the already-compensated
			// idempotent-replay path the Redis revert below uses
			// ticketTypeIDForRevert defensively — uuid.Nil propagates
			// through revert.lua's namespace lookup as the legacy
			// `orderID`-scoped key (per the dual-namespace contract
			// surfaced by CAP A7). Discarding the explicit found flag is
			// safe because this branch ends with `return nil` after the
			// idempotency check, not with a path that calls IncrementTicket.
			ticketTypeIDForRevert = ticketTypeID
			return nil
		}

		// D4.1 follow-up: increment the per-ticket-type counter (the
		// SoT the API surfaces). The legacy events.available_tickets
		// is frozen post-D4.1; its IncrementTicket is preserved for
		// backward compat but not called here.
		//
		// Resolve which ticket_type to increment via three paths:
		//
		//   Path A — clean (post-v3 wire format):
		//     event.TicketTypeID is non-nil; use directly.
		//
		//   Path B — legacy fallback (pre-v3 message in flight during
		//     rolling upgrade):
		//     event.TicketTypeID == uuid.Nil. Look up ticket_types for
		//     the event_id; D4.1's default-single-ticket-type-per-event
		//     pattern means exactly one row in 99% of cases. Use that
		//     row's id.
		//
		//   Path C — unrecoverable (D8 multi-ticket-type future + a
		//     legacy message somehow still in flight):
		//     event.TicketTypeID == uuid.Nil AND > 1 ticket_type per
		//     event. Skipping the DB increment is the conservative
		//     choice — wrong row means corrupting the visible counter,
		//     no row means manual review (the order's `failed` status
		//     is preserved so ops can find it). The Redis revert below
		//     also skips because the runtime key is ticket_type scoped.
		ticketTypeID, found, err := s.resolveTicketTypeID(ctx, repos, event)
		if err != nil {
			return &stepError{step: "list_ticket_type", err: err}
		}
		ticketTypeIDForRevert = ticketTypeID
		// PR #127 B7: branch on the explicit `found` flag instead of
		// comparing against uuid.Nil. Path C (unrecoverable) returns
		// (uuid.Nil, false, nil); the DB IncrementTicket is skipped
		// (wrong-row corruption is worse than counter drift, which ops
		// can reconcile out-of-band — see Path-C comment above).
		if found {
			if err := repos.TicketType.IncrementTicket(ctx, ticketTypeID, event.Quantity); err != nil {
				return &stepError{step: "incrementticket", err: fmt.Errorf("ticketTypeRepo.IncrementTicket ticket_type=%s: %w", ticketTypeID, err)}
			}
		}
		// else: Path C skipped DB increment — Redis revert below still
		// runs. MarkCompensated still applies because the saga has
		// completed its user-visible work (Redis inventory is
		// authoritative for the read path; the DB ticket_type counter
		// drift is operationally visible and can be reconciled out-of-
		// band by ops).

		if err := repos.Order.MarkCompensated(ctx, event.OrderID); err != nil {
			return &stepError{step: "markcompensated", err: fmt.Errorf("orderRepo.MarkCompensated order_id=%s: %w", event.OrderID, err)}
		}

		// Atomically record the compensation in PG — the PG-based idempotency
		// guard that survives Redis crashes (Seata TCC fence pattern).
		// ON CONFLICT DO NOTHING makes this safe for Kafka redelivery.
		n, err := repos.SagaCompletion.RecordCompletion(ctx, compensationID, event.OrderID)
		if err != nil {
			return &stepError{step: "record_completion", err: fmt.Errorf("sagaCompRepo.RecordCompletion order_id=%s: %w", event.OrderID, err)}
		}
		if n == 0 {
			// Anomalous: compensationID already existed on a wasAlreadyCompensated=false
			// path. Structurally impossible under normal operation; indicates a
			// compensationID collision or manual DB intervention.
			s.log.Error(ctx, "saga: RecordCompletion was no-op (row pre-existed on first-time path — investigate compensationID collision)",
				tag.OrderID(event.OrderID))
		}
		return nil
	})

	if errUow != nil {
		s.log.Error(ctx, "failed to rollback DB inventory", tag.OrderID(event.OrderID), tag.Error(errUow))
		// Classify the wrapped step error. Three buckets:
		//
		//   1. *stepError wrapper present → record `<step>_error`.
		//      One of getbyid_error / list_ticket_type_error /
		//      incrementticket_error / markcompensated_error.
		//   2. context.Canceled / context.DeadlineExceeded — the
		//      UoW propagated an upstream cancellation. Distinct
		//      label so ops can filter shutdown noise from real
		//      failures.
		//   3. Any other UoW-infrastructure error (tx-begin failure,
		//      connection-pool exhaustion, etc.) → `uow_infra_error`.
		//      Reserves the deferred `"unknown"` sentinel for true
		//      classifier drift (a refactor that forgot to record).
		if outcome := outcomeForStepError(errUow); outcome != "" {
			record(outcome)
		} else if errors.Is(errUow, context.Canceled) || errors.Is(errUow, context.DeadlineExceeded) {
			record("context_error")
		} else {
			record("uow_infra_error")
		}
		return errUow // Retry — already wrapped inside the closure
	}

	// 2. Rollback Redis Inventory (Hot path). Idempotency is enforced
	// by the Lua script via an EXISTS-then-SET guard on a compensation
	// key (see revert.lua for the crash-safety trade-off).
	//
	// When wasAlreadyCompensated=true (Kafka redelivery after DB
	// was already final), first check the PG record to see whether
	// the Redis revert already completed. This guard survives Redis
	// crashes: if the Redis key was lost, revert.lua's EXISTS check
	// would not protect us — but the PG redis_reverted_at flag does.
	//
	// ⚠️ revert.lua's EXISTS check on saga:reverted:{compensationID}
	// MUST NOT be removed — it is load-bearing defense-in-depth for
	// the WasRedisReverted fail-open path (when PG check errors).
	if wasAlreadyCompensated {
		if ticketTypeIDForRevert != uuid.Nil {
			done, err := s.sagaCompRepo.WasRedisReverted(ctx, compensationID)
			if err != nil {
				s.log.Warn(ctx, "saga: WasRedisReverted failed; proceeding (revert.lua as fallback)",
					tag.OrderID(event.OrderID), tag.Error(err))
				s.metrics.IncWasRedisRevertedError()
				// fail-open: revert.lua EXISTS guard is the last-resort defence.
			} else if done {
				record("already_compensated")
				return nil
			}
			// done=false: DB done, Redis revert never completed → fall through
		}
	}

	if ticketTypeIDForRevert == uuid.Nil {
		s.log.Warn(ctx, "skipping Redis inventory revert because ticket_type_id is unavailable",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
		)
		// Disambiguate the two paths that land here. `already_compensated`
		// is the Kafka at-least-once redelivery case where DB was already
		// final. `path_c_skipped` is the rolling-upgrade /
		// multi-ticket-type case where we can't resolve the ticket_type
		// to revert. Both no-op the histogram observation.
		if wasAlreadyCompensated {
			record("already_compensated")
		} else {
			record("path_c_skipped")
		}
		return nil
	}
	if err := s.inventoryRepo.RevertInventory(ctx, ticketTypeIDForRevert, event.Quantity, compensationID); err != nil {
		s.log.Error(ctx, "failed to rollback Redis inventory",
			tag.OrderID(event.OrderID),
			tag.Error(err),
		)
		// Distinguish the IDEMPOTENT RE-DRIVE failure from the FRESH
		// compensation Redis failure. Both correspond to "Redis can't
		// revert inventory right now" but the operator triage is
		// different: idempotent re-drive failures are usually benign
		// retries the SETNX guard short-circuited downstream; fresh
		// compensation failures are real inventory leaks. (H2 from
		// Slice 2 silent-failure review.)
		if wasAlreadyCompensated {
			record("already_compensated_redis_error")
		} else {
			record("redis_revert_error")
		}
		return fmt.Errorf("inventoryRepo.RevertInventory order_id=%s: %w", event.OrderID, err)
	}

	// Persist redis_reverted_at so future redeliveries can skip the
	// Redis revert via WasRedisReverted=true. NOT best-effort: failure
	// here propagates so the Kafka consumer retries the whole message.
	// On retry, revert.lua's EXISTS guard (7-day TTL) makes RevertInventory
	// a no-op, then MarkRedisReverted gets a fresh attempt. This closes
	// the double-revert gap that a best-effort nil-return left open: if we
	// returned nil here, the PG fence would never record completion, and a
	// redelivery after the EXISTS key expires could double-revert Redis →
	// cache_high ghost ticket that can never sell out.
	if err := s.sagaCompRepo.MarkRedisReverted(ctx, compensationID); err != nil {
		s.log.Warn(ctx, "saga: MarkRedisReverted failed, triggering Kafka retry",
			tag.OrderID(event.OrderID), tag.Error(err))
		s.metrics.IncMarkRedisRevertedError()
		if wasAlreadyCompensated {
			record("already_compensated_redis_mark_failed")
		} else {
			record("compensated_redis_mark_failed")
		}
		return fmt.Errorf("saga: MarkRedisReverted order_id=%s: %w", event.OrderID, err)
	}

	s.log.Info(ctx, "rollback successful", tag.OrderID(event.OrderID))
	// Histogram observed ONLY on the fully-compensated success path
	// — `already_compensated` is sub-millisecond no-op work and
	// would skew p50/p99 to the floor.
	//
	// `originatedAt` is `events_outbox.created_at` (threaded through
	// `kafka.Message.Time` by the consumer per Slice 0's data path);
	// `time.Since(originatedAt)` measures end-to-end saga latency
	// including outbox poll delay, Kafka round-trip, and compensator
	// work — what dashboards/alerts actually need.
	if wasAlreadyCompensated {
		record("already_compensated")
	} else {
		s.metrics.ObserveLoopDuration(time.Since(originatedAt))
		record("compensated")
	}
	return nil
}

// resolveTicketTypeID maps the OrderFailedEvent to the ticket_type that
// IncrementTicket should target, per the three-path resolution
// documented in HandleOrderFailed:
//
//   - Path A (clean):       event.TicketTypeID != uuid.Nil → (id, true, nil).
//   - Path B (legacy):      ListByEventID returns exactly 1 row → (id, true, nil).
//                           Logs Warn so ops see the rolling-upgrade window.
//   - Path C (unrecoverable): ListByEventID returns 0 or > 1 rows →
//                           (uuid.Nil, false, nil). Caller skips DB increment
//                           but continues to Redis revert + MarkCompensated.
//                           Logs Error so the case is operator-visible.
//
// ListByEventID lookup errors propagate up wrapped.
//
// PR #127 B7: replaced the (uuid.Nil, nil) magic-value sentinel with an
// explicit `found bool` return so caller branches are obvious from the
// signature and don't depend on the caller remembering to compare
// against uuid.Nil.
func (s *compensator) resolveTicketTypeID(ctx context.Context, repos *application.Repositories, event application.OrderFailedEvent) (uuid.UUID, bool, error) {
	if event.TicketTypeID != uuid.Nil {
		return event.TicketTypeID, true, nil
	}

	// Legacy event (wire format v < 3) caught in Kafka during rolling
	// upgrade. Try a per-event lookup as a recovery path.
	ticketTypes, err := repos.TicketType.ListByEventID(ctx, event.EventID)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("ticketTypeRepo.ListByEventID event_id=%s (legacy event fallback): %w", event.EventID, err)
	}
	switch len(ticketTypes) {
	case 1:
		// D4.1 default-single-ticket-type-per-event — the dominant
		// case during rolling upgrade. Safe to attribute the
		// compensation to this row.
		s.log.Warn(ctx, "saga compensator: legacy event missing ticket_type_id; resolved via single-ticket-type fallback (rolling-upgrade window)",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
			tag.TicketTypeID(ticketTypes[0].ID()),
		)
		return ticketTypes[0].ID(), true, nil
	case 0:
		// Event has no ticket_types — data corruption (event was
		// created pre-D4.1 and never migrated). Manual review needed.
		s.log.Error(ctx, "saga compensator: legacy event missing ticket_type_id AND no ticket_types found for event; DB ticket_type increment skipped — manual review required",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
		)
		return uuid.Nil, false, nil
	default:
		// > 1 ticket_type — D8 multi-ticket-type expansion + a legacy
		// pre-v3 message still in Kafka. We CANNOT pick the right one
		// without the explicit TicketTypeID. Skip rather than corrupt.
		s.log.Error(ctx, "saga compensator: legacy event missing ticket_type_id AND multiple ticket_types per event; cannot disambiguate, DB ticket_type increment skipped — manual review required",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
			mlog.Int("ticket_type_count", len(ticketTypes)),
		)
		return uuid.Nil, false, nil
	}
}
