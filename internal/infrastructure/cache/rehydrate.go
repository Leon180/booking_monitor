package cache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// inventoryRehydrateLockID namespaces the Postgres advisory lock used to
// serialise app-startup rehydrate across multi-instance deployments. The
// outbox relay uses 1001; we pick 2001 so namespaces are obviously
// distinct in `pg_locks` queries.
const inventoryRehydrateLockID int64 = 2001

// RehydrateInventory scans events with `available_tickets > 0` from
// Postgres (the source of truth) and populates Redis `event:{id}:qty`
// keys via SETNX. Idempotent: existing Redis keys are NEVER overwritten.
//
// Why this exists.
//   This codebase treats Redis as an ephemeral hot-path cache while
//   Postgres is the source of truth (see `docs/architectural_backlog.md`
//   §"Cache-truth architecture"). Under that contract, Redis must be
//   reconstructible at any time from DB. RehydrateInventory is the
//   explicit reconstruction path. It runs at app startup so every fresh
//   deploy / Redis restart / accidental FLUSHALL converges to the
//   correct state without operator intervention.
//
// Why SETNX, not SET.
//   During normal operation, Redis is AHEAD of DB:
//     - Lua deduct atomically writes `event:{id}:qty -= N` AND emits a
//       stream message
//     - Worker eventually consumes the message and runs DecrementTicket
//       on `events.available_tickets` in a DB transaction
//   So at any moment, Redis qty == DB available_tickets MINUS in-flight
//   deducts that haven't reached the worker yet. Overwriting Redis with
//   the DB value would re-add the in-flight deducts to inventory and
//   cause double-allocation. SETNX preserves the live value when Redis
//   already has one, and only writes when the key is missing (i.e. the
//   case rehydrate is for).
//
// Why advisory lock.
//   Multi-instance startup (k8s rolling deploy, docker-compose scale)
//   would have N pods all scanning events + SETNX'ing simultaneously.
//   SETNX guarantees correctness regardless, but the duplicated DB scan
//   wastes I/O. `pg_try_advisory_lock(2001)` serialises the work to
//   exactly one instance per startup wave; other instances log "skipped"
//   and rely on the leader to populate Redis.
//
// What this is NOT.
//   This is the STARTUP path. Continuous DB↔Redis sync (drift detection,
//   reconciliation of sustained mismatches) is a separate concern owned
//   by the recon subcommand — see backlog PR-D.

// RehydrateInventoryParams bundles the dependencies of the rehydrate
// path so the call site can remain a single positional argument under
// fx. Fields are public so external callers (e.g. the integration test
// harness in PR-B's follow-up) can construct the struct directly without
// going through fx wiring.
//
// D4.1 follow-up: TicketTypeRepo is the new SoT for inventory.
// `events.available_tickets` is frozen (no longer decremented by the
// worker) and reading it would seed Redis with stale `total_tickets`
// values, causing double-booking after a Redis wipe. The actual
// remaining inventory is the SUM across `event_ticket_types` for each
// event — caller resolves via TicketTypeRepo.SumAvailableByEventID.
type RehydrateInventoryParams struct {
	EventRepo      domain.EventRepository
	TicketTypeRepo domain.TicketTypeRepository
	RedisClient    *redis.Client
	Locker         application.DistributedLock
	Cfg            *config.Config
	Logger         *mlog.Logger
}

func RehydrateInventory(ctx context.Context, p RehydrateInventoryParams) error {
	log := p.Logger.With(mlog.String("component", "inventory_rehydrate"))

	// 1. Try the advisory lock. Other startup instances will see false
	// and skip; the leader runs the rehydrate and unlocks at the end.
	acquired, err := p.Locker.TryLock(ctx, inventoryRehydrateLockID)
	if err != nil {
		return fmt.Errorf("inventoryRehydrate try-lock: %w", err)
	}
	if !acquired {
		log.Info(ctx, "inventory rehydrate skipped: another instance holds the lock")
		return nil
	}
	defer func() {
		// Use a fresh ctx for unlock — the unlock must run even if the
		// parent ctx is being torn down. 5s budget mirrors the
		// outbox-relay unlock pattern.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Locker.Unlock(unlockCtx, inventoryRehydrateLockID); err != nil {
			log.Warn(ctx, "inventory rehydrate unlock failed",
				tag.LockID(inventoryRehydrateLockID), tag.Error(err))
		}
	}()

	// 2. Scan events with available_tickets > 0. Sold-out events are
	// excluded — Lua deduct handles missing keys correctly (DECRBY → -N
	// → revert path → return -1) so we don't need cache entries for
	// them.
	events, err := p.EventRepo.ListAvailable(ctx)
	if err != nil {
		return fmt.Errorf("inventoryRehydrate list events: %w", err)
	}

	// 3. SETNX each event's qty key. Live keys are preserved; missing
	// keys get the DB value with the configured TTL.
	//
	// Failure semantics: a single SETNX error aborts the entire
	// rehydrate. This is INTENTIONAL — if Redis is in a state where
	// even one SETNX fails (network blip, OOM, AUTH failure), the
	// most likely shape of the next attempt is also failure, and
	// proceeding would leave Redis half-populated with no clear
	// signal to the operator. Better to fail-fast: fx aborts startup,
	// k8s liveness probe restarts the pod, the next startup wave
	// retries the whole rehydrate from scratch.
	var rehydrated, skipped, drifted int
	for _, e := range events {
		key := inventoryKey(e.ID())
		// D4.1 follow-up: the actual remaining inventory is the SUM
		// across event_ticket_types (the per-ticket-type SoT), NOT
		// `events.available_tickets` which is frozen post-D4.1. SUM
		// across a single ticket_type (D4.1 default) returns the same
		// value the legacy column would have held; across multiple
		// ticket types (future D8) it aggregates correctly because
		// the Redis key is keyed at event level (the deduct.lua hot
		// path key has not migrated to ticket_type granularity yet).
		dbAvailable, err := p.TicketTypeRepo.SumAvailableByEventID(ctx, e.ID())
		if err != nil {
			return fmt.Errorf("inventoryRehydrate sum ticket_types event=%s: %w", e.ID(), err)
		}
		if dbAvailable <= 0 {
			// Event has no ticket types OR all ticket types are sold
			// out. Either way Redis seed is unnecessary — Lua deduct
			// returns sold-out for missing/zero keys. Skip without
			// counting as "rehydrated".
			continue
		}
		ok, err := p.RedisClient.SetNX(ctx, key, dbAvailable, p.Cfg.Redis.InventoryTTL).Result()
		if err != nil {
			return fmt.Errorf("inventoryRehydrate SETNX event=%s: %w", e.ID(), err)
		}
		if ok {
			rehydrated++
			continue
		}
		// SETNX returned false → the key already exists. Normal cause:
		// app restart while Redis was alive, in-flight deducts mean
		// Redis is AHEAD of DB by some count. We must NOT overwrite
		// (that would re-add the in-flight deducts to inventory).
		//
		// But: SETNX-false silently masks "key exists with WRONG value"
		// (corruption, manual tinkering, NOGROUP-aftermath). Detect
		// the latter by GET-ing the existing value and comparing.
		//
		// Drift definition: Redis value > DB value. Redis < DB is
		// EXPECTED during normal operation (in-flight deducts).
		// Redis > DB cannot happen via the deduct-then-DB-decrement
		// flow; it only happens when something put the wrong value in.
		skipped++
		existingStr, getErr := p.RedisClient.Get(ctx, key).Result()
		if getErr != nil {
			if errors.Is(getErr, redis.Nil) {
				// Race: key existed at SETNX time, expired before GET.
				// Ignore — it'll be (correctly) repopulated on next access.
				continue
			}
			log.Warn(ctx, "inventory rehydrate drift-check GET failed",
				tag.EventID(e.ID()), tag.Error(getErr))
			continue
		}
		existing, parseErr := strconv.Atoi(existingStr)
		if parseErr != nil {
			log.Warn(ctx, "inventory rehydrate drift-check parse failed",
				tag.EventID(e.ID()),
				mlog.String("redis_value", existingStr), tag.Error(parseErr))
			continue
		}
		if existing > dbAvailable {
			drifted++
			observability.InventoryRehydrateDriftTotal.Inc()
			log.Warn(ctx, "inventory rehydrate drift detected (Redis > DB)",
				tag.EventID(e.ID()),
				mlog.Int("redis_qty", existing),
				mlog.Int("db_available_tickets", dbAvailable),
				mlog.Int("drift", existing-dbAvailable),
			)
		}
	}

	log.Info(ctx, "inventory rehydrate complete",
		mlog.Int("events_total", len(events)),
		mlog.Int("events_rehydrated", rehydrated),
		mlog.Int("events_skipped_already_present", skipped),
		mlog.Int("events_drifted", drifted),
	)
	return nil
}
