package payment

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

// WebhookService is the application-layer entry point for the D5
// `POST /webhook/payment` HTTP handler. It owns the Pattern A
// state-machine transitions triggered by inbound provider events
// (`payment_intent.succeeded` → MarkPaid, `payment_intent.payment_failed`
// → MarkPaymentFailed + emit `order.failed` for the saga compensator).
//
// SCOPE: D5 only handles `succeeded` and `payment_failed`.
// `payment_intent.canceled` is out-of-scope (plan v5 §Out-of-scope) —
// the dispatcher returns 200 + `unsupported_type` metric.
//
// CONCURRENCY: every method is safe to call from the HTTP handler's
// goroutine. State changes go through the repository layer's atomic
// SQL transitions; the orphan-repair `SetPaymentIntentID` call sits
// in the same UoW as MarkPaid so a partial commit cannot land.
type WebhookService interface {
	// HandleWebhook dispatches a parsed envelope. Idempotent on
	// duplicate provider redeliveries. Returns one of the typed
	// sentinels below so the handler can map to a precise HTTP code.
	HandleWebhook(ctx context.Context, env Envelope) error
}

// Webhook-side typed errors. Why a separate set from D4's:
//   - The handler wants distinct HTTP code mappings (D4 mostly 4xx,
//     D5 has 4xx for client-shape problems and 5xx for "ask the
//     provider to retry" semantics).
//   - The metric labels ("unknown_intent{not_found}", "intent_mismatch",
//     etc.) only make sense in the webhook surface and would clutter
//     the existing `/pay` error contract if shared.
var (
	// ErrWebhookUnknownIntent fires when neither the metadata.order_id
	// primary lookup nor the payment_intent_id fallback resolved an
	// order. Handler returns 500 so the provider retries — this is
	// the rescue path for the SetPaymentIntentID race documented at
	// service.go:331; silently 200-ing here would permanently swallow
	// the only success signal we get for that race's orphans.
	ErrWebhookUnknownIntent = errors.New("webhook: unknown payment intent")

	// ErrWebhookIntentMismatch fires when metadata.order_id hits an
	// order whose persisted `payment_intent_id` is non-empty AND
	// different from the webhook's `object.id`. Either an attacker
	// forged metadata, a leaked test fixture drifted into prod, or a
	// real provider bug. Handler returns 500 to retry the provider
	// AND the alert is immediate-critical (single-event paging) so a
	// human investigates — we MUST NOT auto-resolve.
	ErrWebhookIntentMismatch = errors.New("webhook: payment intent mismatch")

	// ErrWebhookUnexpectedStatus fires when the order is in a Pattern A
	// status we don't expect to receive a webhook for (Pending /
	// Charging / Confirmed / Failed — legacy A4 states). Handler maps
	// to 409. Could indicate a real bug if it grows; the alert is
	// soak-based.
	ErrWebhookUnexpectedStatus = errors.New("webhook: order in unexpected status")
)

// WebhookMetrics captures the counters the handler + service emit.
// Behind an interface so the unit test can use a fake recorder and
// the production wiring (`internal/infrastructure/observability/`)
// supplies a Prometheus-backed implementation. Keeps the service
// package free of a Prometheus dependency at the application layer.
type WebhookMetrics interface {
	// Received is incremented on every fully-handled event with one
	// of: "succeeded" / "failed" / "unsupported" / "unexpected_status"
	// / "persist_failed" / "malformed".
	Received(result string)
	// Duplicate fires when an inbound event hits a row already in a
	// terminal state. `priorStatus` is the existing status string.
	Duplicate(priorStatus string)
	// UnknownIntent fires on the no-resolution path; reason is
	// "not_found" or "cross_env_livemode".
	UnknownIntent(reason string)
	// IntentMismatch fires on metadata.order_id ↔ payment_intent_id
	// mismatch — single-event paging.
	IntentMismatch()
	// LateSuccess fires on a `succeeded` event whose reservation has
	// elapsed. `detectedAt` is "service_check" or "sql_predicate".
	// Single-event paging — every occurrence requires a manual refund.
	LateSuccess(detectedAt string)
	// UnsupportedType fires on event types not in {succeeded, payment_failed}.
	UnsupportedType(eventType string)
}

type webhookService struct {
	orderRepo        domain.OrderRepository
	uow              application.UnitOfWork
	metrics          WebhookMetrics
	expectedLiveMode bool
	log              *mlog.Logger
	now              func() time.Time
}

// NewWebhookService wires the application service. expectedLiveMode
// comes from config: production deployments set true (live keys
// configured), test/dev set false (test keys). The handler 200-no-ops
// any envelope whose livemode disagrees so a misrouted test webhook
// can't error-storm a prod listener.
func NewWebhookService(
	orderRepo domain.OrderRepository,
	uow application.UnitOfWork,
	metrics WebhookMetrics,
	expectedLiveMode bool,
	logger *mlog.Logger,
) WebhookService {
	return &webhookService{
		orderRepo:        orderRepo,
		uow:              uow,
		metrics:          metrics,
		expectedLiveMode: expectedLiveMode,
		log:              logger.With(mlog.String("component", "payment_webhook")),
		now:              time.Now,
	}
}

// isTerminalForWebhook reports whether `s` is a status that means
// "this order has already been resolved; an inbound webhook is a
// duplicate redelivery and should 200 no-op". Includes both Pattern A
// terminal states (Paid / Expired / PaymentFailed / Compensated) so
// the saga compensator's eventual `MarkCompensated` doesn't get
// retro-actively reverted by a slow provider redelivery.
func isTerminalForWebhook(s domain.OrderStatus) bool {
	switch s {
	case domain.OrderStatusPaid,
		domain.OrderStatusPaymentFailed,
		domain.OrderStatusCompensated,
		domain.OrderStatusExpired:
		return true
	}
	return false
}

func (s *webhookService) HandleWebhook(ctx context.Context, env Envelope) error {
	// 1. Cross-env guard. A test-mode signature reaching a prod
	//    listener should ACK without retry storm.
	if env.LiveMode != s.expectedLiveMode {
		s.log.Warn(ctx, "webhook livemode mismatch — ignoring",
			mlog.String("envelope_id", env.ID),
			mlog.String("event_type", env.Type))
		s.metrics.UnknownIntent("cross_env_livemode")
		return nil
	}

	obj := env.Data.Object

	// 2. Resolve order. Primary = metadata.order_id; fallback = payment_intent_id.
	//    `resolveOrderID` emits a metric only on the recognised
	//    `ErrWebhookUnknownIntent` branch (the orphan-rescue 500 case).
	//    Transient DB / repo errors are surfaced verbatim for the
	//    provider to retry, but they don't get a label here — operators
	//    should reach for the repo-side `db_*` counters in that case.
	orderID, source, err := s.resolveOrderID(ctx, obj)
	if err != nil {
		return err
	}

	order, err := s.orderRepo.GetByID(ctx, orderID)
	if err != nil {
		// ErrOrderNotFound here is impossible if resolution succeeded
		// against the same DB seconds ago — surface as 500.
		s.log.Error(ctx, "webhook: GetByID after resolve failed",
			tag.OrderID(orderID), tag.Error(err))
		return err
	}

	// 3. Intent ID consistency check (Codex round-2 P2 fix). When we
	//    resolved via metadata.order_id, an order whose persisted
	//    payment_intent_id disagrees with the webhook's object.id is
	//    either an attack or a leaked test fixture — refuse to flip.
	//    The fallback path (source == "fallback") cannot trigger this
	//    branch because the lookup itself was BY payment_intent_id.
	if source == "metadata" && order.PaymentIntentID() != "" && order.PaymentIntentID() != obj.ID {
		s.log.Error(ctx, "webhook: intent id mismatch on metadata-resolved order",
			tag.OrderID(orderID),
			mlog.String("envelope_id", env.ID),
			mlog.String("order_intent_id", order.PaymentIntentID()),
			mlog.String("webhook_intent_id", obj.ID))
		s.metrics.IntentMismatch()
		return ErrWebhookIntentMismatch
	}

	// 4. Idempotency — duplicate redelivery against an already-resolved
	//    order returns 200 no-op so the provider stops retrying.
	if isTerminalForWebhook(order.Status()) {
		s.log.Info(ctx, "webhook: duplicate against terminal order",
			tag.OrderID(orderID),
			tag.Status(string(order.Status())),
			mlog.String("envelope_id", env.ID))
		s.metrics.Duplicate(string(order.Status()))
		return nil
	}
	if order.Status() != domain.OrderStatusAwaitingPayment {
		// Pending / Charging / Confirmed / Failed (legacy A4 states).
		// Webhook arrives against an order that never went through
		// Pattern A — likely a test fixture or a stale order. 409.
		s.log.Warn(ctx, "webhook: order in unexpected status",
			tag.OrderID(orderID),
			tag.Status(string(order.Status())),
			mlog.String("envelope_id", env.ID))
		s.metrics.Received("unexpected_status")
		return ErrWebhookUnexpectedStatus
	}

	// 5. Verdict dispatch.
	switch env.Type {
	case EventTypePaymentIntentSucceeded:
		return s.handleSuccess(ctx, order, obj.ID)
	case EventTypePaymentIntentPaymentFailed:
		reason := "unknown"
		if obj.LastPaymentError != nil {
			reason = formatLastError(obj.LastPaymentError)
		}
		return s.handleFailure(ctx, order, reason)
	default:
		// Unsupported event type — 200 ACK so the provider doesn't
		// retry-storm us on a class of events we don't understand.
		s.log.Info(ctx, "webhook: unsupported event type",
			mlog.String("envelope_id", env.ID),
			mlog.String("event_type", env.Type))
		s.metrics.UnsupportedType(env.Type)
		s.metrics.Received("unsupported")
		return nil
	}
}

// resolveOrderID returns (orderID, source, error). `source` is one of
// "metadata" or "fallback" — used by the caller to decide whether the
// intent-id consistency check applies.
func (s *webhookService) resolveOrderID(ctx context.Context, obj PaymentIntentObject) (uuid.UUID, string, error) {
	if raw, ok := obj.Metadata[MetadataKeyOrderID]; ok {
		if parsed, err := uuid.Parse(raw); err == nil && parsed != uuid.Nil {
			return parsed, "metadata", nil
		}
		s.log.Warn(ctx, "webhook: metadata.order_id present but unparseable; falling back",
			mlog.String("raw", raw),
			mlog.String("intent_id", obj.ID))
	}
	// Fallback: query by payment_intent_id. Reaches the orphan-repair
	// path only when our /pay successfully called the gateway but
	// failed to persist the intent_id (the doc-comment race at
	// service.go:331).
	order, err := s.orderRepo.FindByPaymentIntentID(ctx, obj.ID)
	if err != nil {
		if errors.Is(err, domain.ErrOrderNotFound) {
			s.log.Error(ctx, "webhook: unknown intent — neither metadata nor lookup matched",
				mlog.String("intent_id", obj.ID))
			s.metrics.UnknownIntent("not_found")
			return uuid.Nil, "", ErrWebhookUnknownIntent
		}
		return uuid.Nil, "", fmt.Errorf("webhook resolveOrderID: FindByPaymentIntentID intent=%s: %w", obj.ID, err)
	}
	return order.ID(), "fallback", nil
}

// handleSuccess implements the 5b/5c/5d/5e flow from plan v5:
//   - 5a service-level reservation pre-check (handled inline)
//   - 5b atomic UoW: orphan-repair SetPaymentIntentID + MarkPaid
//   - 5c ErrReservationExpired (from either repo call) → late-success path
//   - 5d ErrInvalidTransition → re-read terminal → 200 idempotent
//   - 5e other → 500 propagate
func (s *webhookService) handleSuccess(ctx context.Context, order domain.Order, intentID string) error {
	// 5a. Service-level guard (belt-and-suspenders before the SQL
	//     predicate). Cheap + lets us record `detected_at=service_check`
	//     vs `sql_predicate` separately so ops can tell whether the
	//     race window is mostly the gateway round-trip (service_check
	//     dominates) or post-handler-entry (sql_predicate dominates).
	if !order.ReservedUntil().After(s.now()) {
		return s.handleLateSuccess(ctx, order, intentID, "service_check")
	}

	err := s.uow.Do(ctx, func(repos *application.Repositories) error {
		// Orphan repair: row has no intent_id yet (the SetPaymentIntentID
		// race fired). Write it BEFORE MarkPaid so future webhook
		// redeliveries find the row via the metadata path naturally,
		// not by the fallback. Both calls live in the same tx so a
		// partial commit can't leave a row marked Paid without an
		// intent_id.
		if order.PaymentIntentID() == "" {
			if e := repos.Order.SetPaymentIntentID(ctx, order.ID(), intentID); e != nil {
				return e
			}
		}
		return repos.Order.MarkPaid(ctx, order.ID())
	})
	if err == nil {
		s.log.Info(ctx, "webhook: order paid",
			tag.OrderID(order.ID()),
			mlog.String("intent_id", intentID))
		s.metrics.Received("succeeded")
		return nil
	}

	// 5c. SQL predicate detected the reservation expired between our
	//     service-level check and the UPDATE. Either SetPaymentIntentID
	//     OR MarkPaid surfaced it.
	if errors.Is(err, domain.ErrReservationExpired) {
		return s.handleLateSuccess(ctx, order, intentID, "sql_predicate")
	}

	// 5d. Concurrent webhook race OR intent_id mismatch on
	//     SetPaymentIntentID. Re-read; terminal status = idempotent
	//     200 (Stripe redelivery hit during our handle window).
	if errors.Is(err, domain.ErrInvalidTransition) {
		cur, getErr := s.orderRepo.GetByID(ctx, order.ID())
		if getErr != nil {
			s.log.Error(ctx, "webhook: race fallback GetByID failed",
				tag.OrderID(order.ID()),
				mlog.NamedError("original_error", err),
				tag.Error(getErr))
			return getErr
		}
		if isTerminalForWebhook(cur.Status()) {
			s.log.Info(ctx, "webhook: lost concurrent race — terminal already",
				tag.OrderID(order.ID()),
				tag.Status(string(cur.Status())))
			s.metrics.Duplicate(string(cur.Status()))
			return nil
		}
		// Non-terminal under ErrInvalidTransition is a genuine
		// inconsistency — surface so we don't silently mark the
		// order Paid in the wrong state. Most likely the
		// SetPaymentIntentID intent-id mismatch case (the row's
		// intent_id is non-empty AND different from ours), but at
		// this layer we already filtered that via step 3 — so this
		// branch landing means a row state we don't model. Loud +
		// short-circuit: don't fall through into 5e where the
		// `persist_failed` metric label would mis-classify a
		// state-machine inconsistency as a transient DB problem.
		s.log.Error(ctx, "webhook: ErrInvalidTransition with non-terminal status — possible bug",
			tag.OrderID(order.ID()),
			tag.Status(string(cur.Status())),
			tag.Error(err))
		s.metrics.Received("persist_failed")
		return err
	}

	// 5e. Anything else — DB outage, ctx cancellation, repository
	//     error. Surface so the provider retries.
	s.log.Error(ctx, "webhook: handleSuccess UoW failed",
		tag.OrderID(order.ID()),
		tag.Error(err))
	s.metrics.Received("persist_failed")
	return err
}

// handleLateSuccess: a `succeeded` webhook arrived but the reservation
// has elapsed (either service-level or SQL predicate caught it).
//
// Why we don't just MarkPaid: that would soft-lock inventory beyond
// the user's promised window, breaking the reservation TTL contract
// and giving the customer "paid for an expired reservation".
//
// Why we don't silently drop: the customer's money DID move at the
// provider — silently dropping leaves "I paid but have no ticket"
// forever invisible. We need a paper trail for the manual refund.
//
// Outcome: walk the order to Expired (saga path, NOT Paid) + emit
// `order.failed` so the saga compensator reverts Redis inventory. A
// critical alert pages an operator who initiates the provider-side
// refund. Stripe's `refunds.create` is the typical real-world
// follow-up; out of D5 scope (no auto-refund — manual review by design).
func (s *webhookService) handleLateSuccess(ctx context.Context, order domain.Order, intentID, detectedAt string) error {
	s.log.Error(ctx, "webhook: late success on expired reservation — refund required",
		tag.OrderID(order.ID()),
		mlog.String("intent_id", intentID),
		mlog.String("detected_at", detectedAt),
		mlog.String("reserved_until", order.ReservedUntil().Format(time.RFC3339)))
	s.metrics.LateSuccess(detectedAt)

	err := s.uow.Do(ctx, func(repos *application.Repositories) error {
		if e := repos.Order.MarkExpired(ctx, order.ID()); e != nil {
			return e
		}
		failedEvent := application.NewOrderFailedEventFromOrder(
			order,
			"late_success_after_expiry: intent_id="+intentID+" detected_at="+detectedAt,
		)
		payload, e := json.Marshal(failedEvent)
		if e != nil {
			return e
		}
		outboxEvent, e := domain.NewOrderFailedOutbox(payload)
		if e != nil {
			return e
		}
		_, e = repos.Outbox.Create(ctx, outboxEvent)
		return e
	})
	if err == nil {
		// Provider gets 200 ACK — we accepted the event AND walked it
		// into the saga. The actual refund is operator-initiated.
		return nil
	}
	// MarkExpired ErrInvalidTransition fallback: D6 sweeper raced us
	// and already flipped to Expired. Re-read; terminal = idempotent
	// (the saga will cover or already covered). Non-terminal under
	// ErrInvalidTransition mirrors handleSuccess 5d — surface as a
	// possible-bug log + return immediately so persist_failed metric
	// doesn't mis-classify a state-machine inconsistency.
	if errors.Is(err, domain.ErrInvalidTransition) {
		cur, getErr := s.orderRepo.GetByID(ctx, order.ID())
		if getErr != nil {
			return getErr
		}
		if isTerminalForWebhook(cur.Status()) {
			s.log.Info(ctx, "webhook: late success — D6 sweeper raced; terminal already",
				tag.OrderID(order.ID()),
				tag.Status(string(cur.Status())))
			return nil
		}
		s.log.Error(ctx, "webhook: handleLateSuccess ErrInvalidTransition with non-terminal status — possible bug",
			tag.OrderID(order.ID()),
			tag.Status(string(cur.Status())),
			tag.Error(err))
		s.metrics.Received("persist_failed")
		return err
	}
	s.log.Error(ctx, "webhook: handleLateSuccess UoW failed",
		tag.OrderID(order.ID()),
		tag.Error(err))
	s.metrics.Received("persist_failed")
	return err
}

// handleFailure: `payment_intent.payment_failed` →
// MarkPaymentFailed + emit `order.failed` (saga compensator reverts
// Redis inventory). NO reservation-window guard here — failure is
// failure, the saga has to run regardless of TTL.
func (s *webhookService) handleFailure(ctx context.Context, order domain.Order, reason string) error {
	err := s.uow.Do(ctx, func(repos *application.Repositories) error {
		if e := repos.Order.MarkPaymentFailed(ctx, order.ID()); e != nil {
			return e
		}
		failedEvent := application.NewOrderFailedEventFromOrder(order, "payment_failed: "+reason)
		payload, e := json.Marshal(failedEvent)
		if e != nil {
			return e
		}
		outboxEvent, e := domain.NewOrderFailedOutbox(payload)
		if e != nil {
			return e
		}
		_, e = repos.Outbox.Create(ctx, outboxEvent)
		return e
	})
	if err == nil {
		s.log.Info(ctx, "webhook: order payment_failed",
			tag.OrderID(order.ID()),
			mlog.String("reason", reason))
		s.metrics.Received("failed")
		return nil
	}
	// Same race fallback as handleSuccess 5d: concurrent MarkPaymentFailed
	// (another webhook redelivery handled-in-parallel) → re-read terminal
	// → idempotent 200. Non-terminal under ErrInvalidTransition mirrors
	// handleSuccess 5d — surface as a possible-bug log + return so the
	// fallthrough doesn't mis-label a state-machine inconsistency.
	if errors.Is(err, domain.ErrInvalidTransition) {
		cur, getErr := s.orderRepo.GetByID(ctx, order.ID())
		if getErr != nil {
			return getErr
		}
		if isTerminalForWebhook(cur.Status()) {
			s.log.Info(ctx, "webhook: handleFailure lost race — terminal already",
				tag.OrderID(order.ID()),
				tag.Status(string(cur.Status())))
			s.metrics.Duplicate(string(cur.Status()))
			return nil
		}
		s.log.Error(ctx, "webhook: handleFailure ErrInvalidTransition with non-terminal status — possible bug",
			tag.OrderID(order.ID()),
			tag.Status(string(cur.Status())),
			tag.Error(err))
		s.metrics.Received("persist_failed")
		return err
	}
	s.log.Error(ctx, "webhook: handleFailure UoW failed",
		tag.OrderID(order.ID()),
		tag.Error(err))
	s.metrics.Received("persist_failed")
	return err
}

// formatLastError stringifies the provider's failure context for the
// `order.failed` saga event. Stripe's actual decline codes are short
// strings ("card_declined", "insufficient_funds", ...) plus a
// human-readable message — both are useful for operator triage.
func formatLastError(e *PaymentIntentLastError) string {
	if e.Code != "" && e.Message != "" {
		return e.Code + ": " + e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return e.Message
}
