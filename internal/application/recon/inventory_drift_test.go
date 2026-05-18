package recon_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// recordingDriftMetrics captures every emission for table-driven
// assertions. Same intent as recordingReconMetrics — assertion sites
// read like `m.detected["cache_high"]` instead of polling Prometheus
// global singletons, which keeps these tests parallel-safe.
type recordingDriftMetrics struct {
	driftedEvents        int
	detected             map[string]int
	listEventsErrors     int
	cacheReadErrors      int
	sweepDurations       []float64
	autoRehydrated      int
	autoRehydrateErrors int
}

func newRecordingDriftMetrics() *recordingDriftMetrics {
	return &recordingDriftMetrics{detected: make(map[string]int)}
}

func (m *recordingDriftMetrics) SetDriftedEventsCount(c int)       { m.driftedEvents = c }
func (m *recordingDriftMetrics) IncDriftDetected(direction string) { m.detected[direction]++ }
func (m *recordingDriftMetrics) IncListEventsErrors()              { m.listEventsErrors++ }
func (m *recordingDriftMetrics) IncCacheReadErrors()               { m.cacheReadErrors++ }
func (m *recordingDriftMetrics) ObserveSweepDuration(s float64) {
	m.sweepDurations = append(m.sweepDurations, s)
}
func (m *recordingDriftMetrics) IncAutoRehydrated()      { m.autoRehydrated++ }
func (m *recordingDriftMetrics) IncAutoRehydrateErrors() { m.autoRehydrateErrors++ }

// driftHarness bundles the mocks + recording metrics + the detector
// itself. Tolerance defaults to 5; each test can override via the
// constructor argument.
type driftHarness struct {
	d          *recon.InventoryDriftDetector
	events     *mocks.MockEventRepository
	ticketType *mocks.MockTicketTypeRepository
	inventory  *mocks.MockInventoryRepository
	metrics    *recordingDriftMetrics
}

func newDriftHarness(t *testing.T, tolerance int) *driftHarness {
	t.Helper()
	return newDriftHarnessOpts(t, tolerance, false)
}

func newDriftHarnessAutoRehydrate(t *testing.T, tolerance int) *driftHarness {
	t.Helper()
	return newDriftHarnessOpts(t, tolerance, true)
}

func newDriftHarnessOpts(t *testing.T, tolerance int, autoRehydrate bool) *driftHarness {
	t.Helper()
	ctrl := gomock.NewController(t)
	events := mocks.NewMockEventRepository(ctrl)
	ticketType := mocks.NewMockTicketTypeRepository(ctrl)
	inv := mocks.NewMockInventoryRepository(ctrl)
	metrics := newRecordingDriftMetrics()

	cfg := recon.DriftConfig{
		SweepInterval:     50 * time.Millisecond,
		AbsoluteTolerance: tolerance,
		AutoRehydrate:     autoRehydrate,
	}
	d := recon.NewInventoryDriftDetector(events, ticketType, inv, cfg, metrics, mlog.NewNop())
	return &driftHarness{d: d, events: events, ticketType: ticketType, inventory: inv, metrics: metrics}
}

// eventWithAvail builds a domain.Event with a known UUID + availableTickets
// using ReconstructEvent (skips invariant validation, which is what we
// want — tests should be free to fabricate any state).
func eventWithAvail(t *testing.T, avail int) domain.Event {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return domain.ReconstructEvent(id, "test event", 1000, avail, 0)
}

// expectDBQty registers the ListByEventID expectation. The detector now
// compares Redis against per-ticket-type availability and treats an
// event as drifted when any ticket type under it drifts. Tests model
// the common D4.1 shape of exactly one ticket type per event.
func (h *driftHarness) expectDBQty(ctx context.Context, e domain.Event, dbQty int) {
	tt := domain.ReconstructTicketType(e.ID(), e.ID(), "Default", 2000, "usd", 1000, dbQty, nil, nil, nil, "", 0)
	h.ticketType.EXPECT().ListByEventID(ctx, e.ID()).Return([]domain.TicketType{tt}, nil)
}

func TestDriftSweep_NoEvents_ZeroGaugeNoCalls(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{}, nil)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 0, h.metrics.driftedEvents, "no events → drifted gauge cleared to 0")
	assert.Empty(t, h.metrics.detected, "no events → no detection counters bumped")
	assert.Len(t, h.metrics.sweepDurations, 1, "exactly one duration observation")
}

func TestDriftSweep_WithinTolerance_NoFlag(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// DB has 100 available, cache has 97 → drift = 3 (within tolerance 5).
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(97, true, nil)
	h.expectDBQty(ctx, e, 100)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 0, h.metrics.driftedEvents, "within tolerance → not flagged")
	assert.Empty(t, h.metrics.detected)
}

func TestDriftSweep_CacheLowExcess_FlagsCacheLowExcess(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// drift = 100 - 50 = 50 > tolerance 5 → cache_low_excess.
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(50, true, nil)
	h.expectDBQty(ctx, e, 100)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 1, h.metrics.driftedEvents)
	assert.Equal(t, 1, h.metrics.detected["cache_low_excess"])
	assert.Zero(t, h.metrics.detected["cache_high"], "cache_high not bumped on positive drift")
}

func TestDriftSweep_CacheHigh_AlwaysFlags(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 1000) // huge tolerance — irrelevant for cache_high
	ctx := context.Background()

	// drift = 100 - 110 = -10 → cache_high (negative drift, always anomalous).
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(110, true, nil)
	h.expectDBQty(ctx, e, 100)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 1, h.metrics.driftedEvents)
	assert.Equal(t, 1, h.metrics.detected["cache_high"], "negative drift bypasses tolerance")
}

func TestDriftSweep_CacheKeyAbsent_FlagsCacheMissing(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// Redis returns (0, false, nil) — key NOT present. DB has 100 →
	// cache_missing branch (rehydrate didn't fire). Distinct from
	// the (0, true) "key present but zeroed" case below.
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(0, false, nil)
	h.expectDBQty(ctx, e, 100)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 1, h.metrics.driftedEvents)
	assert.Equal(t, 1, h.metrics.detected["cache_missing"])
	assert.Zero(t, h.metrics.detected["cache_low_excess"],
		"missing key takes the cache_missing branch")
}

func TestDriftSweep_CachePresentButZero_FlagsCacheLowExcess(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// Redis returns (0, true, nil) — key PRESENT, value 0. DB has 100
	// → drift = 100, exceeds tolerance 5 → cache_low_excess. This is
	// the "decremented all the way down to sold-out" case; distinct
	// from cache_missing because the remediation is "investigate
	// worker stuckness", not "re-run rehydrate".
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(0, true, nil)
	h.expectDBQty(ctx, e, 100)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 1, h.metrics.driftedEvents)
	assert.Equal(t, 1, h.metrics.detected["cache_low_excess"],
		"present-but-zero is cache_low_excess, NOT cache_missing")
	assert.Zero(t, h.metrics.detected["cache_missing"])
}

func TestDriftSweep_MultipleEvents_AggregatesCounts(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// Three events: one within tolerance, two flagged different directions.
	clean := eventWithAvail(t, 100)
	low := eventWithAvail(t, 50)
	high := eventWithAvail(t, 10)

	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{clean, low, high}, nil)
	h.inventory.EXPECT().GetInventory(ctx, clean.ID()).Return(99, true, nil)  // within tolerance
	h.expectDBQty(ctx, clean, 100)
	h.inventory.EXPECT().GetInventory(ctx, low.ID()).Return(20, true, nil)    // cache_low_excess (drift=30)
	h.expectDBQty(ctx, low, 50)
	h.inventory.EXPECT().GetInventory(ctx, high.ID()).Return(15, true, nil)   // cache_high (drift=-5)
	h.expectDBQty(ctx, high, 10)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 2, h.metrics.driftedEvents, "two events flagged out of three")
	assert.Equal(t, 1, h.metrics.detected["cache_low_excess"])
	assert.Equal(t, 1, h.metrics.detected["cache_high"])
}

func TestDriftSweep_ListAvailableError_BumpsListErrors(t *testing.T) {
	t.Parallel()
	// Pre-seed the recording gauge with a non-zero value to confirm
	// the failure path resets it. Otherwise a stale "drifted=N" from
	// the previous sweep would linger and dashboards would silently
	// mis-report "as of last successful sweep, N events drifted" as
	// if it were current.
	h := newDriftHarness(t, 5)
	h.metrics.driftedEvents = 7
	ctx := context.Background()

	dbErr := errors.New("db is down")
	h.events.EXPECT().ListAvailable(ctx).Return(nil, dbErr)

	err := h.d.Sweep(ctx)
	require.Error(t, err, "ListAvailable failure aborts the whole sweep")
	assert.ErrorIs(t, err, dbErr, "wrapped error preserves cause")
	assert.Equal(t, 1, h.metrics.listEventsErrors)
	assert.Equal(t, 0, h.metrics.driftedEvents,
		"gauge MUST be reset to 0 on blind sweep so stale data doesn't leak")
}

func TestDriftSweep_PerEventCacheError_ContinuesSweep(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// First event's cache read fails; second succeeds with drift in
	// tolerance. Sweep MUST continue past the failure.
	e1 := eventWithAvail(t, 100)
	e2 := eventWithAvail(t, 50)

	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e1, e2}, nil)
	h.expectDBQty(ctx, e1, 100)
	h.inventory.EXPECT().GetInventory(ctx, e1.ID()).Return(0, false, errors.New("redis transient"))
	h.expectDBQty(ctx, e2, 50)
	h.inventory.EXPECT().GetInventory(ctx, e2.ID()).Return(50, true, nil)

	require.NoError(t, h.d.Sweep(ctx),
		"per-event cache failure should not abort the whole sweep")
	assert.Equal(t, 1, h.metrics.cacheReadErrors)
	assert.Equal(t, 0, h.metrics.driftedEvents, "second event was clean")
}

func TestDriftSweep_PerEventCtxCancelled_NoCacheReadErrorBump(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	// Single event whose cache read returns context.Canceled.
	// Discrimination from the transient-Redis case: cancellation is
	// graceful (caller initiated), so we MUST NOT bump
	// IncCacheReadErrors — that counter feeds an alert and bumping
	// it on shutdown would page operators on every clean SIGTERM.
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.expectDBQty(ctx, e, 100)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(0, false, context.Canceled)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Zero(t, h.metrics.cacheReadErrors,
		"context.Canceled is graceful — must not pollute the cache-read-error counter")
	assert.Zero(t, h.metrics.driftedEvents)
}

// TestDriftSweep_MultiTTEvent_AllTypesChecked verifies that checkOne iterates
// ALL ticket types and returns the worst outcome. Under the old early-exit
// logic, TT2's cache_high would have been silently skipped because TT1 already
// returned cache_missing. This is the P0 regression guard.
func TestDriftSweep_MultiTTEvent_AllTypesChecked(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)
	ctx := context.Background()

	e := eventWithAvail(t, 150)

	tt1ID, err := uuid.NewV7()
	require.NoError(t, err)
	tt2ID, err := uuid.NewV7()
	require.NoError(t, err)

	tt1 := domain.ReconstructTicketType(tt1ID, e.ID(), "Standard", 2000, "usd", 100, 100, nil, nil, nil, "", 0)
	tt2 := domain.ReconstructTicketType(tt2ID, e.ID(), "VIP", 5000, "usd", 50, 50, nil, nil, nil, "", 0)

	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.ticketType.EXPECT().ListByEventID(ctx, e.ID()).Return([]domain.TicketType{tt1, tt2}, nil)
	// TT1: cache_missing — first non-clean; old code would have returned here
	h.inventory.EXPECT().GetInventory(ctx, tt1ID).Return(0, false, nil)
	// TT2: cache_high (Redis=60 > DB=50) — P0 case; must still be detected
	h.inventory.EXPECT().GetInventory(ctx, tt2ID).Return(60, true, nil)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 1, h.metrics.driftedEvents, "event is drifted")
	assert.Equal(t, 1, h.metrics.detected["cache_missing"], "TT1 cache_missing detected")
	assert.Equal(t, 1, h.metrics.detected["cache_high"],
		"TT2 cache_high MUST be detected even though TT1 already flagged cache_missing")
}

func TestDriftSweep_CtxCancelledAtLoopBoundary_ExitsCleanly(t *testing.T) {
	t.Parallel()
	h := newDriftHarness(t, 5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before sweep — first ListAvailable will receive cancelled ctx

	// Some EventRepository implementations honour ctx cancellation
	// before the actual SQL call (returning ctx.Err() as the error).
	// The detector's behaviour is the same as ListAvailable returning
	// any other error: increment listEventsErrors and propagate.
	h.events.EXPECT().ListAvailable(ctx).Return(nil, context.Canceled)

	err := h.d.Sweep(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ── Auto-rehydrate tests (Gap B, INVENTORY_DRIFT_AUTO_REHYDRATE=true) ──────────
//
// Safety basis: auto-rehydrate is metadata-only. SetTicketTypeMetadata (HSET)
// refreshes the immutable booking snapshot key (event_id, amount_cents,
// currency). The qty key is intentionally NOT restored — SETNX from a stale
// DB value during the async write-behind window would produce cache_high,
// which is P0 in a flash-sale context (false 202 Accepted for non-existent
// inventory). Qty restoration is operator-gated via startup rehydrate.

func TestDriftSweep_AutoRehydrate_MetadataRefresh_Success(t *testing.T) {
	t.Parallel()
	h := newDriftHarnessAutoRehydrate(t, 5)
	ctx := context.Background()

	// cache_missing + AutoRehydrate=true → SetTicketTypeMetadata called (metadata HSET only).
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.expectDBQty(ctx, e, 100)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(0, false, nil)
	h.inventory.EXPECT().SetTicketTypeMetadata(ctx, gomock.Any()).Return(nil)

	require.NoError(t, h.d.Sweep(ctx))
	assert.Equal(t, 1, h.metrics.detected["cache_missing"], "cache_missing counter still bumped")
	assert.Equal(t, 1, h.metrics.autoRehydrated, "metadata HSET succeeded → autoRehydrated++")
	assert.Zero(t, h.metrics.autoRehydrateErrors)
	assert.Equal(t, 1, h.metrics.driftedEvents, "event still counted as drifted (qty key was missing at detection time)")
}

func TestDriftSweep_AutoRehydrate_Error_IncrementsErrors_SweepContinues(t *testing.T) {
	t.Parallel()
	h := newDriftHarnessAutoRehydrate(t, 5)
	ctx := context.Background()

	// SetTicketTypeMetadata returns an error (Redis pool exhaustion etc.).
	// Auto-rehydrate is best-effort: sweep continues, cache_missing counter
	// is still bumped so the InventoryDriftDetected alert fires.
	e := eventWithAvail(t, 100)
	h.events.EXPECT().ListAvailable(ctx).Return([]domain.Event{e}, nil)
	h.expectDBQty(ctx, e, 100)
	h.inventory.EXPECT().GetInventory(ctx, e.ID()).Return(0, false, nil)
	h.inventory.EXPECT().SetTicketTypeMetadata(ctx, gomock.Any()).Return(errors.New("redis pool exhausted"))

	require.NoError(t, h.d.Sweep(ctx), "auto-rehydrate error must not abort the sweep")
	assert.Equal(t, 1, h.metrics.detected["cache_missing"])
	assert.Equal(t, 1, h.metrics.autoRehydrateErrors)
	assert.Zero(t, h.metrics.autoRehydrated)
}
