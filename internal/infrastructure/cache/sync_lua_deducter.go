package cache

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"booking_monitor/internal/domain"
	_ "embed"
)

// sync_lua_deducter.go — D12 Stage 2 only.
//
// Stage 2's hot path is `EVAL deduct_sync.lua + sync PG INSERT`,
// not `EVAL deduct.lua + XADD orders:stream + async worker`. The
// two scripts share the same metadata read + amount_cents math
// (see `lua/deduct_sync.lua` for the comment that pins them
// together) but Stage 2's variant has no XADD block.
//
// Why a separate type rather than a method on
// redisInventoryRepository: the InventoryRepository interface
// (`internal/domain/inventory.go`) is the contract Stages 3-4 use
// for the async-worker hot path. Adding a sync-deduct method to it
// would force Stages 3-4 to either implement a method they don't
// need or stub it. Splitting the type keeps the interface honest.
//
// The compensation path (revert.lua + saga:reverted SETNX guard)
// IS shared via InventoryRepository.RevertInventory — there is one
// canonical revert across all stages, regardless of which deduct
// script ran in the forward path.

//go:embed lua/deduct_sync.lua
var deductSyncScriptSource string

// syncDeductArgsPool sizes a pooled 1-element slice to match
// deduct_sync.lua's single ARGV (count). Separate pool from
// deductArgsPool / revertArgsPool so a future ARG-count change to
// any one script can't corrupt another path's backing array.
var syncDeductArgsPool = sync.Pool{
	New: func() interface{} {
		s := make([]interface{}, 1)
		return &s
	},
}

// RedisSyncDeducter wraps the Stage 2 deduct_sync.lua script. It is
// constructed at server startup and reused for every booking call;
// the wrapped *redis.Script handles EVALSHA caching + EVAL fallback
// transparently.
//
// Returned as a concrete pointer (not an interface) so the cmd
// binary can wire it directly into the synclua.Service constructor;
// the application-side Deducter interface sits in
// `internal/application/booking/synclua/service.go` for unit-test
// substitutability — this struct satisfies it implicitly.
type RedisSyncDeducter struct {
	client *redis.Client
	script *redis.Script
}

// NewRedisSyncDeducter constructs the Stage 2 deducter. The caller
// owns the *redis.Client lifecycle; this struct holds it as a
// borrowed reference and never closes it.
func NewRedisSyncDeducter(client *redis.Client) *RedisSyncDeducter {
	return &RedisSyncDeducter{
		client: client,
		script: redis.NewScript(deductSyncScriptSource),
	}
}

// Deduct runs deduct_sync.lua against the ticket_type's qty +
// metadata keys. On the success branch it returns the same
// `domain.DeductInventoryResult` shape as the async path's
// `redisInventoryRepository.DeductInventory` so downstream code
// stays uniform across stages.
//
// Branches mirror deduct_sync.lua's return values:
//   - "ok"               → DeductInventoryResult{Accepted:true, ...}
//   - "sold_out"         → DeductInventoryResult{Accepted:false}, nil
//   - "metadata_missing" → DeductInventoryResult{}, ErrTicketTypeRuntimeMetadataMissing
//
// On any other shape the function returns errUnexpectedLuaResult so
// the caller can surface a 500 rather than silently mis-routing the
// booking.
func (d *RedisSyncDeducter) Deduct(ctx context.Context, ticketTypeID uuid.UUID, quantity int) (domain.DeductInventoryResult, error) {
	keys := []string{inventoryKey(ticketTypeID), ticketTypeMetaKey(ticketTypeID)}

	argsPtr := syncDeductArgsPool.Get().(*[]interface{})
	args := *argsPtr
	args[0] = quantity
	defer syncDeductArgsPool.Put(argsPtr)

	raw, err := d.script.Run(ctx, d.client, keys, args...).Result()
	if err != nil {
		return domain.DeductInventoryResult{}, err
	}

	res, ok := raw.([]interface{})
	if !ok || len(res) == 0 {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua result %T: %w", raw, errUnexpectedLuaResult)
	}

	status, ok := res[0].(string)
	if !ok {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua status %T: %w", res[0], errUnexpectedLuaResult)
	}

	switch status {
	case "ok":
		if len(res) != 4 {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected ok payload len=%d: %w", len(res), errUnexpectedLuaResult)
		}
		eventIDStr, ok1 := res[1].(string)
		amountCentsStr, ok2 := res[2].(string)
		currency, ok3 := res[3].(string)
		if !ok1 || !ok2 || !ok3 {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: malformed ok payload: %w", errUnexpectedLuaResult)
		}
		eventID, parseEventErr := uuid.Parse(eventIDStr)
		if parseEventErr != nil {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: parse event_id %q: %w", eventIDStr, parseEventErr)
		}
		amountCents, parseAmountErr := strconv.ParseInt(amountCentsStr, 10, 64)
		if parseAmountErr != nil {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: parse amount_cents %q: %w", amountCentsStr, parseAmountErr)
		}
		return domain.DeductInventoryResult{
			Accepted:    true,
			EventID:     eventID,
			AmountCents: amountCents,
			Currency:    currency,
		}, nil
	case "sold_out":
		return domain.DeductInventoryResult{Accepted: false}, nil
	case "metadata_missing":
		return domain.DeductInventoryResult{}, domain.ErrTicketTypeRuntimeMetadataMissing
	default:
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua status %q: %w", status, errUnexpectedLuaResult)
	}
}
