# Booking Monitor — Pattern A browser demo (D8-minimal)

> Single-page Vite + React + TypeScript app that exercises the full
> Pattern A reservation → pay-or-expire flow against the running
> `booking_monitor` API. Mock-only — confirm step goes through
> `POST /test/payment/confirm/:order_id` (forges a signed webhook),
> not a real Stripe gateway. Real-Stripe wiring is deferred to D4.2.

## What the demo proves

The whole D1–D6 arc, in 30 seconds:

1. **Book** — `POST /api/v1/book` returns 202 with `order_id` + `reserved_until` + `links.pay`.
2. **Poll** — 1 Hz `GET /api/v1/orders/:id` watches the worker async-persist the row, then the user-driven status transitions.
3. **Pay** OR **Let it expire** — your call:
   - **Pay** → `POST /api/v1/orders/:id/pay` (PaymentIntent) → `POST /test/payment/confirm/:order_id?outcome=succeeded|failed` (mock webhook). Status flips `awaiting_payment` → `charging` → `paid` (or `payment_failed` + saga compensation).
   - **Let it expire** → do nothing. The D6 expiry sweeper flips `awaiting_payment` → `expired` once `reserved_until` elapses, then the saga reverts Redis inventory.
4. **Terminal** — UI's intent-aware status display ([src/intent.ts](src/intent.ts)) maps the `(intent, observed_status)` pair to a human-readable terminal so you don't see flicker through transient `charging` states.

## Prerequisites

- Node ≥ 20.19 (matches Vite 8's minimum; declared in [package.json](package.json) `engines`)
- The booking_monitor stack running. The simplest path is `docker compose up -d` from the repo root with `CORS_ALLOWED_ORIGINS` and `ENABLE_TEST_ENDPOINTS` set:

  ```bash
  # From repo root, NOT this directory
  export APP_ENV=development
  export CORS_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173
  export ENABLE_TEST_ENDPOINTS=true
  export PAYMENT_WEBHOOK_SECRET=demo_secret_local_only
  docker compose up -d
  ```

  - `APP_ENV=development` is **required** because `docker-compose.yml` passes `APP_ENV=${APP_ENV:-production}` into the container, which wins over `config/config.yml`'s yaml value (cleanenv precedence: env > yaml > env-default). Without this export the container starts as production and the PR #96 guard rejects `ENABLE_TEST_ENDPOINTS=true` at startup. (`make run-server` host-side does NOT need this — it reads the yaml directly.)
  - `CORS_ALLOWED_ORIGINS` lets the browser fetch from this app (5173 is the Vite dev server default; 127.0.0.1 covers OS configs that don't alias localhost).
  - `ENABLE_TEST_ENDPOINTS=true` mounts `/test/payment/confirm/:order_id`.

## Run the dev server

```bash
cd demo
cp .env.example .env.local    # optional — only if you need to override VITE_API_ORIGIN
npm install                   # first time only
npm run dev                   # http://localhost:5173
```

Open <http://localhost:5173> and click **Start a fresh booking**. The activity log streams every API call with timestamps.

## Configuration

| Env var | Default | What |
| --- | --- | --- |
| `VITE_API_ORIGIN` | `http://localhost` | Origin of the booking_monitor API. Demo derives `${origin}/api/v1` for the public surface and `${origin}/test` for the mock-confirm route. |

The default targets nginx on host port 80 (the only port the compose stack publishes externally). If you're running the Go binary directly via `make run-server`, override to `http://localhost:8080`.

## Layout

```
demo/
├── index.html             # Vite entry; sets the document <title>
├── package.json           # engines.node, scripts: dev / build / lint / preview
├── src/
│   ├── api.ts             # typed fetch client for the 6 endpoints in play
│   ├── intent.ts          # (intent, observed_status) → display message
│   ├── App.tsx            # single-page orchestrator (book/pay/expire + 1Hz poller)
│   ├── App.css            # light + dark theme via prefers-color-scheme
│   └── main.tsx           # React 19 root
└── public/favicon.svg     # default Vite favicon (replace if you want)
```

## Why "minimal"?

This is the D8-minimal posture per the post-Phase-2 roadmap: prove the API contract end-to-end through a browser without committing to a production frontend. Real Stripe Elements integration, multi-event listing, login/auth, and the full ticket-purchase journey are out of scope — they belong in D4.2 and beyond.
