# API Contracts & Idempotency Review

**Reviewer**: API Architecture Audit (Claude Opus 4.7, project audit 2026-05-27)
**Scope**: REST design, status codes, idempotency, webhooks, Stripe SOP alignment, error envelope, versioning
**Repo state**: `fix/grafana-datasource-uid` branch, post v1.0.0 (2026-05-10)
**Source-of-truth**: actual Go code read at audit time. CLAUDE.md was treated as a hypothesis to verify, not a citation.

---

## TL;DR

The system is **mostly aligned** with Stripe SOP and the wider 2025 webhook / idempotency literature — better than the typical Phase-3 prototype. Idempotency middleware is well-factored (SHA-256 body fingerprint, X-Idempotency-Replayed, lazy migration), webhook verification delegates to `stripe-go/v82`'s `webhook.ValidatePayloadWithTolerance` (constant-time, 5 min default skew, raw-bytes), and gateway-side idempotency is correctly threaded on `orderID` to Stripe's `Idempotency-Key`.

The defects that matter:

1. **CRIT — `payment_failed` webhook triggers immediate saga compensation**, contradicting both the reservation model (15-minute hold) and Stripe SOP (`requires_payment_method` is a soft state). Memory file flagged this; the gap is still present in `handleFailure` at `internal/application/payment/webhook_service.go:486`.
2. **MAJOR — `/api/v1/events/:id` is a stub that returns 200 + a misleading body**, increments `page_views_total`, and shows zero event data. Returning 200 here is dishonest; per HTTP semantics this should be 501 Not Implemented or actually load the event.
3. **MAJOR — `ENABLE_TEST_ENDPOINTS=true` is only rejected when `APP_ENV=production`**. Staging / unset env-name + live Stripe keys + test endpoints is a valid (but dangerous) combination — `POST /test/payment/confirm/:order_id` would let any caller settle real-money orders by URL guess.
4. **MAJOR — Error envelope is bespoke `{error: "..."}`**, not RFC 7807 / RFC 9457 `application/problem+json`. Two distinct webhook 500 reasons (`ErrWebhookUnknownIntent`, `ErrWebhookIntentMismatch`) collapse into indistinguishable bodies — no `type`, no `code`, no `trace_id`.

The rest of the surface (status codes, 202 shape, fingerprint validation, race handling, late-success handling) is **production-grade**. The webhook handler's late-success path in particular is unusually well thought out.

---

## 1. Resource modeling + path consistency

| Path | Verb | Resource? | Stripe-shape? |
| :-- | :-- | :-- | :-- |
| `POST /api/v1/book` | verb | NO — verb in path | Stripe: `POST /v1/payment_intents` (noun) |
| `POST /api/v1/events` | noun | YES | Aligned |
| `GET  /api/v1/events/:id` | noun | YES (but stub) | Aligned |
| `POST /api/v1/orders/:id/pay` | sub-resource verb | hybrid | Stripe: `POST /v1/payment_intents/:id/confirm` — same hybrid shape |
| `GET  /api/v1/orders/:id` | noun | YES | Aligned |
| `GET  /api/v1/history` | abstract noun | thin | Stripe: `GET /v1/charges` (noun-of-the-thing) |
| `POST /webhook/payment` | abstract noun | accepted | Aligned (root-mounted) |

**Findings:**

- `/book` is a verb. The strict-REST equivalents would be `POST /api/v1/orders` (creates an order) or `POST /api/v1/reservations` (creates a reservation). The current name is a residue from earlier phases — pre-PR #47 the response was `{"order_id": ..., "status": "processing"}` and the wire status word `reserved` (D3 Pattern A) makes the verb mismatch more obvious now ("you book and the response says you reserved"). Domain-wise the actual resource being created is a **reservation**, not a booking.
- `/history` is a thin noun for "orders the caller can see". Strict REST would say `GET /api/v1/orders?user_id=...` — but the current shape also has no auth (CLAUDE.md acknowledges "no auth today"), so "history" without ownership scoping is a placeholder anyway. Re-naming to `/orders` should be done at the same time as auth lands (N9).
- `POST /webhook/payment` at root (not under `/api/v1`) is correct: webhook URLs are dashboard-controlled at the provider side, and versioning lives in the envelope's `type` discriminator. Stripe / Shopify dashboards both publish root-mounted webhook URLs. The route comment at [`routes.go:5`](../../internal/infrastructure/api/webhook/routes.go) documents this.

**Verdict**: `/book` is the only material inconsistency. Worth folding into a future `/api/v2` rename rather than rushing.

---

## 2. HTTP status code audit

| Endpoint | Success | Error code mappings | Issues |
| :-- | :-- | :-- | :-- |
| `POST /book` | 202 Accepted | 400 invalid body, 409 sold-out / user-already-bought, 413 oversize, 503 ctx-cancelled, 504 ctx-deadline | No `Location` header (uses `links.self` JSON); no `Retry-After` hint for the documented 404-on-fresh-poll case |
| `GET  /orders/:id` | 200 | 400 invalid uuid, 404 not-found (incl. async window), 503/504 | No `Retry-After` on the post-202 404 path (clients told to "retry with backoff" with no machine hint) |
| `POST /orders/:id/pay` | 200 | 400 invalid uuid, 404 missing, 409 wrong-status / expired / missing-snapshot / state-transition, 500 transient | 409 returns a free-text `error` field — client must string-match to disambiguate Paid vs Expired vs Snapshot-missing |
| `GET  /history` | 200 | 400 invalid params, 500 default | Defensive page-size cap (100) shown in code; OK |
| `POST /events` | 201 Created | 400 invariant violations, 409 name-taken | No `Location` header to the created event; clients must construct it manually |
| `GET  /events/:id` | 200 (stub) | — | Returns `{"message": "View event", "event_id": "..."}`. NOT loading the event. See §10 |
| `POST /webhook/payment` | 200 `{received: true}` | 400 body parse / read failed, 401 signature invalid / missing / malformed / skew / mismatch, 409 unexpected status, 413 oversize, 500 unknown-intent / intent-mismatch / DB-transient / config-error | 500 has multiple distinct causes — no `code` field in the response (intent-mismatch is a security incident that should be paged loudly; the response body alone doesn't distinguish it from a transient DB failure) |
| `GET  /metrics` | 200 | n/a (Prometheus) | nginx denies all (correct) |
| `GET  /livez` | 200 always | n/a | k8s pattern correct |
| `GET  /readyz` | 200 or 503 | per-dep JSON | k8s pattern correct |

### Specific question audit

**202 Accepted shape on `/book` (verified at `handler.go:204`):**

```json
{
  "order_id": "...",
  "status": "reserved",
  "message": "...",
  "reserved_until": "...",
  "expires_in_seconds": 900,
  "links": { "self": "/api/v1/orders/<uuid>", "pay": "/api/v1/orders/<uuid>/pay" }
}
```

- **`Location` header**: NOT set. RFC 7231 §6.3.3 says "If the request was processed... a representation may be sent with a status monitor or pointer to it." `Location` is the IETF-canonical pointer; `links.self` is a HAL/JSON:API convention. Either is acceptable, **but emitting BOTH is best practice** — generic HTTP tooling (curl --location-trusted) reads the header, JSON-aware clients read the body.
- **`Retry-After`**: NOT set on the 404 path of `GET /orders/:id` either. MDN documents `Retry-After` as valid on 503/429/3xx, but [Microsoft Q&A confirms](https://learn.microsoft.com/en-us/answers/questions/1336899/how-should-we-handle-retry-after-in-response-heade) it is also valid on 200/202 for "this is the recommended poll interval" semantics. **Recommend**: emit `Retry-After: 1` on the 404 inside the async-processing window so well-behaved clients can avoid retry-storming.

**409 Conflict on `/pay`**: 4 distinct sentinel causes (`ErrOrderNotAwaitingPayment`, `ErrReservationExpired`, `ErrInvalidTransition`, `ErrOrderMissingPriceSnapshot`) collapse onto 409 with **distinct public messages** ([errors.go:97-121](../../internal/infrastructure/api/booking/errors.go)). The string is human-readable; no `code` field for machine consumption. Stripe's API returns `{"error": {"type": "...", "code": "...", "message": "..."}}` — well worth aligning to (see §9).

**404 on `/orders/:id` "for ~ms after 202"**: Honestly documented at [`handler.go:217`](../../internal/infrastructure/api/booking/handler.go) and in CLAUDE.md. No `Retry-After` hint as noted above.

**400 vs 422 vs 409 discipline**:
- 400 = malformed body / invalid path UUID / unparseable signature header
- 409 = business-state conflict (sold-out, already-bought, wrong-status, expired, name-taken)
- 422 = NOT USED anywhere in the codebase (`grep -r "422\|StatusUnprocessableEntity"` returns 0 hits)
- 413 = oversize body
- 401 = signature invalid (webhook only)
- 500 = anything unmapped, plus `webhook config_error`

The 422 absence is OK — the codebase consistently uses 400 for "I cannot parse your request" and 409 for "your request was understood but the system state rejects it", which is a defensible split. Some practitioners reserve 422 specifically for "syntactically valid JSON but semantically invalid fields"; the project's binding-tag validation catches those at 400. Either convention is acceptable — but a webhook 401 + payment 409 + booking 400 + saga 500 catalog should be **documented somewhere** beyond CLAUDE.md prose.

---

## 3. Idempotency-Key full audit

### 3.1 Storage + race window

[`internal/infrastructure/api/middleware/idempotency.go`](../../internal/infrastructure/api/middleware/idempotency.go) is the canonical implementation. Flow at lines 178-318:

1. Read header → validate length+ASCII → 400 if invalid
2. Read body once via `c.GetRawData()` → re-feed via `bytes.NewReader`
3. Compute `Fingerprint(bodyBytes) = hex(sha256.Sum256(...))`
4. `repo.Get(ctx, key)` from Redis (via [`cache/idempotency.go`](../../internal/infrastructure/cache/idempotency.go))
5. Three-way branch:
   - **hit + matching fp** → replay cached body + `X-Idempotency-Replayed: true` + `c.Abort()`
   - **hit + different fp** → 409 with structured error
   - **hit + empty fp (legacy)** → replay + lazy `Set` of fresh fingerprint
   - **miss** → call handler, capture response, conditionally write back

**Storage write**: Redis-only via `SET idempotency:<key> <json> EX <ttl>` ([cache/idempotency.go:103](../../internal/infrastructure/cache/idempotency.go)). NOT a SETNX — the Set is unconditional after the handler runs.

**Race window — CONFIRMED**: Two parallel requests with the same key both pass the Get (cache miss) before either Set lands. Both handlers run end-to-end (Redis Lua deducts inventory twice from the user's perspective, DB row-create races, etc.), and the slower one's Set OVERWRITES the faster one's. Net effect:
- Inventory deduction: protected by the Lua script (atomic on Redis) — so over-decrement is impossible.
- Order rows: the worker DB layer has a UNIQUE constraint on `(idempotency_key, user_id)` (per CLAUDE.md "worker (DB unique constraint)") — so the DB insert race is handled there.
- **Idempotency cache**: actually exposed to a tiny "the slower handler's response body wins" race window.

This is acceptable in practice because (a) both responses for an idempotent operation should be equivalent, and (b) Stripe's actual semantics also race-permit (their docs say "if you submit two requests in parallel... only one is processed"). But it's worth a code comment acknowledging the race. **Recommendation**: switch the initial cache-write to `SET ... NX` (atomic) so the first response wins deterministically; subsequent retries see a Set conflict and skip the write.

### 3.2 Fingerprint comparison

- **Hash**: SHA-256 hex via `crypto/sha256` ([idempotency.go:96](../../internal/infrastructure/api/middleware/idempotency.go)). Aligned with industry pattern.
- **Canonicalization**: NONE. Raw body bytes hashed verbatim. The code comment at lines 87-95 explicitly justifies this against the Stripe / Shopify / GitHub / AWS Powertools "client must send byte-identical retries" convention.
- **Implication**: a client that pretty-prints JSON differently on a retry (whitespace, key reordering) produces a different fingerprint and gets a 409. This is the documented Stripe behavior — see [the Medium "Uncomfortably Deep Dive"](https://sameerahmed56.medium.com/an-uncomfortably-deep-dive-into-the-idempotency-key-67626c8d3f3d) discussing the pitfall: "if a user adds an insignificant optional field to a retry... these produce different hashes". Project explicitly accepts this tradeoff.

**Verdict**: Defensible. SHA-256 hex is the SOP. The fingerprint-mismatch 409 with `X-Idempotency-Replayed` for matches is exactly Stripe's contract.

### 3.3 TTL semantics

- Default: `REDIS_IDEMPOTENCY_TTL=24h` ([config.go reads through `cfg.Redis.IdempotencyTTL`](../../internal/infrastructure/cache/idempotency.go)). **Aligned with Stripe's 24h documented retention.**
- **Post-expiry semantics** = **the request is processed as if fresh** (no replay, no 409). A client that retries with the same key 25h later will get a completely new order. This matches Stripe.

**Verdict**: Correct, aligned with Stripe SOP, documented in CLAUDE.md.

### 3.4 Lazy migration

`idempotencyRecord` ([cache/idempotency.go:52](../../internal/infrastructure/cache/idempotency.go)) has `Fingerprint string json:"fingerprint,omitempty"`. Pre-N4 entries lack the field; `json.Unmarshal` populates it as `""`. The middleware's three-way switch at [idempotency.go:222](../../internal/infrastructure/api/middleware/idempotency.go) treats `cachedFP == ""` as the legacy signal → replay + lazy write-back. Counter `IdempotencyReplaysTotal{result="legacy_match"}` makes the migration progress observable.

**Verdict**: Clean. The on-write-back failure path correctly logs Warn and lets the next replay retry the migration — no silent inconsistency.

---

## 4. Webhook security audit

[`internal/infrastructure/api/webhook/verifier.go`](../../internal/infrastructure/api/webhook/verifier.go) + [handler.go](../../internal/infrastructure/api/webhook/handler.go).

### Signature verification

- **Algorithm**: `stripewebhook.ValidatePayloadWithTolerance` from `github.com/stripe/stripe-go/v82/webhook` — the official Stripe SDK's verifier. Stripe explicitly recommends using the SDK ([docs.stripe.com/webhooks](https://docs.stripe.com/webhooks?verify=verify-manually)): "never hand-roll HMAC verification. The official SDKs handle constant-time comparison, timestamp tolerance, and header parsing correctly."
- **Timestamp + body**: yes — Stripe-Signature header contains `t=<unix>,v1=<hex>` where the HMAC is over `t.body`. Tampering with `t` invalidates the signature.
- **Skew tolerance**: configurable via `tolerance time.Duration`, defaults to `stripewebhook.DefaultTolerance = 5 min` per Stripe docs. Wired through [`webhook/module.go`](../../internal/infrastructure/api/webhook/module.go) (5 min in production).
- **Constant-time comparison**: confirmed — `stripe-go/v82/webhook` uses `hmac.Equal`. Comment at [verifier.go:135](../../internal/infrastructure/api/webhook/verifier.go) cites this.
- **Future-skew**: explicitly accepted per Stripe SDK behavior (line 57). Acceptable trade — a signature with future `t` still requires the secret to verify.
- **Raw body**: read via `io.ReadAll(io.LimitReader(c.Request.Body, MaxBodyBytes+1))` BEFORE `json.Unmarshal`. Marshal round-trip is correctly avoided.
- **Body cap**: 64 KiB ([handler.go:28](../../internal/infrastructure/api/webhook/handler.go)). Catches slow-loris-by-body. The `+1` trick to detect truncation is well-engineered ([handler.go:111](../../internal/infrastructure/api/webhook/handler.go)).
- **Empty-secret pre-flight**: returns `ErrConfigError` → 500 (not 401) so operators page on misconfig rather than treating it as a 401 flood.

**Verdict**: BEST-IN-CLASS. This is a textbook implementation. No findings.

### Idempotency on the webhook

[`webhook_service.go:HandleWebhook`](../../internal/application/payment/webhook_service.go) dispatches by `env.Type` and order status. Idempotency mechanisms:

1. **Cross-env guard** (line 171): livemode mismatch → 200-no-op (test event hitting prod listener).
2. **Terminal-status redelivery** (line 238): if the order is already in `paid / payment_failed / compensated / expired`, return 200 with `Duplicate` metric. EXCEPT for the `succeeded`-after-`expired` / `compensated` / `payment_failed` case, which routes to `handleLateSuccess` (paper trail for manual refund — see §6).
3. **Intent-ID consistency check** (line 207): metadata-resolved order whose persisted `payment_intent_id` disagrees with the webhook's → `ErrWebhookIntentMismatch` 500 + page-worthy alert.

**Note**: the webhook does NOT dedupe on `env.ID` (Stripe's event UUID like `evt_xxx`). Two redeliveries of the same Stripe event would both be processed — Stripe's SDK doesn't enforce this; you're expected to use status checks (which the code does, via `isTerminalForWebhook`). This is **correct** but worth a code comment — a future engineer might add an event-id cache as a "belt and suspenders" without realizing the status check is the primary mechanism.

### Sync vs async

The handler is **synchronous**: signature verify → JSON unmarshal → service dispatch → return 200 OR 5xx. State changes commit inside a UoW (`SetPaymentIntentID + MarkPaid` in one tx for the orphan-repair case). If anything fails, the provider retries.

**Tradeoff actually made**: synchronous = strong consistency (the 200 means "we've durably processed this"), at the cost of webhook handler latency dominated by DB write latency (~10ms typical). Stripe's webhook retry policy (up to 3 days, exponential backoff) absorbs transient failures. For a flash-sale system this is the right call — async-queue-then-process would let a queue backlog hide a saga-compensation lag.

**Verdict**: Correct tradeoff. No findings on the sync vs async axis.

---

## 5. Payment retry design gap (memory:payment_retry_design_gap)

**Memory file claim verified**: still present.

[`webhook_service.go:486 (handleFailure)`](../../internal/application/payment/webhook_service.go):

```go
func (s *webhookService) handleFailure(ctx context.Context, order domain.Order, reason string) error {
    err := s.uow.Do(ctx, func(repos *application.Repositories) error {
        if e := repos.Order.MarkPaymentFailed(ctx, order.ID()); e != nil { return e }
        failedEvent := application.NewOrderFailedEventFromOrder(order, "payment_failed: "+reason)
        // ... emit order.failed → saga compensator reverts inventory IMMEDIATELY
```

This unconditionally transitions to `payment_failed` AND emits the saga event on the FIRST `payment_intent.payment_failed` webhook. The user's reservation window (default 15 min) is ignored.

**Why it's wrong**:

- **Stripe SOP**: `requires_payment_method` is a SOFT state — [Stripe docs](https://docs.stripe.com/payments/payment-intents/verifying-status): "If the payment attempt fails (for example due to a decline), the PaymentIntent's status returns to `requires_payment_method`." The PaymentIntent remains valid; the client can call `paymentIntent.confirm()` again with a different card.
- **Hard vs soft declines**: [Stripe docs on declines](https://docs.stripe.com/declines/card) explicitly distinguish them. Soft declines (insufficient_funds, processing_error, 3DS abandon) are retryable; hard declines (fraudulent, lost_card, pickup_card) are not. The code at `handleFailure` makes no distinction — `obj.LastPaymentError.Code` is captured in the reason string but not used to branch.
- **Reservation semantics**: a 15-minute hold exists precisely so the user can retry payment within that window. Saga compensation on first failure means the user gets one shot; that contradicts the product's own promise.

**Recommended fix**:

```go
func (s *webhookService) handleFailure(ctx context.Context, order domain.Order, reason string, declineCode string) error {
    // 1. Reservation still valid AND decline is soft → keep awaiting_payment
    //    just emit metric + log. Client can call /pay again to confirm
    //    a new payment_method against the SAME PaymentIntent.
    if order.ReservedUntil().After(s.now()) && isSoftDecline(declineCode) {
        s.metrics.SoftDecline(declineCode)
        s.log.Info(ctx, "webhook: soft payment failure within reservation window; keeping awaiting_payment for retry",
            tag.OrderID(order.ID()), mlog.String("decline_code", declineCode))
        return nil
    }
    // 2. Hard decline OR reservation expired → existing saga path
    return s.uow.Do(ctx, func(repos *application.Repositories) error { /* MarkPaymentFailed + saga emit */ })
}
```

Where `isSoftDecline` consults Stripe's documented soft/hard table (insufficient_funds, generic_decline, do_not_honor, processing_error → soft; fraudulent, lost_card, stolen_card, pickup_card → hard). Per [Stripe docs](https://docs.stripe.com/declines/card), the decline_code is at `last_payment_error.decline_code`.

**Risk profile**: this is **product-correctness CRIT**, not security CRIT. The current behavior is operationally observable (`payment_webhook_received_total{result="failed"}` would alert on a flood; the saga compensator's `compensation_total` would echo). But customer experience: pay-with-declined-card → reservation cancelled → fix card → seat gone. KKTIX, BookMyShow, Ticketmaster all keep the hold during retry.

---

## 6. Gateway idempotency on `/pay`

[`internal/infrastructure/payment/stripe_gateway.go:287`](../../internal/infrastructure/payment/stripe_gateway.go):

```go
params.SetIdempotencyKey(orderID.String())
```

**Confirmed**: every `CreatePaymentIntent` call uses `orderID.String()` as the Stripe `Idempotency-Key` header. Stripe's 24h server-side cache returns the SAME PaymentIntent (same `id`, same `client_secret`) for repeat calls with the same orderID. Aligns with Stripe's [`Idempotent requests`](https://docs.stripe.com/api/idempotent_requests) spec.

**Verification chain**:
- Domain port contract: `PaymentIntentCreator` doc-comment at [payment.go:196-208](../../internal/domain/payment.go) explicitly mandates this.
- Mock gateway honors it (in [mock_gateway.go](../../internal/infrastructure/payment/mock_gateway.go) via in-memory cache).
- Stripe gateway honors it via `params.SetIdempotencyKey(orderID.String())`.
- Application service [`service.go:91-217`](../../internal/application/payment/service.go) calls `g.gateway.CreatePaymentIntent` on every `/pay`, no application-side cache.

**Double-charge risk**: zero. Repeat `/pay` calls with the same orderID always retrieve the same intent. The client confirms the intent via `client_secret`; the webhook is the single source of truth for "money moved".

**Edge case audited**: the documented `SetPaymentIntentID` race at [`service.go:166`](../../internal/application/payment/service.go) — gateway succeeds, DB persist fails. The next `/pay` retry returns the SAME intent (Stripe cache); even if persist fails again, `metadata.order_id` is set on the gateway side, so the eventual webhook resolves via the `metadata` primary lookup. Orphan rescue is structurally guaranteed.

**Verdict**: CORRECT. Possibly the best-implemented piece in the entire payment surface.

---

## 7. Price snapshot integrity audit

CLAUDE.md claim: D4.1 reads price from the order snapshot, not from the ticket_type. Verified at [`service.go:144-145`](../../internal/application/payment/service.go):

```go
amount := order.AmountCents()
currency := order.Currency()
```

**Audit results**:

- **`/pay` reads ONLY from `order` accessors**: confirmed. No `ticketTypeRepo.GetByID` call. No price lookup. The amount + currency are the price snapshot frozen at `domain.NewReservation` time. TOCTOU pricing bug = impossible at this layer.
- **`HasPriceSnapshot()` guard**: invoked at [service.go:136](../../internal/application/payment/service.go). Surfaces `ErrOrderMissingPriceSnapshot` → mapped to 409 Conflict with distinct public message + Error-level log (data-integrity defect, pages on-call). Confirmed at [errors.go:120 + 141](../../internal/infrastructure/api/booking/errors.go) and the `isExpectedPayError` exclusion at line 151.
- **Webhook handler**: `handleSuccess` does NOT lookup price either — just calls `MarkPaid(orderID)`. The price on the gateway side was set at intent-create time and matches our snapshot.
- **Test endpoint** `/test/payment/confirm/:order_id`: reads `order.AmountCents()` / `order.Currency()` ([payment_confirm.go:131](../../internal/infrastructure/api/testapi/payment_confirm.go)) — also goes via the snapshot, not fresh lookup. Confirmed.

**Verdict**: CORRECT. No fresh-price lookups anywhere in the payment surface. The freeze at book-time is honored.

---

## 8. Versioning strategy

- **Current**: `/api/v1/*` prefix for all customer-facing endpoints; webhook + ops endpoints at root.
- **Constant**: `orderSelfLinkPrefix = "/api/v1/orders/"` in [handler.go:44](../../internal/infrastructure/api/booking/handler.go). Comment notes: "bumping `/api/v1` to `/api/v2` only needs this prefix changed."
- **`apiV1Prefix` constant** in [server.go:357](../../cmd/booking-cli/server.go).
- **No `Sunset` header**, no version-skew matrix, no documented `v2` plan.
- **Single literal in CLAUDE.md** describes the `v1` surface.

**Findings**:

- v1 is **effectively forever** — there's no deprecation contract in code or docs.
- Pure-additive changes (new optional fields on responses; new endpoints) don't need a v2. Breaking changes (renaming `event_id` → `ticket_type_id`, which was D4.1) historically went into v1 directly. The D4.1 change kept backwards-compat by adding `omitempty` on the new fields rather than rev'ing the version — defensible for a flash-sale prototype, suspect for a production e-commerce API.
- The `BookingStatus` enum at [response.go:131-145](../../internal/infrastructure/api/dto/response.go) keeps `processing` (legacy A4) alongside `reserved` (D3 Pattern A) for backward compat — good discipline.

**Recommendation**: write a 1-page "versioning policy" doc declaring (a) additive vs breaking change rules, (b) v1 has no end-of-life today, (c) when v2 lands, expect a 6-month overlap with `Sunset: <RFC1123 date>` headers on v1 responses. Today this is missing.

---

## 9. Error response envelope audit

Current shape across the API: `{"error": "<public message>"}` via `dto.ErrorResponse`.

[`dto/response.go:204-209`](../../internal/infrastructure/api/dto/response.go):

```go
type ErrorResponse struct {
    Error string `json:"error"`
}
```

Webhook handler uses `gin.H{"error": "..."}` literals ([handler.go:115, 122, 137, 140, 148, 157](../../internal/infrastructure/api/webhook/handler.go)) — same shape, ad-hoc.

**vs RFC 7807 / RFC 9457 `application/problem+json`**: the modern standard ([RFC 9457](https://datatracker.ietf.org/doc/html/rfc9457) is the 2023 update of RFC 7807) specifies:

```json
{
  "type": "https://example.com/probs/sold-out",
  "title": "Sold out",
  "status": 409,
  "detail": "All 100 tickets for this ticket_type have been reserved",
  "instance": "/api/v1/book",
  "trace_id": "..."
}
```

with `Content-Type: application/problem+json`.

**Findings**:

- **No machine-readable error code** anywhere. Client distinguishing "sold out" vs "user already bought" vs "ticket_type not found" requires string-matching the public message ("sold out", "user already bought ticket", "resource not found"). Three of these collapse to 409, four collapse to 404, etc. — string matching is fragile.
- **No `trace_id` / correlation-id** in error bodies. `X-Correlation-ID` IS set as a response header by the `middleware.Combined` middleware, but it's not in the JSON body. Support tickets without a body-side correlation-id are harder to triage (operators have to ask for headers, which clients may not show).
- **No `type` URI** pointing to docs.
- **Webhook errors use a separate ad-hoc `gin.H` shape**: two distinct 500 reasons (`unknown intent` vs `intent mismatch`) collapse to indistinguishable bodies ("unknown intent — please retry" vs "intent mismatch — please retry"). The HTTP code is the same; only the body string differs.

**Verdict**: **MAJOR**. Move to RFC 9457 `problem+json` envelope with a stable `code` field for machine consumption. Don't leak internals (no SQL errors, no stack traces — already correctly suppressed via the default branch of `mapError`).

Recommended envelope:

```json
{
  "type": "https://booking.example.com/errors/sold-out",
  "title": "Sold out",
  "status": 409,
  "code": "TICKETS_SOLD_OUT",
  "detail": "All tickets for ticket_type_id=... have been reserved",
  "instance": "/api/v1/book",
  "correlation_id": "01HXXXXXXXXX"
}
```

This is a non-breaking change for 4xx/5xx clients that only read `status` + the body (the `error` field can stay alongside the new fields for one release cycle, then go away).

---

## 10. Stub endpoint audit

[`handler.go:290-294`](../../internal/infrastructure/api/booking/handler.go):

```go
func (h *bookingHandler) HandleViewEvent(c *gin.Context) {
    observability.PageViewsTotal.WithLabelValues("event_detail").Inc()
    c.JSON(http.StatusOK, gin.H{"message": "View event", "event_id": c.Param("id")})
}
```

**Issues**:

1. **Returns 200 OK** with no validation that the event exists. `GET /api/v1/events/00000000-0000-0000-0000-000000000000` returns the same 200 + the literal nil UUID echo. Misleading.
2. **Increments `page_views_total{type="event_detail"}`** regardless of whether the event exists. The conversion-rate metric is therefore biased — random URL guesses inflate the denominator.
3. **No 404 path**: an invalid UUID still returns 200 with the raw string echoed in `event_id` ("01900000-not-a-uuid" round-trips). Echoing arbitrary client input back is also a low-grade reflection sink (negligible for JSON, but bad pattern).

**Comparable Stripe**: `GET /v1/payment_intents/<bad>` returns 404 with `{"error": {"type": "invalid_request_error", "message": "No such payment_intent: ...", "param": "intent"}}`. Real-world REST APIs don't 200 on stubs.

**Recommendation**: pick one:

- **Defer-and-be-honest** (cheap): return `501 Not Implemented` with a `Retry-After: 0` and a `{"code": "NOT_IMPLEMENTED", "title": "GET /events/:id is planned but not yet implemented"}`. Keep the page-view metric outside (or label it `stub_endpoint=true` so it's filterable out of the conversion ratio).
- **Implement** (D4.1-finishing scope): wire `event.Service.GetEvent(eventID)` → load event + ticket_types → return the existing `EventResponse` DTO. The mapper at `EventResponseFromDomain` already exists ([dto/mapper.go](../../internal/infrastructure/api/dto/mapper.go)). 30-min PR.

Returning 200 + a misleading body is the worst of all options — it pretends success and pollutes the metric.

---

## 11. CORS / CSRF / rate-limit audit

### CORS

[`middleware/cors.go`](../../internal/infrastructure/api/middleware/cors.go):

- Exact-match allow-list (no wildcard, no regex).
- Empty allow-list = middleware no-op (production-safe default).
- Emits `Vary: Origin` on EVERY response (correct; prevents intermediate proxy cache poisoning).
- Preflight handling: `OPTIONS` matched origins get `Access-Control-Allow-Methods: GET, POST, OPTIONS` + 600s max-age.
- Allow-headers: `Content-Type, Idempotency-Key, X-Correlation-ID`.
- Mounted BEFORE `Combined` middleware so OPTIONS preflights short-circuit without correlation-id allocation.

**Verdict**: CORRECT, well-engineered.

### CSRF

- **None**. No CSRF token, no `SameSite` cookie strategy. The API doesn't issue cookies (no auth today), so there's nothing to CSRF on the customer side.
- **`/webhook/payment`**: correctly does NOT need CSRF protection. Authentication is HMAC signature; Stripe doesn't send a CSRF token. Mounted at root, outside `/api/v1` rate-limit zone, by design.

**Verdict**: Acceptable for current state. When auth lands (N9 — JWT or session), CSRF protection needs to be added in lockstep for any cookie-based session. JWT-in-Authorization-header is CSRF-immune by construction.

### Rate limiting

- **Single zone** at nginx layer ([deploy/nginx/nginx.conf:142-181](../../deploy/nginx/nginx.conf)): `limit_req zone=booking burst=200 nodelay`, 100 r/s, 429 status.
- **Per-route**: NO. Every `/api/v1/*` path shares the same zone. `/book` (write) and `/orders/:id` (read) compete for the same bucket.
- **`/webhook/payment`**: explicitly EXCLUDED from rate-limit (mounted at root, not `/api/`). Correct — provider IPs are unpredictable and signature-authenticated.
- **`/metrics`**: nginx `deny all` — not exposed publicly. Correct.

**Findings**:

- **MINOR — Per-route rate-limit is missing**. A booking storm (`POST /book` flood) consumes the same bucket as `GET /history` polling. Per [Gin rate-limit patterns 2024-2025](https://www.linkedin.com/pulse/owasp-top-10-api-security-risks-2023-broken-ravendra-kumar-singh-lhvbf), production APIs typically split:
  - `POST /book` — strict (e.g., 10 r/s/IP — flash-sale fairness)
  - `POST /events` — admin-rate (1 r/s)
  - `GET /orders/:id` — looser (100 r/s — polling is expected)
- Implementing per-route would either need (a) multiple nginx zones or (b) a Go-side middleware (e.g., `ulule/limiter` or `tollbooth`). The CLAUDE.md note about "N9 will add per-route rate limit" suggests this is on the roadmap — verify by checking the post-Phase-2 roadmap.

### Admin endpoints

- `/admin/events/stream` (SSE): JWT-gated via `adminJWT.Func()` at [server.go:372](../../cmd/booking-cli/server.go). Correct.
- `/admin/loglevel` + `/debug/pprof/*`: bound to `127.0.0.1:6060` by default ([server.go pprofAddr](../../cmd/booking-cli/server.go)). Operator-only.

**Verdict on admin gating**: Correct.

---

## 12. Test endpoint safety audit

[`internal/infrastructure/api/testapi/`](../../internal/infrastructure/api/testapi/) — `POST /test/payment/confirm/:order_id`.

### Current guards

[`config.go:802-806`](../../internal/infrastructure/config/config.go):

```go
if c.Server.EnableTestEndpoints {
    missing = append(missing, "server.enable_test_endpoints / ENABLE_TEST_ENDPOINTS (forbidden when APP_ENV=production)")
}
```

This rejects `ENABLE_TEST_ENDPOINTS=true` IFF `APP_ENV=production`. Plus:

- The route is only mounted when `cfg.Server.EnableTestEndpoints == true` ([server.go:353](../../cmd/booking-cli/server.go)). When false, GET/POST `/test/*` returns 404.
- Test handler `NewHandler` returns error if `PAYMENT_WEBHOOK_SECRET` is empty — startup fails.

### Gap

**There is NO cross-field check between `ENABLE_TEST_ENDPOINTS` and `STRIPE_API_KEY` prefix.**

Concrete scenarios that pass `Validate()` today:

| `APP_ENV` | `ENABLE_TEST_ENDPOINTS` | `STRIPE_API_KEY` | Result |
| :-- | :-- | :-- | :-- |
| `staging` | `true` | `sk_live_xxx` | **BOOTS** — test endpoint can settle real-money orders |
| `staging` | `true` | `rk_live_xxx` | **BOOTS** — same problem with a restricted key |
| `dev` | `true` | `sk_live_xxx` | **BOOTS** — same |
| `""` (unset) | `true` | `sk_live_xxx` | **BOOTS** — `normalizedAppEnv("")` is not `"production"` |
| `production` | `true` | any | rejected (good) |

The fall-through is real because [config.go:791](../../internal/infrastructure/config/config.go) reads `normalizedAppEnv(c.App.Env)` and only the literal `"production"` triggers the strict block. Any other value (staging, dev, unset, typo'd "PRODUCTION" which `normalizedAppEnv` may not handle — verify) bypasses the guard.

### Recommended startup guard

```go
// In Config.Validate(), unconditional (no APP_ENV gate):
if c.Server.EnableTestEndpoints && c.Payment.Provider == "stripe" {
    isLiveKey := strings.HasPrefix(c.Payment.Stripe.APIKey, "sk_live_") ||
                  strings.HasPrefix(c.Payment.Stripe.APIKey, "rk_live_")
    if isLiveKey {
        missing = append(missing,
            "server.enable_test_endpoints + payment.stripe.api_key (live key starts with sk_live_/rk_live_) — refusing to boot: /test/payment/confirm would settle real-money orders. Either set STRIPE_API_KEY to sk_test_/rk_test_, or set ENABLE_TEST_ENDPOINTS=false")
    }
}
```

Also worth a paired check:

```go
if c.Server.EnableTestEndpoints && c.Payment.Provider == "stripe" {
    // Even with test keys, document the risk
    log.Warn("/test/* endpoints are enabled — never expose this listener to the public internet")
}
```

**Severity**: MAJOR. The current default (`ENABLE_TEST_ENDPOINTS=false`) and the docker-compose / CI defaults both have this off, so the production-typical case is safe. But a single misconfigured staging environment with real Stripe credentials is enough to materialize the risk.

---

## Findings (severity: CRIT / MAJOR / MINOR / NIT)

### [CRIT-1] `payment_failed` webhook reverts inventory on the first soft decline

**Where**: [`internal/application/payment/webhook_service.go:486 (handleFailure)`](../../internal/application/payment/webhook_service.go)
**What**: Any `payment_intent.payment_failed` webhook unconditionally transitions order to `payment_failed` AND emits the saga compensation event. The user's 15-minute reservation window is ignored. Soft declines (insufficient_funds, processing_error, 3DS abandon) — which Stripe documents as retryable, and which competitors (KKTIX, Ticketmaster, BookMyShow) all honor with a retry-during-hold — cause the seat to be lost.
**Recommendation**: Branch on `obj.LastPaymentError.DeclineCode`. For soft declines while `reserved_until > now()`, keep `awaiting_payment` and emit a `SoftDecline` metric. For hard declines OR expired reservations, run the existing saga path.
**References**:
- [Stripe Verifying Status](https://docs.stripe.com/payments/payment-intents/verifying-status) — `requires_payment_method` is a soft state.
- [Stripe Card Declines](https://docs.stripe.com/declines/card) — soft vs hard decline taxonomy.
- Memory file `payment_retry_design_gap.md` (12 days old, **gap verified still present**).

### [MAJOR-1] `ENABLE_TEST_ENDPOINTS=true` is only rejected when `APP_ENV=production`

**Where**: [`internal/infrastructure/config/config.go:802-806`](../../internal/infrastructure/config/config.go)
**What**: Staging or unset APP_ENV + `ENABLE_TEST_ENDPOINTS=true` + `STRIPE_API_KEY=sk_live_*` is a valid combo at startup. `POST /test/payment/confirm/:order_id` signs an envelope with the production webhook secret and submits it to the production webhook endpoint, marking real-money orders as paid by URL guess.
**Recommendation**: Add an unconditional cross-field check rejecting `EnableTestEndpoints=true` + live-key prefix (`sk_live_*` / `rk_live_*`), regardless of `APP_ENV`. See §12 for the snippet.
**References**:
- [OWASP API Security Top 10 2023 — API8 Security Misconfiguration](https://owasp.org/API-Security/editions/2023/en/0xa8-security-misconfiguration/).

### [MAJOR-2] `GET /api/v1/events/:id` returns 200 with a misleading body

**Where**: [`internal/infrastructure/api/booking/handler.go:290-294`](../../internal/infrastructure/api/booking/handler.go)
**What**: Returns `{"message": "View event", "event_id": "<echo>"}` for any path param, valid or not. Increments `page_views_total{type="event_detail"}` unconditionally — biases the conversion-rate metric. CLAUDE.md acknowledges this as a known gap but the gap is shipped as 200 OK.
**Recommendation**: Either implement (the mapper + service exist; ~30-min PR) OR return 501 Not Implemented with a `problem+json` body. 200 with empty data is the worst option.
**References**: [RFC 9110 §15.6.2 (501 Not Implemented)](https://www.rfc-editor.org/rfc/rfc9110.html#name-501-not-implemented).

### [MAJOR-3] Error envelope is bespoke `{error: "..."}`, not RFC 9457 problem+json

**Where**: [`internal/infrastructure/api/dto/response.go:204-209`](../../internal/infrastructure/api/dto/response.go) + webhook ad-hoc `gin.H{"error": ...}` literals.
**What**: No machine-readable `code` field, no `trace_id` in body (only as response header), no `type` URI for docs link-out. Two distinct webhook 500 reasons (`unknown intent` vs `intent mismatch`) collapse to near-identical bodies — operators can't quickly distinguish "transient race" from "security incident".
**Recommendation**: Migrate to RFC 9457 `application/problem+json` envelope. Add a stable `code` enum (e.g., `TICKETS_SOLD_OUT`, `ORDER_NOT_AWAITING_PAYMENT`, `WEBHOOK_INTENT_MISMATCH`). Keep `error` for one release cycle for backward compat.
**References**:
- [RFC 9457 — Problem Details for HTTP APIs](https://datatracker.ietf.org/doc/html/rfc9457).
- [oneuptime — How to Build API Problem Details](https://oneuptime.com/blog/post/2026-01-30-api-problem-details/view).

### [MAJOR-4] Idempotency cache write is unconditional Set, not SETNX

**Where**: [`internal/infrastructure/cache/idempotency.go:103`](../../internal/infrastructure/cache/idempotency.go) + the middleware's post-handler write at [`api/middleware/idempotency.go:307`](../../internal/infrastructure/api/middleware/idempotency.go).
**What**: Two parallel requests with the same Idempotency-Key both pass the GET (cache miss), both run handlers end-to-end, both write Set. The later write overwrites the earlier. Inventory is protected by the Redis Lua script (atomic) and the DB UNIQUE constraint on `(idempotency_key, user_id)` at the worker, so the actual race is bounded to "slow handler's body wins" — but a deterministic-first-write would be cleaner.
**Recommendation**: Use `SET ... NX EX <ttl>` on the initial write. On NX conflict, log Warn and skip — the first writer wins, retries will see the cached response.
**References**:
- [Stripe Idempotent Requests](https://docs.stripe.com/api/idempotent_requests) — same race documented in the SDK behavior.

### [MINOR-1] `/book` is a verb path; should be `/orders` (or `/reservations`)

**Where**: [`internal/infrastructure/api/booking/handler.go:373`](../../internal/infrastructure/api/booking/handler.go)
**What**: REST convention is noun-paths. `/book` is a residue from earlier phases. The actual resource being created is a reservation (Pattern A).
**Recommendation**: Defer to a future `/api/v2` rev — not worth a breaking change in v1 today.

### [MINOR-2] No `Retry-After` on `GET /orders/:id` 404 inside async-processing window

**Where**: [`handler.go:251`](../../internal/infrastructure/api/booking/handler.go)
**What**: CLAUDE.md and the doc-comment say "retry with backoff" but no `Retry-After` header is emitted. Machine clients have to guess.
**Recommendation**: Emit `Retry-After: 1` on this 404. Cheap, semantic.

### [MINOR-3] Per-route rate-limit absent

**Where**: [`deploy/nginx/nginx.conf:142-181`](../../deploy/nginx/nginx.conf)
**What**: Single zone for all `/api/*`. `POST /book` and `GET /orders/:id` share a bucket.
**Recommendation**: Add a stricter zone for `POST /book` (flash-sale fairness) when N9 lands.

### [MINOR-4] Webhook doesn't dedupe on `env.ID`

**Where**: [`internal/application/payment/webhook_service.go (HandleWebhook)`](../../internal/application/payment/webhook_service.go)
**What**: Two redeliveries of the same Stripe event UUID (`evt_xxx`) are both processed. Idempotency is achieved via order-status checks (`isTerminalForWebhook`), not envelope-id dedup. This is correct but not documented in code; a future engineer might add an event-id cache without realizing the existing mechanism.
**Recommendation**: Add a code comment in `HandleWebhook` clarifying that status checks are the primary idempotency mechanism and event-id is intentionally NOT cached.

### [MINOR-5] `POST /events` returns 201 without a `Location` header

**Where**: [`handler.go:287`](../../internal/infrastructure/api/booking/handler.go)
**What**: 201 Created should include `Location: /api/v1/events/<new-id>`. Currently the client must construct the URL from the response body's `id`.
**Recommendation**: Add `c.Header("Location", "/api/v1/events/"+result.Event.ID().String())` before the 201 response.

### [NIT-1] Health probe paths under `/livez` / `/readyz` (not `/healthz`)

**Where**: [`api/ops/`](../../internal/infrastructure/api/ops/)
**What**: k8s-style naming, correct. Not a finding — just calling out that this aligns with kubelet defaults rather than the older `/healthz` convention.

### [NIT-2] `dto.ErrorResponse` field tag is `error` (lowercase)

**Where**: [`dto/response.go:208`](../../internal/infrastructure/api/dto/response.go)
**What**: Reserved JS keyword. Most TypeScript clients consume it fine but some linters complain. Non-blocking.

---

## Recommendations summary

| # | Severity | Topic | Effort |
| :-- | :-- | :-- | :-- |
| 1 | CRIT | Branch `handleFailure` on soft/hard decline + reservation window | M (~2 days; new metric, branching logic, tests covering both paths) |
| 2 | MAJOR | Cross-field guard: `EnableTestEndpoints` + `sk_live_*` → refuse startup | S (~2 hours; one Validate() block + test) |
| 3 | MAJOR | Implement OR 501 `/events/:id` | S (~3 hours to implement, 30 min for 501) |
| 4 | MAJOR | RFC 9457 `problem+json` envelope + machine `code` field | M (~3 days; new shared envelope type + handler migrations + client docs) |
| 5 | MAJOR | SETNX on idempotency write | S (~2 hours) |
| 6 | MINOR | `/book` → `/orders` (v2 only) | XS (defer) |
| 7 | MINOR | `Retry-After: 1` on async-window 404 | XS (5 lines) |
| 8 | MINOR | Per-route rate limit (N9 scope) | M (nginx zones OR Go middleware) |
| 9 | MINOR | Code comment on event-id-dedup non-strategy | XS |
| 10 | MINOR | `Location` header on `POST /events` | XS |

---

## References

### Stripe SOP
- [Stripe Idempotent Requests](https://docs.stripe.com/api/idempotent_requests) — 24h cache, body-mismatch behavior, key uniqueness.
- [Stripe Webhooks (verification)](https://docs.stripe.com/webhooks) — 5-min tolerance, raw-body requirement, constant-time comparison, SDK-first approach.
- [Stripe Verifying Payment Status](https://docs.stripe.com/payments/payment-intents/verifying-status) — `requires_payment_method` returns after failed attempt; PaymentIntent remains valid for retry.
- [Stripe Card Declines](https://docs.stripe.com/declines/card) — soft vs hard decline taxonomy.
- [Stripe PaymentIntent Lifecycle](https://docs.stripe.com/payments/paymentintents/lifecycle) — status machine.

### Industry analysis
- [Sameer Ahmed — "An Uncomfortably Deep Dive into the Idempotency Key"](https://sameerahmed56.medium.com/an-uncomfortably-deep-dive-into-the-idempotency-key-67626c8d3f3d) — body-hash canonicalization pitfalls.
- [Simplico — Idempotency in Payment APIs (2026-04-04)](https://simplico.net/2026/04/04/idempotency-in-payment-apis-prevent-double-charges-with-stripe-omise-and-2c2p/) — cross-provider comparison.
- [Medium — "Handling Payment Webhooks Reliably"](https://medium.com/@sohail_saifii/handling-payment-webhooks-reliably-idempotency-retries-validation-69b762720bf5) — sync vs async tradeoff.
- [Hooklistener — Stripe Webhooks Implementation 2026](https://www.hooklistener.com/learn/stripe-webhooks-implementation) — current Stripe SDK best practices.

### Standards
- [RFC 9457 — Problem Details for HTTP APIs](https://datatracker.ietf.org/doc/html/rfc9457) — replaces RFC 7807; same structure, updated text.
- [RFC 7807 — Problem Details for HTTP APIs (original)](https://datatracker.ietf.org/doc/html/rfc7807).
- [MDN — `Retry-After` header](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Retry-After).
- [Microsoft Q&A — Retry-After on 200/201/202](https://learn.microsoft.com/en-us/answers/questions/1336899/how-should-we-handle-retry-after-in-response-heade) — confirms valid on 2xx.

### Security
- [OWASP API Security Top 10 — 2023 Edition](https://owasp.org/API-Security/editions/2023/en/0xa2-broken-authentication/) — API2 Broken Authentication (webhook signature scope).
- [OWASP — API8 Security Misconfiguration](https://owasp.org/API-Security/editions/2023/en/0xa8-security-misconfiguration/) — test endpoint in non-prod env.

### In-repo cross-references
- [`docs/PROJECT_SPEC.md`](../PROJECT_SPEC.md) — § Payment intent + webhook contracts.
- [`docs/runbooks/README.md`](../runbooks/README.md) — D4.2 cutover note + leaked-key incident response.
- Memory file `payment_retry_design_gap.md` (2026-05-15, project-scope).
