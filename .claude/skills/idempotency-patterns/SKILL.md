---
name: idempotency-patterns
description: HTTP idempotency-key contracts, fingerprint validation, cache policies, and middleware composition. Activates when designing or reviewing idempotent endpoints, retry-safe operations, or cache-replay logic.
origin: booking_monitor (PR #46-#48)
---

# Idempotency Patterns

Stripe-style idempotency-key implementation with body fingerprinting, lazy migration, and middleware-based composition. Distilled from `booking_monitor`'s N4 PR; references back to that codebase's source files where helpful.

## When to Activate

- Adding `Idempotency-Key` header support to a state-changing HTTP endpoint
- Reviewing existing idempotency implementations for correctness
- Designing cache-replay semantics (same-key/same-body vs same-key/different-body)
- Choosing between cache-as-handler-concern and cache-as-middleware
- Deciding what status codes to cache (2xx vs 4xx vs 5xx)

## The Contract (Stripe-style)

| Scenario | Server response |
| :-- | :-- |
| First request with key `K` | Process normally; cache `(response, sha256(body))` for 24h |
| Same key + **same** body | Replay cached response, set `X-Idempotency-Replayed: true`. **Do NOT invoke business logic again.** |
| Same key + **different** body | **409 Conflict** — do NOT replay (would mislead client into thinking the new request succeeded). Client must use a fresh key. |
| Same key + cached entry has no fingerprint (legacy / pre-fingerprinting) | Replay AND lazily write back the freshly-computed fingerprint so subsequent replays validate |
| 2xx response | Cache it |
| **4xx response** | **Do NOT cache** — burns the key for 24h on a typo'd / transient business state |
| **5xx response** | **Do NOT cache** (deviation from Stripe — see below) |
| Cache GET errored | Fail-open (process the request) AND skip the post-processing Set so a flaky-then-recovered cache doesn't pin transient state |

## Critical design decisions

### Hash function: SHA-256 of raw bytes, no canonicalization

**Use `sha256` of raw body bytes, hex-encoded. Do NOT canonicalize JSON.**

```go
sum := sha256.Sum256(bodyBytes)
fingerprint := hex.EncodeToString(sum[:])
```

Why no canonicalization (e.g. RFC 8785 JCS / sort-keys-before-marshal):

- **Industry convention**: Stripe, Shopify, GitHub Octokit, AWS Lambda Powertools all hash raw bytes
- **Client owns its serialisation**: clients control their own marshaling; the contract is "send byte-identical retries"
- **Canonicalisation costs CPU + introduces ambiguity** ("whose canonical form?") for negligible benefit
- **xxHash / non-cryptographic hashes are unsafe** because the attacker controls the body — a forged collision would let one user's body match another's fingerprint

`sha256.Sum256(nil)` is well-defined and non-empty, so empty-body retries collide harmlessly (the cache key is ALSO part of the lookup; a fingerprint match without a key match is impossible).

### Status code on mismatch: 409 Conflict (not 422)

The IETF draft `draft-ietf-httpapi-idempotency-key-header` says SHOULD use 422, but:

- The draft is currently **expired**
- Stripe and the broader SDK ecosystem use **409**
- Deviating from Stripe breaks client SDKs that pattern-match on status

**Use 409.** Document the choice if a reviewer challenges 422.

### What to cache: 2xx ONLY (deviation from Stripe)

Stripe caches both 2xx and 5xx. The 5xx caching exists to prevent retry-storming a known-degraded gateway, **assuming the 5xx represents stable degraded state**.

For most application-side services, this assumption is wrong:

- Most 5xx are TRANSIENT (Redis blip, DB connection refused once, third-party API hiccup)
- Or programmer-error (unmapped error type → generic 500)
- Pinning these for 24h is **worse customer experience** than letting them retry against a recovered server

**Recommended policy:**

```go
func shouldCacheStatus(status int) bool {
    return status >= 200 && status < 300
}
```

Caveats — when 5xx caching IS appropriate:

- Your 5xx represents a stable rate-limit / quota state (e.g., gateway returning 503 with Retry-After)
- You DON'T have rate-limiting at the edge (nginx / API gateway) — Stripe's policy is the only retry-storm protection
- The stable-5xx case is rare; default to NOT caching 5xx

### Lazy migration for fingerprint introduction

When ADDING fingerprint to an existing cache (entries written before the upgrade have no `fingerprint` field):

```go
type idempotencyRecord struct {
    StatusCode  int    `json:"status_code"`
    Body        string `json:"body"`
    Fingerprint string `json:"fingerprint,omitempty"`  // omitempty for legacy compat
}
```

Treat `cachedFingerprint == ""` as "match, replay, AND write back the fresh fingerprint":

```go
switch cachedFP {
case "":
    // legacy entry: replay + lazy writeback
    repo.Set(ctx, key, cached, freshFingerprint)  // best-effort
    replayCached(c, cached)
case freshFingerprint:
    replayCached(c, cached)
default:
    // mismatch
    c.JSON(409, errorResponse)
}
```

Per-key migration window closes on the FIRST replay; worst case is the cache TTL (24h typically) for keys that never see a replay.

### Middleware vs handler

**Idempotency belongs in middleware, NOT inside the handler.** Cross-cutting concern, endpoint-agnostic — moving it to middleware:

- Shrinks handlers to business logic (~30 lines instead of 150+)
- Adds a second idempotent endpoint via one-line route registration
- Evolves the contract in a single file

The Gin middleware shape requires `gin.ResponseWriter` wrapping to capture the response body for caching:

```go
type captureWriter struct {
    gin.ResponseWriter
    body *bytes.Buffer
}

func (w *captureWriter) Write(p []byte) (int, error) {
    w.body.Write(p)                  // capture
    return w.ResponseWriter.Write(p) // forward to client
}
func (w *captureWriter) WriteString(s string) (int, error) {  // c.String() goes through this
    w.body.WriteString(s)
    return w.ResponseWriter.WriteString(s)
}
```

Wire ordering: body-size middleware (`http.MaxBytesReader`) MUST run BEFORE idempotency so the body read is bounded.

### Body re-feed for downstream binding

The middleware reads the body once via `c.GetRawData()` for fingerprinting; downstream `ShouldBindJSON` would otherwise see EOF. Re-feed:

```go
c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
```

The bytes are already in memory and bounded by the upstream MaxBytesReader; no second size check needed.

### Idempotency-Key validation: ASCII-printable + length cap

```go
func IsValidIdempotencyKey(key string) bool {
    if len(key) > 128 { return false }
    for i := 0; i < len(key); i++ {
        b := key[i]
        if b < 0x20 || b > 0x7E { return false }  // ASCII printable
    }
    return true
}
```

Reject:
- Length > 128 chars (Stripe documents 255; tighter bounds memory cost)
- Bytes outside ASCII printable (0x20–0x7E) — control characters fragment downstream log parsers and terminal output. UTF-8 multibyte sequences (any byte ≥ 0x80) are also rejected; this matches Stripe's spec

### Detached context for cache writes

Use `context.WithoutCancel(ctx)` for the post-processing Set:

```go
setCtx := context.WithoutCancel(ctx)
repo.Set(setCtx, key, result, fp)
```

The request context is cancelled the moment the response is fully flushed. Without detaching, a client disconnect races the cache write — replay protection becomes probabilistic.

## What NOT to do (anti-patterns)

| Anti-pattern | Why it fails |
| :-- | :-- |
| Cache 4xx responses | Client typo's body, gets cached 400 for 24h, can't fix it without generating a new key |
| Cache 5xx responses universally | Pins transient failures; clients retrying after recovery get stale errors |
| Use xxHash / non-cryptographic hash | Attacker controls body → can forge collisions |
| Canonicalize JSON before hashing | Adds CPU + ambiguity; no industry support; clients control marshaling |
| Return cached body on fingerprint mismatch | Client thinks their NEW request succeeded — silent lost-update |
| Return 422 instead of 409 | Diverges from Stripe; SDK ecosystem expects 409 |
| Write idempotency logic inline in handler | Cross-cutting concern; doesn't compose to second endpoint; 150-line handlers |
| Skip the body-size middleware before idempotency middleware | Unbounded body read → memory exhaustion vector |
| Forget `omitempty` on the fingerprint wire field | Pre-fingerprint cached entries unmarshal as `fingerprint:""` round-tripped — loses ability to distinguish "absent" from "explicitly empty" |
| Increment cache hit/miss counter on infra errors | Inflates miss rate during outages, drowning the real signal. Use a separate `cache_get_errors_total` counter |

## Pending: SETNX in-flight sentinel (race protection)

The Stripe-grade race protection that this skill does NOT cover (intentionally — separate PR scope):

```
Pod A: SETNX(K, "processing", TTL=30s) → SUCCESS, holds lock
Pod B: SETNX(K, "processing", TTL=30s) → FAIL → returns 409 "concurrent request"
Pod A: business logic runs → SET(K, response, TTL=24h) → overwrites sentinel
```

Without SETNX, two concurrent same-key requests can both miss the cache and both run the business logic. In `booking_monitor`, the DB UNIQUE constraint catches the resulting duplicate at the worker layer (no double-booking), but at the cost of one wasted Redis stream message + one DB rollback per race.

When to add SETNX:
- Business logic is slow (race window > ms)
- No backstop constraint at the persistence layer
- Compliance / audit requires deterministic 409 on concurrency rather than probabilistic correctness

## Multi-agent review pattern (companion technique)

For any non-trivial idempotency PR, run THREE reviewers in parallel:

1. **`code-reviewer`** — overall design + contract correctness
2. **`go-reviewer`** (or language equivalent) — idiomatic patterns, concurrency, performance
3. **`silent-failure-hunter`** — fail-open paths, swallowed errors, log-level semantics

Each reviewer gets the same self-contained brief. Bundle their findings into a single fixup commit; track which finding is from which reviewer in the commit message. After a substantive change (Q1/Q2 bundle, large refactor), run a SECOND-PASS review on the post-fixup state — first-round reviewers don't catch concerns introduced by their own fixes.

## Reference implementation

`booking_monitor` PR #48 (`feat/n4-idempotency-fingerprint`):

- `internal/infrastructure/api/middleware/idempotency.go` — full middleware
- `internal/domain/idempotency.go` — repository interface (`Get` returns side-channel fingerprint)
- `internal/infrastructure/cache/idempotency.go` — wire format
- `internal/infrastructure/observability/metrics.go` — `idempotency_replays_total{outcome}` + `idempotency_cache_get_errors_total`
- `docs/PROJECT_SPEC.md §5` — the contract table
- `docs/monitoring.md §2` — operator-facing metric guide
