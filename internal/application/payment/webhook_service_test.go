package payment_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// ─── helpers ─────────────────────────────────────────────────────────

// fakeWebhookMetrics records every invocation so tests can assert
// label values + call counts without depending on the prometheus
// adapter. Mirrors the project's `NopMetrics + recorder` precedent.
type fakeWebhookMetrics struct {
	received       []string
	duplicate      []string
	unknownIntent  []string
	intentMismatch int
	lateSuccess    []string
	unsupported    []string
}

func (f *fakeWebhookMetrics) Received(result string)              { f.received = append(f.received, result) }
func (f *fakeWebhookMetrics) Duplicate(prior string)              { f.duplicate = append(f.duplicate, prior) }
func (f *fakeWebhookMetrics) UnknownIntent(reason string)         { f.unknownIntent = append(f.unknownIntent, reason) }
func (f *fakeWebhookMetrics) IntentMismatch()                     { f.intentMismatch++ }
func (f *fakeWebhookMetrics) LateSuccess(detectedAt string)       { f.lateSuccess = append(f.lateSuccess, detectedAt) }
func (f *fakeWebhookMetrics) UnsupportedType(eventType string)    { f.unsupported = append(f.unsupported, eventType) }

// newWebhookFixture stamps out the common 4-tuple every test wants.
// expectedLiveMode = false (test mode) — flip via cfg arg for the
// cross-env case.
func newWebhookFixture(t *testing.T, expectedLiveMode bool) (
	*gomock.Controller,
	payment.WebhookService,
	*mocks.MockOrderRepository,
	*mocks.MockUnitOfWork,
	*fakeWebhookMetrics,
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	metrics := &fakeWebhookMetrics{}
	svc := payment.NewWebhookService(repo, uow, metrics, expectedLiveMode, mlog.NewNop())
	return ctrl, svc, repo, uow, metrics
}

// reservedOrder constructs an awaiting-payment order with a fresh
// reservation TTL. `intentID` may be empty to model the orphan-repair
// path (`SetPaymentIntentID` raced and lost).
func reservedOrder(orderID, eventID uuid.UUID, intentID string, reservedUntil time.Time) domain.Order {
	return domain.ReconstructOrder(
		orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusAwaitingPayment,
		time.Now(), reservedUntil,
		intentID, 4000, "usd",
	)
}

func paidOrder(orderID, eventID uuid.UUID, intentID string) domain.Order {
	return domain.ReconstructOrder(
		orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusPaid,
		time.Now(), time.Now().Add(15*time.Minute),
		intentID, 4000, "usd",
	)
}

func envelope(orderID uuid.UUID, intentID, eventType string) payment.Envelope {
	return payment.Envelope{
		ID:       "evt_" + uuid.NewString(),
		Type:     eventType,
		Created:  time.Now().Unix(),
		LiveMode: false,
		Data: payment.EnvelopeData{
			Object: payment.PaymentIntentObject{
				ID:          intentID,
				Status:      "succeeded",
				AmountCents: 4000,
				Currency:    "usd",
				Metadata:    map[string]string{payment.MetadataKeyOrderID: orderID.String()},
			},
		},
	}
}

func failureEnvelope(orderID uuid.UUID, intentID string) payment.Envelope {
	env := envelope(orderID, intentID, payment.EventTypePaymentIntentPaymentFailed)
	env.Data.Object.Status = "requires_payment_method"
	env.Data.Object.LastPaymentError = &payment.PaymentIntentLastError{
		Code:    "card_declined",
		Message: "your card was declined",
	}
	return env
}

// ─── happy paths ─────────────────────────────────────────────────────

func TestHandleWebhook_HappyPath_Succeeded(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_test_123"
	order := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))

	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	repos := &application.Repositories{Order: repo}
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repo.EXPECT().MarkPaid(gomock.Any(), orderID).Return(nil)
			return fn(repos)
		},
	)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err)
	assert.Equal(t, []string{"succeeded"}, metrics.received)
}

func TestHandleWebhook_HappyPath_PaymentFailed(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_test_456"
	order := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))

	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	outboxRepo := mocks.NewMockOutboxRepository(ctrl)
	repos := &application.Repositories{Order: repo, Outbox: outboxRepo}
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repo.EXPECT().MarkPaymentFailed(gomock.Any(), orderID).Return(nil)
			outboxRepo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.OutboxEvent{}, nil)
			return fn(repos)
		},
	)

	err := svc.HandleWebhook(context.Background(), failureEnvelope(orderID, intentID))
	require.NoError(t, err)
	assert.Equal(t, []string{"failed"}, metrics.received)
}

// ─── orphan repair (SetPaymentIntentID race) ────────────────────────

func TestHandleWebhook_OrphanRepair_PersistsIntentBeforeMarkPaid(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_orphan_999"
	// payment_intent_id is empty — this is the SetPaymentIntentID race orphan.
	order := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))
	order = domain.ReconstructOrder(orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusAwaitingPayment,
		time.Now(), time.Now().Add(10*time.Minute),
		"", 4000, "usd")
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	repos := &application.Repositories{Order: repo}
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			gomock.InOrder(
				repo.EXPECT().SetPaymentIntentID(gomock.Any(), orderID, intentID).Return(nil),
				repo.EXPECT().MarkPaid(gomock.Any(), orderID).Return(nil),
			)
			return fn(repos)
		},
	)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err)
	assert.Equal(t, []string{"succeeded"}, metrics.received)
}

// ─── duplicate / race fallback ───────────────────────────────────────

func TestHandleWebhook_TerminalStatus_IdempotentDuplicate(t *testing.T) {
	ctrl, svc, repo, _, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_dup_1"
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(paidOrder(orderID, eventID, intentID), nil)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err)
	assert.Empty(t, metrics.received, "duplicate path must NOT count as a fresh receipt")
	assert.Equal(t, []string{"paid"}, metrics.duplicate)
}

func TestHandleWebhook_ConcurrentMarkPaid_RaceFallbackToTerminal(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_race_1"

	// First GetByID returns AwaitingPayment, but MarkPaid loses to a
	// concurrent webhook redelivery → ErrInvalidTransition. The race
	// fallback re-reads and finds the order is already Paid → 200 dup.
	awaiting := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))
	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), orderID).Return(awaiting, nil),
	)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repo.EXPECT().MarkPaid(gomock.Any(), orderID).Return(domain.ErrInvalidTransition)
			return fn(&application.Repositories{Order: repo})
		},
	)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(paidOrder(orderID, eventID, intentID), nil)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err, "concurrent race must NOT bubble out as 500")
	assert.Empty(t, metrics.received, "race fallback resolves to duplicate, not received")
	assert.Equal(t, []string{"paid"}, metrics.duplicate)
}

// ─── late success (Codex round-3 P1 #2) ──────────────────────────────

func TestHandleWebhook_LateSuccess_ServiceCheck_RoutesToExpiredPath(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_late_1"
	// reserved_until in the PAST → service-level guard catches it.
	expired := reservedOrder(orderID, eventID, intentID, time.Now().Add(-1*time.Second))
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(expired, nil)

	outboxRepo := mocks.NewMockOutboxRepository(ctrl)
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(nil)
			outboxRepo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.OutboxEvent{}, nil)
			return fn(&application.Repositories{Order: repo, Outbox: outboxRepo})
		},
	)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err)
	assert.Equal(t, []string{"service_check"}, metrics.lateSuccess,
		"service-level guard must record service_check label")
	assert.Empty(t, metrics.received, "late_success path does NOT count as a normal receipt")
}

// Codex round-5 P1 fix: a `succeeded` webhook arriving AFTER D6 has
// already moved the order to `expired` (or saga to `compensated`)
// used to classify as a clean duplicate and silently 200-ACK; money
// had moved at the provider but no late_success metric / log fired.
// Verify the fix routes through handleLateSuccess with
// `detected_at="post_terminal"` so the manual refund alert pages,
// and the inner UoW's MarkExpired-on-terminal-row hits the
// ErrInvalidTransition→re-read→terminal path cleanly (no double
// transition, no duplicate saga emit).
func TestHandleWebhook_LateSuccess_PostTerminal_SucceededOnExpired(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_late_post_terminal"

	expired := domain.ReconstructOrder(
		orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusExpired,
		time.Now().Add(-20*time.Minute),
		time.Now().Add(-5*time.Minute),
		intentID, 4000, "usd",
	)
	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), orderID).Return(expired, nil), // outer resolve
	)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(domain.ErrInvalidTransition)
			return fn(&application.Repositories{Order: repo})
		},
	)
	// handleLateSuccess re-read after the UoW returned ErrInvalidTransition.
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(expired, nil)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err, "succeeded-on-terminal-failure must NOT 5xx — handler logs + metrics + 200")
	assert.Equal(t, []string{"post_terminal"}, metrics.lateSuccess,
		"succeeded event on expired order must emit late_success{detected_at=post_terminal}")
	assert.Empty(t, metrics.duplicate,
		"this is NOT a duplicate — money moved AFTER reservation died; do not silently swallow")
}

func TestHandleWebhook_LateSuccess_SQLPredicate_RoutesToExpiredPath(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_late_sql"
	// Order looks live to the service check (reserved_until in the
	// future at GetByID time) but the SQL predicate fires
	// ErrReservationExpired during the UoW (race window).
	stillLive := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(stillLive, nil)

	gomock.InOrder(
		uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, fn func(*application.Repositories) error) error {
				repo.EXPECT().MarkPaid(gomock.Any(), orderID).Return(domain.ErrReservationExpired)
				return fn(&application.Repositories{Order: repo})
			},
		),
		uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, fn func(*application.Repositories) error) error {
				outboxRepo := mocks.NewMockOutboxRepository(ctrl)
				repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(nil)
				outboxRepo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.OutboxEvent{}, nil)
				return fn(&application.Repositories{Order: repo, Outbox: outboxRepo})
			},
		),
	)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.NoError(t, err)
	assert.Equal(t, []string{"sql_predicate"}, metrics.lateSuccess)
}

// ─── intent mismatch (Codex round-2 P2) ──────────────────────────────

func TestHandleWebhook_IntentMismatch_RefusesToFlip(t *testing.T) {
	ctrl, svc, repo, _, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	dbIntent := "pi_real_for_order"
	webhookIntent := "pi_FORGED_or_drift"
	order := reservedOrder(orderID, eventID, dbIntent, time.Now().Add(10*time.Minute))
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, webhookIntent, payment.EventTypePaymentIntentSucceeded))
	require.ErrorIs(t, err, payment.ErrWebhookIntentMismatch)
	assert.Equal(t, 1, metrics.intentMismatch)
}

// ─── fallback resolution + unknown intent ────────────────────────────

func TestHandleWebhook_MetadataMissing_FallbackToFindByPaymentIntentID(t *testing.T) {
	ctrl, svc, repo, uow, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_legacy_1"
	order := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))

	// metadata.order_id absent → fallback to FindByPaymentIntentID
	repo.EXPECT().FindByPaymentIntentID(gomock.Any(), intentID).Return(order, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repo.EXPECT().MarkPaid(gomock.Any(), orderID).Return(nil)
			return fn(&application.Repositories{Order: repo})
		},
	)

	env := envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded)
	env.Data.Object.Metadata = nil // legacy intent — no metadata
	err := svc.HandleWebhook(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{"succeeded"}, metrics.received)
}

func TestHandleWebhook_UnknownIntent_500ForRetry(t *testing.T) {
	ctrl, svc, repo, _, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	intentID := "pi_unknown_999"
	repo.EXPECT().FindByPaymentIntentID(gomock.Any(), intentID).Return(domain.Order{}, domain.ErrOrderNotFound)

	env := envelope(uuid.Nil, intentID, payment.EventTypePaymentIntentSucceeded)
	env.Data.Object.Metadata = nil // no metadata + intent_id not on file
	err := svc.HandleWebhook(context.Background(), env)
	require.ErrorIs(t, err, payment.ErrWebhookUnknownIntent,
		"orphan rescue path must surface as 500 so provider retries")
	assert.Equal(t, []string{"not_found"}, metrics.unknownIntent)
}

// ─── livemode mismatch ───────────────────────────────────────────────

func TestHandleWebhook_LiveModeMismatch_200NoOp(t *testing.T) {
	ctrl, svc, _, _, metrics := newWebhookFixture(t, false) // we expect false (test mode)
	defer ctrl.Finish()

	env := envelope(uuid.New(), "pi_x", payment.EventTypePaymentIntentSucceeded)
	env.LiveMode = true // mismatch

	err := svc.HandleWebhook(context.Background(), env)
	require.NoError(t, err, "livemode mismatch must NOT 5xx (would retry-storm prod)")
	assert.Equal(t, []string{"cross_env_livemode"}, metrics.unknownIntent)
}

// ─── unsupported / unexpected status ─────────────────────────────────

func TestHandleWebhook_UnsupportedEventType_200NoOp(t *testing.T) {
	ctrl, svc, repo, _, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_canceled"
	order := reservedOrder(orderID, eventID, intentID, time.Now().Add(10*time.Minute))
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, "payment_intent.canceled"))
	require.NoError(t, err)
	assert.Equal(t, []string{"payment_intent.canceled"}, metrics.unsupported)
	assert.Equal(t, []string{"unsupported"}, metrics.received)
}

func TestHandleWebhook_UnexpectedStatus_409(t *testing.T) {
	ctrl, svc, repo, _, metrics := newWebhookFixture(t, false)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	intentID := "pi_legacy"
	// Pending = legacy A4 status, never expected to receive a webhook.
	pending := domain.ReconstructOrder(orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusPending, time.Now(), time.Time{}, "", 0, "")
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(pending, nil)

	err := svc.HandleWebhook(context.Background(), envelope(orderID, intentID, payment.EventTypePaymentIntentSucceeded))
	require.ErrorIs(t, err, payment.ErrWebhookUnexpectedStatus)
	assert.Equal(t, []string{"unexpected_status"}, metrics.received)
}
