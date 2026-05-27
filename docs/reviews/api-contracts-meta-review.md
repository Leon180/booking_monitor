# API Contracts & Idempotency — Meta-Review (Round 2)

**Meta-reviewer**: Senior code review specialist (Claude Opus 4.7), audit 2026-05-27
**Reviewing**: [`docs/reviews/api-contracts-review.md`](api-contracts-review.md) (612 lines, 48 KB)
**Scope**: push-back on miscalibrated severity, false findings, citation integrity, missed angles; no re-write of first-round content.

---

## TL;DR

The first-round review's **craftsmanship is high** (verified file:line citations match real code at the times checked) and most of its findings are real. But on this project it makes three structural mistakes:

1. **CRIT-1 and MINOR-4 are both documented `[Unreleased]` deferrals.** [`CHANGELOG.md:11-13`](../../CHANGELOG.md) explicitly lists "Payment retry window (known UX debt)" and "Webhook provider event-ID dedup (hardening)" as parked work — the reviewer treated the second-stage memory file as the only source of truth and missed the in-repo CHANGELOG entry, which is the authoritative deferral surface for a post-v1.1.0 portfolio project. The severity is calibrated for "we just found this gap"; the calibration that fits "we are knowingly shipping this" is one notch lower.
2. **Two of the five most-load-bearing citations are misrepresented.** The MS Q&A "confirms" Retry-After on 2xx — it is in fact an unanswered user question. Stripe's `/declines/card` page is cited as the "soft vs hard decline taxonomy" — the page uses `advice_code` (`try_again_later` / `do_not_try_again` / `confirm_card_data`); the words "soft decline" / "hard decline" do not appear on it. The first review's recommended fix for CRIT-1 is built on the soft/hard distinction, so the citation gap is also a recommendation gap.
3. **The state-of-the-repo footer is wrong.** The first review states "post v1.0.0 (2026-05-10)". v1.1.0 already shipped 2026-05-18 (Stage 5 durable Kafka intake + saga PG fence + drift detector auto-rehydrate). The post-v1.1.0 [Unreleased] surface has moved; some of the first review's recommendations now collide with shipped behaviour (the saga path is no longer the only revert path — there's a PG fence now).

On the upside: the BEST-IN-CLASS verdict on signature verification holds (verified at [`internal/infrastructure/api/webhook/verifier.go:137-158`](../../internal/infrastructure/api/webhook/verifier.go)), the gateway-idempotency analysis is correct, the price-snapshot integrity audit is correct, and the MAJOR-1 test-endpoint guard analysis is verified faithfully at [`config.go:791-806`](../../internal/infrastructure/config/config.go). Net: a useful review, but two notch-down severity recalibrations and three citation walk-backs are warranted before this drives a remediation PR.

---

## 1. False findings — items the first review flagged that are conscious deferrals

### 1.1 CRIT-1 "payment_failed reverts inventory on first soft decline" — verified gap, **but flagged as `[Unreleased]` debt** in CHANGELOG

**First-review claim** ([api-contracts-review.md:483-491](api-contracts-review.md)): the gap is present in `handleFailure` at `webhook_service.go:486` and warrants CRIT severity. Memory file `payment_retry_design_gap.md` is the only deferral pointer cited.

**Verified against current code**: gap is real. [`webhook_service.go:482-538`](../../internal/application/payment/webhook_service.go) — `handleFailure` unconditionally calls `MarkPaymentFailed` + emits `order.failed` outbox event with no branching on `LastPaymentError.Code` / `DeclineCode` / `reserved_until`. Memory file claim verified.

**Missed evidence** — the deferral is documented in the canonical Keep-a-Changelog surface, not only in a private memory file:

> [`CHANGELOG.md:12`](../../CHANGELOG.md): **Payment retry window** (known UX debt): `payment_failed` webhook currently fires saga compensation immediately; should preserve `awaiting_payment` state for retry within `reserved_until` window.

This is the project's authoritative public deferral statement, sitting in `[Unreleased]` after v1.1.0. Multiple things follow:

- The first review's "Memory file flagged this; the gap is still present" framing under-states the evidence — the gap is BOTH flagged AND publicly disclosed in the changelog as known debt. A reviewer's job on a known-deferred item is to call out **whether the deferral rationale still holds**, not to re-discover the gap.
- The reviewer used the right verification (gap is real in code) but the wrong severity gate (didn't check CHANGELOG `[Unreleased]`). For a portfolio project that has explicitly declared its v1.1.0 surface complete and named the gap in the [Unreleased] list, **CRIT compresses signal**: every reviewer who reads the codebase fresh will re-discover this and assign CRIT, but the maintainer's decision queue is finite.

**Verdict**: gap is real; severity should be **MAJOR (defensible)** or **CRIT-known (operationally clearer)**, NOT CRIT in a list that conflates known-deferred with newly-discovered. The recommendation itself (branch on decline code + reservation window) is correct and the patch shape is reasonable — see §1.4 below for a citation problem with HOW the branch should be written.

### 1.2 MAJOR-1 "`ENABLE_TEST_ENDPOINTS=true` only blocked when `APP_ENV=production`" — real gap, real severity, **but defense-in-depth not credited**

**First-review claim** ([api-contracts-review.md:493-499](api-contracts-review.md)): staging + unset APP_ENV + live Stripe keys + test endpoints is a valid combo at startup; needs unconditional cross-field check rejecting `EnableTestEndpoints=true` + `sk_live_*` prefix.

**Verified at [`config.go:791-806`](../../internal/infrastructure/config/config.go)**: the production-only gate is exactly as claimed. The literal `"production"` string is the only trigger; staging / "" / typo bypass it.

**Missed evidence** (defense-in-depth the reviewer didn't credit):

- [`config.go:873-900`](../../internal/infrastructure/config/config.go) (verified via grep) — when `PAYMENT_PROVIDER=stripe`, Validate() also rejects `sk_test_*` / `rk_test_*` / `pk_test_*` keys (unless `STRIPE_ALLOW_TEST_KEY=true` is set), and rejects `pk_live_*` keys outright (publishable keys are server-side-invalid). So the failure mode the first review imagines — "staging boots with `sk_live_*` AND test endpoints AND can settle real money" — IS valid, but the symmetric "production boots with test keys silently" is independently blocked.
- The first review's recommended fix (live-key prefix + `EnableTestEndpoints` cross-field guard) is the **right second-layer fix**. Defense-in-depth, not just a missing first-layer check.

**Verdict**: finding is real, severity (MAJOR) is well-calibrated, recommended fix is correct. The first review **under-credits** that there are already two related guards in place (`production` block + `*_test_*` rejection at the gateway) and that the gap is "the one combo neither guard catches" rather than a wholesale absence of guards. Phrasing nit, not a severity miscalibration.

### 1.3 MAJOR-2 "`GET /api/v1/events/:id` stub returns 200" — verified, **but flagged as deferred in architectural backlog**

**First-review claim** ([api-contracts-review.md:501-506](api-contracts-review.md)): handler returns 200 with `{"message": "View event", "event_id": "<echo>"}` for any path param; biases the conversion-rate metric; should be 501 OR implemented.

**Verified at [`handler.go:290-294`](../../internal/infrastructure/api/booking/handler.go)**: exactly as described.

**Missed evidence** — the deferral is explicitly tracked:

> [`docs/architectural_backlog.md:202-205`](../architectural_backlog.md): `GET /api/v1/events/:id` full implementation — currently a stub... **Why deferred.** Implementing requires touching application/domain/handler layers; the demo (Phase 3) is the natural time when "view event" becomes a real user flow. **Revisit when.** Phase 3 D-series PRs land — specifically D8 (frontend bootstrap) which will exercise the read path.

And [`CLAUDE.md` § API endpoints](../../.claude/CLAUDE.md): `GET /api/v1/events/:id  # Stub — returns {"message": "View event", "event_id": ...} + bumps page_views_total. Does NOT load event details (deferred to Phase 3).`

**Verdict**: the gap is documented in TWO places — backlog + CLAUDE.md. The interesting finding is NOT that the handler is a stub (everyone knows) but that **the metric bias is undocumented**. `page_views_total{type="event_detail"}` will be polluted by random URL guesses, and no one tracking it knows that. THAT angle is well-calibrated, not a phantom finding — but the headline framing "/events/:id is a stub" is restating a documented deferral. Severity should be **MINOR** ("the documented stub also has an undocumented metric-bias quirk") rather than **MAJOR** ("the API has an undisclosed stub").

### 1.4 MINOR-4 "Webhook doesn't dedupe on `env.ID`" — `[Unreleased]` debt, severity well-calibrated

**Verified at [`webhook_service.go:173,210,245,256,275`](../../internal/application/payment/webhook_service.go)**: `env.ID` is logged but not used for dedup; reliance on order-status checks (correct mechanism).

**Missed evidence**:

> [`CHANGELOG.md:13`](../../CHANGELOG.md): **Webhook provider event-ID dedup** (hardening): store Stripe `evt_*` ID to make double-delivery provably idempotent at the HTTP boundary, not just at the state-machine layer.

Already publicly declared as deferred hardening. The first review's MINOR severity + "add a code comment" recommendation is **well-calibrated** for a known-deferred item — this is the correct shape of feedback for backlog-tracked work.

---

## 2. Severity miscalibration

### 2.1 The "BEST-IN-CLASS" verdict on signature verification

**First-review claim** ([api-contracts-review.md:159](api-contracts-review.md)): "BEST-IN-CLASS. This is a textbook implementation. No findings."

**Sample-verified at [`verifier.go:137-158`](../../internal/infrastructure/api/webhook/verifier.go) + [`handler.go:100-158`](../../internal/infrastructure/api/webhook/handler.go)**:

- Constant-time HMAC via stripe-go's `webhook.ValidatePayloadWithTolerance` ✓ (docstring at verifier.go:135 cites `hmac.Equal`).
- 5-min default skew via configurable `tolerance time.Duration` ✓.
- Raw body BEFORE unmarshal via `io.ReadAll(io.LimitReader(c.Request.Body, MaxBodyBytes+1))` at handler.go:111 ✓.
- 64 KiB body cap with +1 trick for truncation detection at handler.go:111+118 ✓.
- Empty-secret pre-flight at verifier.go:146 returns ErrConfigError → 500 ✓.
- Future-skew explicitly accepted with documented rationale at verifier.go:54-61 ✓.

**Verdict**: BEST-IN-CLASS is defensible. **One nit the first review missed**: the future-skew acceptance (verifier.go:54-61) is a real change-of-behavior from the pre-3b verifier and is documented as "judged acceptable" — for a security audit, even a documented unilateral acceptance of asymmetric tolerance deserves a sentence in the review acknowledging the trade-off was made, not just the implementation. The first review's "no findings" doesn't surface this. Not a severity miscalibration (still essentially BEST-IN-CLASS), but a documentation completeness nit.

### 2.2 CRIT-1 — see §1.1

The severity argument is essentially: how does the audit format handle "known debt that is genuinely high impact"? Three reasonable answers:

- **CRIT (first review's choice)**: signals "fix urgently". Compresses signal — a maintainer can't differentiate "known debt CRIT" from "newly-discovered CRIT" without context.
- **MAJOR (this meta-review's preference)**: signals "fix this version cycle". Reads cleanly in a list of mixed-known/new findings.
- **CRIT-known label (richer scheme)**: explicit known/new field. More precise, but the first review's format doesn't have it.

Given the format is flat, **MAJOR** is the right severity for a known-deferred-shipping item with a real user-impact gap. Defensible to leave it CRIT if the reviewer's signal is "the deferral rationale no longer holds because the [Unreleased] item has been parked for a long time without a date" — but the first review doesn't make that argument; it argues from severity alone.

### 2.3 The first review's tone on Stripe SOP alignment

> [api-contracts-review.md:10-12](api-contracts-review.md): "The system is **mostly aligned** with Stripe SOP and the wider 2025 webhook / idempotency literature — better than the typical Phase-3 prototype."

Sample verifications above (§2.1) support this for the verification path. Other claims also hold:

- Idempotency-Key fingerprint matches Stripe's published behavior ✓ (verified at idempotency.go:213-263).
- Gateway `SetIdempotencyKey(orderID.String())` matches Stripe's recommended convention ✓ (verified at [stripe_gateway.go:287](../../internal/infrastructure/payment/stripe_gateway.go) per the first review's citation).
- 24h Idempotency-Key TTL aligns ✓.
- Price snapshot at intent-create time aligns ✓ (verified at [service.go:144-145](../../internal/application/payment/service.go)).

**Verdict**: the alignment tone is calibrated. The first review's "BEST-IN-CLASS" verdict on the webhook handler — verified — is the strongest backing for the broader tone.

### 2.4 MAJOR-4 "Idempotency cache is SET, not SETNX"

**First-review claim** ([api-contracts-review.md:518-523](api-contracts-review.md)): non-atomic write between Get and Set produces a slow-handler-wins race; recommends SETNX.

**Verified at [`idempotency.go:88-107`](../../internal/infrastructure/cache/idempotency.go) + [`api/middleware/idempotency.go:307-316`](../../internal/infrastructure/api/middleware/idempotency.go)**: the race is real.

**Severity push-back**: MAJOR seems high. The first review itself argues that:

- Inventory deduction is protected by atomic Lua (correct).
- Order rows are protected by DB UNIQUE on `(idempotency_key, user_id)` (correct).
- The actual race surface is "slow handler's response body wins" in the cache.

For a flash-sale system where Idempotency-Key replays produce semantically-equivalent 202 envelopes (the order_id is the same — minted in the handler before Redis), "slow handler's body wins" is **a cosmetic difference**, not a correctness defect. The `expires_in_seconds` field could differ by milliseconds; nothing else in the 202 body would. This is acceptable for the same reason Stripe documents the same race-permissive behavior on their own server (cited by the first review at api-contracts-review.md:523).

**Recommended severity**: **MINOR**. The fix is cheap and clean (SETNX), and there's value in deterministic-first-write for the operator narrative — but framing this as MAJOR alongside test-endpoint-bypassing-live-keys overweights it.

---

## 3. Citation integrity — sample of 5 verified

| First-review citation | Verified? | Notes |
| :-- | :-- | :-- |
| RFC 9457 obsoletes RFC 7807 | **YES** | Verified verbatim against [datatracker.ietf.org/doc/html/rfc9457](https://datatracker.ietf.org/doc/html/rfc9457): "This document obsoletes RFC 7807." |
| Stripe Verifying Status: `requires_payment_method` returns after failed attempt | **YES** | Verified verbatim against [docs.stripe.com/payments/payment-intents/verifying-status](https://docs.stripe.com/payments/payment-intents/verifying-status): "What Happened: The customer's payment failed on your checkout page → Expected PaymentIntent Status: `requires_payment_method`". The first review's reasoning is correctly grounded. |
| MS Q&A "confirms Retry-After is valid on 200/202" | **NO — MISREPRESENTED** | The page is an **unanswered user question**, not Microsoft confirmation. The user wrote: "We are getting 'Retry-After' in response headers for response status codes like - 200,201 and 202... though we receive 'Retry-After', the requests are getting executed successfully" — and the question went unanswered. The first review at api-contracts-review.md:78 says "Microsoft Q&A confirms it is also valid on 200/202", which inverts the page. |
| Stripe Card Declines: soft vs hard decline taxonomy | **PARTIALLY MISREPRESENTED** | [docs.stripe.com/declines/card](https://docs.stripe.com/declines/card) does NOT use the terms "soft decline" / "hard decline" — those terms do not appear on the page. The page uses `advice_code` (`try_again_later` / `do_not_try_again` / `confirm_card_data`). The first review names specific codes (`insufficient_funds`, `do_not_honor`, `processing_error`, `fraudulent`, `lost_card`, `stolen_card`, `pickup_card`) as if Stripe groups them under soft/hard — Stripe groups them under advice codes. The taxonomy the first review uses is industry-vernacular (Visa / Mastercard scheme docs) but is **not what Stripe's docs at the cited URL teach**. |
| OWASP API8:2023 Security Misconfiguration → "test endpoints in non-prod env" | **OVER-CLAIMED** | [owasp.org/API-Security/editions/2023/en/0xa8-security-misconfiguration](https://owasp.org/API-Security/editions/2023/en/0xa8-security-misconfiguration/) does NOT specifically discuss test endpoints, dev/test environment isolation, or feature flag misconfiguration. It speaks at the level of "unnecessary features are enabled (e.g. HTTP verbs, logging features)". The first review uses OWASP API8 as a citation that's adjacent-but-not-load-bearing; the specific failure mode it describes is the project's own (sk_live_* + test endpoints), which OWASP doesn't itself enumerate. Defensible as a "category-of-issue" citation but the first review's framing reads like OWASP names this scenario specifically — it doesn't. |

**Citation integrity verdict**: **3 of 5 sampled citations are misrepresented or over-claimed**. Per the user's documented project rule (`benchmark_methodology_dual_scenario.md`: "citation integrity rule: always click-through before merging"), this is a meaningful audit-quality concern. The RFC 9457 and Stripe Verifying Status citations are pristine; the MS Q&A and `declines/card` citations need walk-back; OWASP needs more careful framing.

**Specific recommendation**: the CRIT-1 fix (§5 of the first review) is written around "soft decline" / "hard decline" branching with a presumed taxonomy at `last_payment_error.decline_code`. The actual Stripe-aligned branching axis is `last_payment_error.advice_code` ∈ `{try_again_later, do_not_try_again, confirm_card_data}` per [docs.stripe.com/declines/card](https://docs.stripe.com/declines/card). The fix shape is right; the field names need correcting before someone writes the patch.

---

## 4. Actionability of recommendations

The first review's recommendations are **mostly actionable** — coordinate-bearing file:line citations + concrete recommended code shape. Sample audit:

| Finding | Code-shape recommendation? | Effort estimate? | Issues |
| :-- | :-- | :-- | :-- |
| CRIT-1 (payment_failed branching) | YES — full Go snippet | YES — "~2 days, new metric + branching + tests" | **Field name wrong**: `LastPaymentError.DeclineCode` should be `LastPaymentError.AdviceCode` per Stripe SOP (see §3 above) |
| MAJOR-1 (test-endpoint cross-field) | YES — full Validate() block | YES — "~2h" | Clean, correct |
| MAJOR-2 (events:/id stub) | YES — pick 501 or implement | YES — "~3h to implement, 30min for 501" | Clean |
| MAJOR-3 (RFC 9457 envelope) | YES — example envelope | YES — "~3 days" | Hand-wavy on the **migration path** — "keep `error` for one release cycle for backward compat" is one sentence, but the API has ~20 ErrorResponse-emitting sites; the migration is a multi-PR sequence, not a "release cycle" |
| MAJOR-4 (SETNX) | YES — one-line idea | YES — "~2h" | Severity overcalibrated (see §2.4) |
| MINOR-1 (`/book` rename) | "Defer to v2" — no code | NO (XS) | Defer is the right call but lists no v2 trigger |
| MINOR-2 (Retry-After: 1) | YES — header value | YES — "5 lines" | Citation invalid (see §3) — RFC 9110 doesn't prohibit 2xx Retry-After, but it doesn't bless 2xx either; the recommendation needs honest framing |
| MINOR-5 (Location header on /events 201) | YES — exact line | YES — XS | Clean |

**Verdict**: actionability is generally high but two recommendations (CRIT-1 and MINOR-2) bake misrepresented citations into the patch shape. A maintainer implementing CRIT-1 verbatim will write `LastPaymentError.DeclineCode` and find that Stripe Go SDK does have that field, but the SDK's documented branching axis is `AdviceCode` — they'll merge a patch that mostly works but doesn't match the cited authority. Patch the citation, the patch shape stays the same.

---

## 5. Missed angle — **OpenAPI spec / contract test coverage**

The first review covers 12 sub-dimensions (resource modeling / status codes / idempotency / webhooks / payment retry / gateway idempotency / price snapshot / versioning / error envelope / stub endpoint / CORS-CSRF-rate-limit / test endpoint safety). It does NOT cover **API specification artifacts**.

**State of the repo (verified)**:

```
find . -name "openapi*" -o -name "swagger*" -o -name "*.openapi.yml"
# (no results)
```

No `openapi.yaml`, no `swagger.json`, no Gin OpenAPI auto-generator (`swag init`), no contract tests (no `dredd` / `schemathesis` / `pact` references). The API surface is described **only** in:

- [`CLAUDE.md` § API endpoints](../../.claude/CLAUDE.md) — markdown prose (~12 lines for the whole API).
- [`docs/PROJECT_SPEC.md`](../PROJECT_SPEC.md) — markdown prose.
- DTO Go types (`internal/infrastructure/api/dto/`) — source code, no annotations.

**Implications**:

- **No machine-consumable contract.** A new consumer (frontend, mobile app, internal service) has to read Go source. The browser demo at `demo/` works because the project author wrote both sides; a third-party integrator would need to reverse-engineer.
- **No contract-test enforcement in CI.** Breaking changes to response shape are caught only by:
  - Unit tests on the DTO mapper functions (these catch field renames within Go but not the wire-format implication for clients).
  - The browser demo's smoke test (manual, not in CI per [.github/workflows/ci.yml](../../.github/workflows/ci.yml)).
- **No `Sunset` header strategy**, no version-skew matrix — the first review notes this at §8 ("versioning strategy") but treats it as a 1-page doc task. The deeper missing primitive is the OpenAPI artifact that would let `Sunset` mean something machine-readable.
- **No swagger UI / redoc** at any operational URL — there's no `/docs`, `/swagger`, `/openapi.json`. Standard for an API in 2026.

**Severity assessment**: for a portfolio / interview-prep project, this is a **legitimate gap that lowers credibility** more than it lowers correctness. The audience for the project (interviewers, fellow engineers reading the repo) expects an OpenAPI spec in 2026. The absence is more visible than the test-endpoint cross-field gap because it's discoverable at directory level, not at startup-config level.

**Recommendation** (sized as an action item the first review didn't surface):

1. **Generate `docs/openapi.yaml`** from the existing DTOs. Two paths:
   - `swag init` (gin-swagger) — annotation-driven, minimal code disruption. ~4 hours.
   - Hand-written YAML reflecting the 8 endpoints. ~2 hours; staler over time.
2. **Mount `/docs` (Redoc or Swagger UI)** in non-production. Reuse the `EnableTestEndpoints` gate or add `ENABLE_API_DOCS`. ~30min.
3. **Add a contract test in CI** — `schemathesis` against `openapi.yaml` runs ~2 minutes; catches DTO drift before clients see it. Add to `.github/workflows/ci.yml` as a 5th job.
4. **Frame `Sunset` strategy ON the OpenAPI** — once the spec exists, version-deprecation can be expressed as schema annotations (`deprecated: true` + `x-sunset-date`).

**Why this matters more than the first review's MINOR-3 (per-route rate limit)**: per-route rate-limit is interview-defensible as "nginx zone splitting deferred to N9". OpenAPI absence is harder to defend — every Stripe/Twilio/Shopify-equivalent public API has one, and an interviewer reading the repo expects one.

---

## 6. Verdict

**Use this review as a checklist, walk back two CRIT/MAJOR severities, fix three citations, and add the OpenAPI angle.**

| Aspect | Round-1 score | Notes |
| :-- | :-- | :-- |
| Code-claim accuracy (file:line verifications) | **HIGH** | All 5 sampled citations match real code at audit-time. |
| Severity calibration | **MEDIUM** | CRIT-1 should be MAJOR (deferred); MAJOR-2 should be MINOR (documented stub + undocumented metric quirk); MAJOR-4 should be MINOR (race surface bounded). Otherwise calibrated. |
| Citation integrity | **MEDIUM-LOW** | 3 of 5 sampled URLs misrepresented or over-claimed (MS Q&A, Stripe declines, OWASP). The recommended CRIT-1 patch shape inherits the misrepresented `decline_code` field name. |
| Actionability | **HIGH** | File:line + code-shape recommendations on 9 of 11 findings. Effort estimates are realistic. |
| Coverage breadth | **MEDIUM-HIGH** | 12 sub-dims covered; OpenAPI / contract artifacts not covered (§5 of this meta-review). |
| State-of-repo currency | **LOW** | Footer says "post v1.0.0 (2026-05-10)"; v1.1.0 shipped 2026-05-18. Several recommendations need re-checking against post-v1.1.0 state (saga PG fence is post-v1.0.0). |
| Trade-off awareness | **MEDIUM** | Cited memory file for payment-retry; missed CHANGELOG [Unreleased]; missed architectural_backlog.md `/events/:id` entry. |

**Net recommendation**:

1. Walk **CRIT-1** down to **MAJOR-known** in the headline list. Patch its citation — branch on `advice_code`, not "soft/hard `decline_code`".
2. Walk **MAJOR-2** down to **MINOR** (documented stub + undocumented metric quirk).
3. Walk **MAJOR-4** down to **MINOR** (race is bounded by lower-layer guards as the first review itself acknowledges).
4. Walk **MAJOR-3 (RFC 9457)** to a **proper migration plan** — the "release cycle" framing is glib for ~20 ErrorResponse sites; either commit to a multi-PR sequence or scope the migration to webhook 500s + payment 409s only.
5. **Add §5** (OpenAPI / contract test) as the missing sub-dimension. Sized as ~6 hours of work for a documented, machine-consumable, CI-enforced API spec.
6. **Refresh the footer** to reflect v1.1.0. Re-check that no recommendation conflicts with [`CHANGELOG.md` 1.1.0 entries](../../CHANGELOG.md) (saga PG fence, drift detector auto-rehydrate).
7. Citations to walk back:
   - MS Q&A on Retry-After (cite RFC 9110 §10.2.3 directly OR remove the 2xx-emit recommendation).
   - Stripe `/declines/card` "soft vs hard" (re-cite using `advice_code` taxonomy).
   - OWASP API8 (soften to "category" not "this scenario").

The first review's strongest sections — webhook signature audit, gateway idempotency, price snapshot integrity — are **production-grade work** and should stand verbatim. The findings list is real; the severity dial and citation polish are where the round-2 pass adds value.
