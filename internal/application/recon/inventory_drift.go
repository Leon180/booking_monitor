package recon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// InventoryDriftDetector compares Redis cached inventory against the
// Postgres source-of-truth and reports drift. Sibling sweeper to
// Reconciler in this package: same loop / --once shape (driven by
// cmd/booking-cli/recon.go), separate type because the dependencies
// + outcome semantics are disjoint (no payment gateway, no UoW; reads
// only; never mutates state).
//
// Drift semantics. For each event with `available_tickets > 0`:
//
//	dbQty    = events.available_tickets   (pulled via EventRepository.ListAvailable)
//	cacheQty = GET event:{uuid}:qty        (pulled via InventoryRepository.GetInventory)
//	drift    = dbQty - cacheQty
//
// During healthy operation `drift` is small and POSITIVE: the Redis
// Lua deduct decrements cache first, then the worker confirms the DB
// row asynchronously. Steady-state drift sits at ~ inflight-bookings.
//
// What this detector flags:
//
//   - |drift| > AbsoluteTolerance — operationally suspicious, log + count
//   - drift < 0 (cache > DB) — hard anomaly: only happens if a saga
//     compensation reverted Redis without reverting DB, OR if a manual
//     SetInventory blew past the reset path. Always a counter bump.
//   - cacheQty == 0 with dbQty > 0 — Redis was reset and rehydrate
//     hasn't run yet (or failed); reported as `direction="cache_missing"`
//     so the operator distinguishes "cache fell off" from "real drift".
//
// What this detector explicitly does NOT do:
//
//  1. Auto-correct. Writing to Redis from a sweep loop races against
//     in-flight Lua deduct calls; the only safe correction is operator-
//     gated (re-run startup rehydrate). Detection without auto-mutate
//     is the conservative default — alert + investigate, then act.
//  2. Drift-down on sold-out. ListAvailable filters `available_tickets > 0`;
//     events at zero are excluded because Lua deduct correctly treats a
//     missing key as sold-out (DECRBY → -count → revert → return -1).
//
// Pairs with the alert `InventoryDriftDetected` (counter rate > 0 for
// 5m → operator review). Final piece of the cache-truth roadmap (PR-D
// of A/B/C/D); see docs/architectural_backlog.md "Cache-truth
// architecture" for the full story.
type InventoryDriftDetector struct {
	events    domain.EventRepository
	inventory domain.InventoryRepository
	cfg       DriftConfig
	metrics   DriftMetrics
	log       *mlog.Logger
}

// NewInventoryDriftDetector is the constructor. Same dependency-
// direction discipline as NewReconciler: application code never imports
// infrastructure/{config,observability}; the bootstrap layer owns the
// cfg/metrics translation.
//
// Decorates the logger with `component=inventory_drift` so every log
// line is filterable independently of the Reconciler's
// `component=recon` lines (the two sweepers cohabit one process).
func NewInventoryDriftDetector(
	events domain.EventRepository,
	inventory domain.InventoryRepository,
	cfg DriftConfig,
	metrics DriftMetrics,
	logger *mlog.Logger,
) *InventoryDriftDetector {
	return &InventoryDriftDetector{
		events:    events,
		inventory: inventory,
		cfg:       cfg,
		metrics:   metrics,
		log:       logger.With(mlog.String("component", "inventory_drift")),
	}
}

// SweepInterval exposes the loop cadence so the cmd-side ticker can
// read it without re-deriving from infrastructure cfg.
func (d *InventoryDriftDetector) SweepInterval() time.Duration { return d.cfg.SweepInterval }

// Sweep runs ONE drift-detection cycle. ListAvailable events from DB,
// look up the cache for each, classify drift. Returns the first
// non-recoverable error encountered. Per-event lookup failures (Redis
// transient down) are logged + counted but do NOT abort the sweep —
// detection should run to completion so dashboards reflect the latest
// known state.
//
// ctx semantics: caller's ctx caps the whole sweep. Per-event lookups
// use the parent ctx directly; the per-event Redis GET is sub-ms in
// the steady state and a hung Redis triggers the parent ctx's deadline
// (Sweep's caller wraps with a budget) rather than a per-event timeout
// — no need for the asymmetric child-ctx pattern that Reconciler.resolve
// uses for the slow gateway call.
func (d *InventoryDriftDetector) Sweep(ctx context.Context) error {
	start := time.Now()
	defer func() {
		d.metrics.ObserveSweepDuration(time.Since(start).Seconds())
	}()

	events, err := d.events.ListAvailable(ctx)
	if err != nil {
		// Same "we cannot even SCAN" signal pattern as
		// Reconciler.IncFindStuckErrors — distinct from per-event
		// failures so dashboards can tell "drift detection itself is
		// broken" from "events have drifted".
		//
		// Reset the gauge so a previous sweep's `drifted=N` doesn't
		// linger as stale data when the detector is blind. The
		// listEventsErrors counter is the authoritative "blind"
		// signal; the gauge means "as of last successful sweep,
		// this many events drifted". Without the reset, a degraded
		// DB would freeze the gauge mid-anomaly and dashboards would
		// silently mis-report.
		d.metrics.SetDriftedEventsCount(0)
		d.metrics.IncListEventsErrors()
		return fmt.Errorf("driftDetector Sweep: list events: %w", err)
	}

	if len(events) == 0 {
		d.log.Debug(ctx, "drift sweep: no available events")
		d.metrics.SetDriftedEventsCount(0)
		return nil
	}

	var drifted, skipped int
	for _, e := range events {
		if err := ctx.Err(); err != nil {
			d.log.Info(ctx, "drift sweep: ctx cancelled, exiting at loop boundary", tag.Error(err))
			return err
		}
		switch d.checkOne(ctx, e) {
		case checkOutcomeDrifted:
			drifted++
		case checkOutcomeSkipped:
			skipped++
		}
	}

	// Set gauge to point-in-time count of drifted events. Cleared on
	// the next sweep; the alert's `for: 5m` clause filters transient
	// in-flight noise from sustained corruption.
	d.metrics.SetDriftedEventsCount(drifted)

	// Surface the skipped count alongside the drifted count so an
	// operator reading the structured log immediately spots a sweep
	// that saw most events fail their cache read — the `IncCacheReadErrors`
	// counter alerts on rate, but the per-sweep "events_skipped"
	// log field is the diagnostic that lets a senior eyeball
	// "is this one event flapping or is Redis flaky for everything?".
	d.log.Info(ctx, "drift sweep complete",
		mlog.Int("events_checked", len(events)),
		mlog.Int("events_drifted", drifted),
		mlog.Int("events_skipped", skipped),
	)
	return nil
}

// checkOutcome distinguishes the three terminal states of `checkOne`:
//
//	checkOutcomeClean   — event is healthy, no metric emission, no log
//	checkOutcomeDrifted — event flagged, counter bumped, gauge increments
//	checkOutcomeSkipped — per-event Redis read failed; sweep continues
//	                      under the "log + count + retry next sweep"
//	                      discipline. Counted separately so a sweep that
//	                      saw 90% of events un-checkable doesn't masquerade
//	                      as "low drift" on the gauge.
type checkOutcome int

const (
	checkOutcomeClean checkOutcome = iota
	checkOutcomeDrifted
	checkOutcomeSkipped
)

// checkOne classifies one event's drift and emits metrics + logs.
// Returns the outcome so Sweep can aggregate per-sweep counts.
func (d *InventoryDriftDetector) checkOne(ctx context.Context, e domain.Event) checkOutcome {
	cacheQty, found, err := d.inventory.GetInventory(ctx, e.ID())
	if err != nil {
		// Distinguish ctx cancellation (graceful shutdown) from real
		// Redis failure. Explicit context-error matching is the only
		// safe pattern: `errors.Is(err, ctx.Err())` would silently
		// degrade to `errors.Is(err, nil) == false` whenever ctx is
		// still alive, so an actual context.Canceled bubbling up from
		// a deeper call site would pollute the cache-read-error
		// counter. Always test against the canonical sentinels.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			d.log.Info(ctx, "drift: cache read aborted by ctx cancellation",
				tag.EventID(e.ID()), tag.Error(err))
			return checkOutcomeClean
		}
		d.metrics.IncCacheReadErrors()
		d.log.Warn(ctx, "drift: cache read failed, will retry next sweep",
			tag.EventID(e.ID()), tag.Error(err))
		return checkOutcomeSkipped
	}

	dbQty := e.AvailableTickets()
	drift := dbQty - cacheQty

	// Branch 1 — cache_missing. The Redis key is ABSENT (`found==false`),
	// not just zero. A genuinely-zero key means "decremented all the
	// way down to sold-out" which is a `cache_low_excess` (worker is
	// stuck) — NOT `cache_missing` (rehydrate didn't fire). Treating
	// the two the same would route the operator to the wrong branch
	// of the runbook.
	if !found && dbQty > 0 {
		d.metrics.IncDriftDetected("cache_missing")
		d.log.Warn(ctx, "drift: cache key absent with positive DB inventory",
			tag.EventID(e.ID()),
			mlog.Int("db_qty", dbQty),
		)
		return checkOutcomeDrifted
	}

	// Branch 2 — cache_high (drift < 0). Cache has MORE than DB. This
	// should never happen in healthy operation; only paths producing
	// it are saga-compensation desync or manual SetInventory beyond
	// the reset baseline. Always a counter bump regardless of magnitude.
	if drift < 0 {
		d.metrics.IncDriftDetected("cache_high")
		d.log.Error(ctx, "drift: cache exceeds DB — saga or manual desync suspected",
			tag.EventID(e.ID()),
			mlog.Int("db_qty", dbQty),
			mlog.Int("cache_qty", cacheQty),
			mlog.Int("drift", drift),
		)
		return checkOutcomeDrifted
	}

	// Branch 3 — cache_low_excess. drift > 0 (cache lower than DB) is
	// EXPECTED during in-flight bookings. Only flag when it exceeds
	// the configured tolerance — sustained large positive drift means
	// either the worker is failing to commit (lost messages) or the
	// reconciler is leaking inventory via force-fail (DEF-CRIT path
	// that was supposedly closed; this counter is the canary).
	if drift > d.cfg.AbsoluteTolerance {
		d.metrics.IncDriftDetected("cache_low_excess")
		d.log.Warn(ctx, "drift: cache below DB exceeds tolerance",
			tag.EventID(e.ID()),
			mlog.Int("db_qty", dbQty),
			mlog.Int("cache_qty", cacheQty),
			mlog.Int("drift", drift),
			mlog.Int("tolerance", d.cfg.AbsoluteTolerance),
		)
		return checkOutcomeDrifted
	}

	// Within tolerance — no metric emission, no log line.
	return checkOutcomeClean
}
