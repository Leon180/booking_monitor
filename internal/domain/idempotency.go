package domain

import "context"

// IdempotencyResult stores the cached outcome of a processed request —
// HTTP response data only. The matching request fingerprint is
// returned as a side-channel value from the repository (not embedded
// here) because fingerprint is REQUEST attribute, not response;
// conflating them inside this struct would tie a response model to a
// per-endpoint request-hash concept that may grow over time
// (multiple endpoints, scoped fingerprints, etc.).
//
// No json/db tags — domain types stay JSON-unaware (per
// `.claude/rules/golang/coding-style.md` rule 7). The Redis
// implementation marshals via its own `idempotencyRecord` translator
// (`internal/infrastructure/cache/idempotency.go`) so the wire format
// is owned by the boundary, not the domain.
type IdempotencyResult struct {
	StatusCode int
	Body       string
}

// IdempotencyRepository provides idempotency key storage. Implementations
// store results with a TTL to bound memory.
//
// The Stripe-style contract enforced by this interface (since N4):
//
//   - Get returns both the cached response AND the fingerprint of the
//     request body that originally produced it. Empty fingerprint
//     signals a legacy entry cached BEFORE fingerprinting was added —
//     callers treat this as "match, replay" and write the freshly-
//     computed fingerprint back via Set so subsequent replays are
//     validated. The migration-window vulnerability closes on the
//     FIRST replay of each key (the write-back upgrades the entry in
//     place); worst case is 24h TTL, but per-key the typical window
//     is one client retry away.
//   - Set stores both atomically; a partial state (response without
//     fingerprint, or fingerprint without response) is impossible.
//
// Callers compare the returned fingerprint against the SHA-256 hex of
// the incoming request body to enforce same-key/same-body replay vs
// same-key/different-body 409 Conflict (Stripe API convention).
//go:generate mockgen -source=idempotency.go -destination=../mocks/idempotency_repository_mock.go -package=mocks
type IdempotencyRepository interface {
	// Get returns (result, fingerprint, error). Cache miss → result is
	// nil with empty fingerprint and nil error. Empty fingerprint with
	// non-nil result indicates a legacy (pre-N4) entry — the caller
	// should accept the replay AND write back the new fingerprint via
	// Set to upgrade the entry in place.
	Get(ctx context.Context, key string) (result *IdempotencyResult, fingerprint string, err error)

	// Set stores result + fingerprint atomically under key. The
	// fingerprint argument MUST be a non-empty hex SHA-256 of the
	// originating request body in production code paths — passing
	// "" here is reserved for out-of-band cache-rebuild tooling
	// (none today). An empty fingerprint round-trips through Get as
	// the legacy-entry signal, so production callers passing "" by
	// mistake would invert the lazy-migration semantics: every
	// subsequent replay would treat the new entry as legacy and
	// re-run the write-back loop. The handler's `fingerprint(...)`
	// always returns a 64-char hex string (SHA-256 of any input,
	// including zero bytes, is non-empty), so this constraint holds
	// trivially for the only production caller.
	Set(ctx context.Context, key string, result *IdempotencyResult, fingerprint string) error
}
