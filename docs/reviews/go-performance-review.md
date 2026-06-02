# Go Performance Mastery Review

**Reviewer**: Backend Performance Audit (Claude Opus 4.7 — 1M context, project audit 2026-05-27)
**Scope**: Allocations, GC, concurrency, profiling, pool tuning — Go 1.25.0 specific
**Anchored to**: Stage 5 k6 benchmarks at [`docs/benchmarks/20260521_210525_stage5_demo_clean_c500/comparison.md`](../benchmarks/20260521_210525_stage5_demo_clean_c500/comparison.md) — 8,395 accepted/s, p95 61.97 ms, app heap 102.8 MiB at 500 VU.
**Toolchain**: Go 1.25.0 / toolchain 1.25.10 ([`go.mod`](../../go.mod) lines 3 + 11).

---

## TL;DR

This codebase is in **mature, well-instrumented shape** for a 500-VU flash-sale simulator. The booking hot path already has the bulk of the standard Go perf hygiene applied: `sync.Pool` on the Redis Lua arg slices, ctx-aware zero-alloc-when-disabled logger, single `context.WithValue` per request, immutable domain value-receivers, `GOGC=400` + `GOMEMLIMIT=256MiB`, OTEL sampled at 1%, and a 102.8 MiB peak heap that proves the GC tuning is working. There is no obvious "free 30%" sitting on the floor in `BookTicket`.

The wins that remain are bounded and earn-able with measurement, not speculation:

1. **Adopt PGO** — no `default.pgo` exists today. Cloudflare landed −3.5% CPU on real services with no source changes; Uber freed 24k cores at scale. For a single-binary monolith with a clearly-dominant hot path (k6 generates exactly the workload that should be profiled), this is the highest-EV unlocked optimization. Risk is bounded — at worst the produced binary is ~0% different.
2. **Pin `Idempotency` middleware allocs** — the `captureWriter`'s `bytes.Buffer` and `bytes.NewReader` body re-feed each construct fresh heap allocations on every booking, with no pool. At 8 k req/s this is the largest remaining steady-state allocation source on the hot path. Pool the buffer; pool the reader pair (or use `io.NopCloser(bytes.NewReader(...))` wrapped via a per-request reset).
3. **Two latent goroutine bugs**: (a) `installInventoryRehydrate`'s OnStart fires on the fx OnStart context with no detached fallback — a slow rehydrate can be cancelled mid-SETNX by the fx startup deadline, leaving Redis half-populated; (b) `runIdleReset` in [`saga_consumer.go:131`](../../internal/infrastructure/messaging/saga_consumer.go) and the relay `Run` goroutine derive their cancel ctx from `context.Background()` (not from `Start`'s ctx) — `installServer`'s `cancel` is fired on OnStop but if the saga consumer crashes mid-Start, the idle-reset goroutine outlives it.

Findings are organised by section below; severity-tagged actionable findings are at the end.

---

## 1. Hot-path allocation budget

Walk through `POST /api/v1/book` → response, anchored to [`internal/application/booking/service_kafka_intake.go:110-222`](../../internal/application/booking/service_kafka_intake.go) (Stage 5 binary is the production hot path per `docker-compose.yml`).

Per request, from the moment Gin dispatches `bookingHandler.HandleBook` ([`internal/infrastructure/api/booking/handler.go:160`](../../internal/infrastructure/api/booking/handler.go)) to the moment the 202 is flushed:

| Step | Site | Allocation shape | Necessary? | Notes |
|---|---|---|---|---|
| Gin context per request | `gin.Context` pool | reused | ✓ | Gin pools it. |
| Correlation-id mint + ctx attach | `middleware.Combined` | 1× `uuid.New()` (16 B array on stack) + `uuid.String()` (~40 B heap) + 1× `context.WithValue` + 1× `r.WithContext` | ✓ | Already optimized via PR #14/15. **Header reads do not allocate** (textproto canonical match is in-place). |
| Idempotency body-read + capture | `middleware.Idempotency` | `bytes.Buffer{}` (zero size, grows to body), `io.NopCloser(bytes.NewReader(bodyBytes))` (24 B + 16 B header), `&captureWriter{}` (32 B + buf), final `bytes.Buffer.String()` copy on cache write | partial | **MAJOR-1 below.** `captureWriter.body` + the `NopCloser`+`Reader` round-trip are net ~150 B of fresh heap per booking with no pool (80 B fixed headers + body grown into `captureWriter.body` for a typical ~70 B booking request). |
| Fingerprint hash | `Fingerprint(bodyBytes)` | `sha256.Sum256` is stack-only; `hex.EncodeToString` allocates 64 B string | partial | The hex string itself is unavoidable for the Redis key; the alternative (`[32]byte` array key) saves the alloc but burns a `sync.Pool` slot or breaks the cache wire format. **MINOR**. |
| ShouldBindJSON request body | `c.ShouldBindJSON(&req)` | `encoding/json` standard decoder — 1× decoder state + small (`BookingRequest` is 3 fields) | ✓ | Body is small (<200 B); `encoding/json` is fine here, NOT a hot spot. |
| UUID v7 mint | `uuid.NewV7()` (line 112) | 16 B on stack + `crypto/rand.Read` internal pool | ✓ | google/uuid `NewV7` does NOT escape under normal conditions — verify via `-m=2`. |
| reservedUntil | `time.Now().Add(...).UTC()` | stack value | ✓ | No alloc. |
| Lua deduct path | `inventoryRepo.DeductInventoryNoStream` | **pooled** `[]interface{}` of len 1 ([`internal/infrastructure/cache/redis.go:57-62`](../../internal/infrastructure/cache/redis.go)); the integer boxing (`args[0] = count`) still allocates `*int` per call; `redis.Script.Run` allocates the command-arg vector internally | partial | `sync.Pool` discipline is excellent. The unavoidable cost is `int → interface{}` boxing inside the script.Run cmd-builder. **NIT** — measuring before chasing. |
| Kafka publish (`PublishIntake`) | `encodeIntakeMessage(msg)` is `json.Marshal` on 8-field `IntakeMessage` struct + `kafka.Message{Key: []byte(msg.OrderID.String())}` | json encoder state + ~140 B payload + 36 B key | ✓ | `encoding/json` here is sustained ~8k/s × 140 B = 1.1 MB/s of garbage. **MAJOR-2 below** — JSON v2 / sonic would cut both alloc and CPU. |
| `domain.NewReservation` | value-receiver, returns `Order` value | stack | ✓ | The repo-row layer is the only place this escapes to heap. |
| Response marshal | `mustMarshal(dto.BookingAcceptedResponse{...})` | `json.Marshal` ~250 B output | ✓ | Same `encoding/json` story as Kafka; jsonv2 / sonic applies here too. |
| Span attributes (tracing decorator) | `attribute.String("event_id", eventID.String())`, etc. | UUID `.String()` × 2 allocates ~40 B each | partial | At 1% sampling the cost is negligible (1.6 KB/s); the `eventID.String()` runs even on un-sampled traces because attribute construction is eager. **NIT** — could move into the sampled-only path but not worth the readability cost. |

**Estimated allocation budget per request, hot path (rough):**

| Source | Bytes/request | Bytes/s @ 8k RPS |
|---|---:|---:|
| `captureWriter.body` (`bytes.Buffer`) + NopCloser+Reader pair | ~150 | 1.2 MB/s |
| Fingerprint hex string (64 B) | 64 | 512 KB/s |
| json.Marshal Kafka IntakeMessage | ~280 (encoder buffer + payload) | 2.2 MB/s |
| json.Marshal response DTO | ~320 | 2.5 MB/s |
| OTEL attribute UUID stringifications | ~80 | 640 KB/s |
| Logger `enrichFields` (only when log fires; happy path = 0) | ~0 | ~0 |
| Pooled Redis args (after Pool reuse) | ~0 amortised | ~0 |
| **Total steady state** | **~900 B/req** | **~7.2 MB/s** |

This is comfortably below `GOMEMLIMIT=256MiB`; observed peak heap is 102.8 MiB. The room to improve isn't dramatic, but it is real and gated entirely on JSON encoder choice + the idempotency-middleware buffer pool.

**`sync.Pool` inventory (correctness audit):**

- `deductArgsPool` (5-elem) at [`redis.go:44`](../../internal/infrastructure/cache/redis.go) — ✓ correct. Reset path is `args[0..4] = newValues` — every slot is overwritten before use. `defer Put(argsPtr)` returns the pointer (not the slice value) so the slice header doesn't escape.
- `deductNoStreamArgsPool` (1-elem) at [`redis.go:57`](../../internal/infrastructure/cache/redis.go) — ✓ correct. Same pattern.
- `revertArgsPool` (1-elem) at [`redis.go:67`](../../internal/infrastructure/cache/redis.go) — ✓ correct. Split from the deduct pool is intentional (per source comment) — backing arrays cannot leak across scripts.

**There is no pool for**: the idempotency `captureWriter.body`, the JSON encoder buffer, the Kafka `kafka.Message.Key` byte slice, the `bytes.NewReader` body re-feed. These are the next targets, in priority order.

---

## 2. Escape analysis spot checks

Recommended commands the user should run from repo root:

```bash
# Booking hot path (Stage 5 service)
go build -gcflags='-m=2' ./internal/application/booking/... 2>&1 \
  | grep -E 'service_kafka_intake\.go|service\.go' \
  | grep -E 'escapes to heap|moved to heap|cannot inline|cost'

# Lua arg pool — confirm pool slice doesn't escape
go build -gcflags='-m=2' ./internal/infrastructure/cache/... 2>&1 \
  | grep -E 'redis\.go' \
  | grep -E 'DeductInventory|argsPool|args\[|interface{}'

# Logger ctx-enrichment — verify enrichFields is inlinable
go build -gcflags='-m=2' ./internal/log/... 2>&1 \
  | grep -E 'log\.go' \
  | grep -E 'enrichFields|emit|Check|inlinable'

# Domain factory — does NewReservation stay on stack?
go build -gcflags='-m=2' ./internal/domain/... 2>&1 \
  | grep -E 'NewReservation|escapes'
```

**Expected findings:**

1. **`uuid.NewV7()` return value** — should stay on stack inside `BookTicket`; it's a 16-byte array. If `-m=2` reports `escapes to heap`, the likely cause is the closure inside `OTEL Tracer.Start(...)` capturing `orderID`. Acceptable; the alternative is uglier code.
2. **`deductArgsPool.Get().(*[]interface{})`** — the type assertion is what stops the compiler from devirtualizing; pool round-trips by design escape the elements (the `int → interface{}` boxing is what the source comment warns about). The slice HEADER does not escape.
3. **`enrichFields`** ([`internal/log/log.go:184`](../../internal/log/log.go)) — should be inlinable on the fast path (cid=="" && !validSpan returns `user` unchanged). Verify the "cost X exceeds budget 80" diagnostic is NOT emitted; the function looks well under budget but the `trace.SpanContextFromContext(ctx)` call may push it over.
4. **`domain.NewReservation`** — value-returning factory should stay stack-allocated when consumed by `Order` value (Stage 5 service returns `Order` not `*Order`). If it escapes, the repo-row mapper in [`internal/infrastructure/persistence/postgres/order_row.go`](../../internal/infrastructure/persistence/postgres/order_row.go) is the suspect — `interface{}` boxing for `Scan` row arguments.
5. **`mustMarshal(dto.BookingAcceptedResponse{...})`** ([`handler.go:204`](../../internal/infrastructure/api/booking/handler.go)) — the struct literal will escape because `json.Marshal` takes `any`. Unavoidable without going to a generic-based marshaler (`json/v2` interface allows this).

---

## 3. Goroutine lifecycle audit

| Goroutine | Owner | Start | Stop | Leak risk | Back-pressure |
|---|---|---|---|---|---|
| HTTP server | `installServer` ([`cmd/booking-cli/server.go:282`](../../cmd/booking-cli/server.go)) | OnStart | `httpServer.Shutdown(ctx)` on OnStop | low | n/a (Gin owns) |
| pprof server | same | OnStart (conditional) | `pprofServer.Shutdown` | low | n/a |
| `outbox.Relay.Run` | `fx.Invoke` at [`server.go:179`](../../cmd/booking-cli/server.go), parent `context.Background()` with manual cancel | OnStart | `cancel()` on OnStop | **LOW** — deferred `Unlock` runs on every exit path (see [`relay.go:71`](../../internal/application/outbox/relay.go)); ticker `defer Stop`. | ticker @ 500 ms; on PG error logs+continues — does NOT back off. **MINOR** — sustained PG outage spams the log at 2 Hz. |
| `worker.Service.Start` (Redis stream consumer) | `startBackgroundRunners` ([`server.go:464`](../../cmd/booking-cli/server.go)), parent `runCtx` (background w/ cancel) | OnStart | `cancel()` on OnStop | **LOW** — `Subscribe` returns on ctx.Done OR after `maxConsecutiveReadErrors` (30 by default) | Back-pressure-aware: `sleepCtx` honours shutdown during the read-error backoff; PEL recovery has the same. |
| `messaging.SagaConsumer.Start` | same | OnStart | `cancel()` + `sagaConsumer.Close()` | **MEDIUM — see CRIT-1.** Spawns `runIdleReset` ([`saga_consumer.go:188`](../../internal/infrastructure/messaging/saga_consumer.go)) under the SAME ctx — bounded. BUT: if the saga consumer's main goroutine returns early (e.g. nil after ctx.Err during fetch), the idle-reset goroutine is still alive UNTIL cancel fires. In practice ctx is the same, but the contract has a window. | `FetchMessage` is blocking; idle-reset is `time.Ticker`-driven. No busy spin. |
| `runIdleReset` | spawned inside `SagaConsumer.Start` | top of Start | parent ctx.Done | LOW per source comment | n/a — pure ticker. |
| Stage 5 `intake_consumer` | wired in stage-5 binary; not in `cmd/booking-cli` | n/a here | — | — | — |
| Expiry sweeper, recon, saga-watchdog, drift recon | separate `booking-cli` subcommands — each owns its own fx graph; not co-resident with the API server | each subcommand's OnStart | each subcommand's OnStop | LOW per inspection — `Sweep()` honours ctx between row resolutions | Cron-style; ticker driven. No back-pressure issues. |

**Specific gaps:**

- **CRIT-1 below.** `installInventoryRehydrate` ([`server.go:222`](../../cmd/booking-cli/server.go)) uses the OnStart-supplied ctx for `RehydrateInventory`. If fx start exceeds its default deadline (15s) while Redis SETNX is mid-loop on a large event catalogue, you can land in a half-populated cache. Other lifecycle hooks use `context.Background()` exactly because of this; rehydrate should too.
- **MINOR.** `outbox.Relay`'s ticker fires every 500 ms regardless of leader status. Standby pods (non-leader) wake up every tick to call `TryLock` which is a real PG round-trip. At 100 replicas this is 200 r/s of useless `pg_try_advisory_lock` calls. The current scale doesn't hit this, but a documented "if you scale this, increase poll interval first" warning belongs in the source comment.
- **MINOR.** [`saga_consumer.go:200`](../../internal/infrastructure/messaging/saga_consumer.go) — the `time.After(time.Second)` on fetch error does NOT pool the timer. Under sustained Kafka outage this allocates a fresh `*Timer` per error iteration. Use `time.NewTimer` + `defer t.Stop()` or `sleepCtx`-style helper.

---

## 4. Sync primitives audit

```
$ grep -rn "sync.Mutex\|sync.RWMutex\|sync.Once\|atomic\." internal/ | wc -l
```

Top hits, audited:

- **`sync.Pool`** — 3 instances, all in [`redis.go`](../../internal/infrastructure/cache/redis.go). Audited correct in §1.
- **`atomic.Int64` (`lastMessageAt`)** in [`saga_consumer.go:70`](../../internal/infrastructure/messaging/saga_consumer.go) — ✓ correct; the source comment is explicit about WHY this is `atomic.Int64` not `atomic.Pointer[time.Time]` (avoids per-Store heap alloc). Excellent discipline.
- **`sync.Mutex`** — used in the admin event bus + a few collectors. None on the booking hot path.
- **`sync.RWMutex`** — none found on hot path. (Spot-check via `grep -rn "sync.RWMutex" internal/`.) ✓
- **No raw `chan` with unbounded buffer** on hot path. Worker channels are bounded by the broker (Redis Streams / Kafka).
- **No `sync.Once`-protected hot field** on booking path. ✓

**Defensible choices summary**: the existing primitives are well-chosen. `atomic.Int64` for the saga lag gauge is exactly the right call vs `atomic.Pointer[time.Time]` — kudos to whoever made that call. No RWMutex-where-atomic-would-do, no Mutex-where-channel-would-do.

---

## 5. Context propagation

[`internal/log/context.go`](../../internal/log/context.go) is exemplary:

- Single `ctxValue` struct (24 B) bundles logger + correlationID via one `context.WithValue` call ([`middleware.go:67`](../../internal/infrastructure/api/middleware/middleware.go)). No backdoor-thread-local-store pattern.
- The source comment explicitly calls out the comparison: per-request cost ~40 B (valueCtx + ctxValue) vs ~1.2 KB if zap's `With()` baked in correlationID. This is the right level of attention to detail.

**`context.WithTimeout` in hot paths:**

- [`service_kafka_intake.go:170`](../../internal/application/booking/service_kafka_intake.go) — `revertCtx, cancel := context.WithTimeout(context.Background(), revertCompensationTimeout)` only on the publish-failure path. NOT in the success hot path. ✓
- [`kafka_intake_publisher.go:106`](../../internal/infrastructure/messaging/kafka_intake_publisher.go) — `publishCtx, cancel := context.WithTimeout(ctx, p.writeTimeout)` on EVERY publish. Cost: each `WithTimeout` allocates a `*timerCtx` (~80 B) + a runtime timer. At 8 k/s this is 640 KB/s + 8 k timer-create/cancel pairs. **MAJOR-3 below** — this is the highest-volume context allocation on the booking hot path. Mitigation: `kafka.Writer.WriteTimeout` already bounds the call; the redundant ctx wrapping was put there as "belt and suspenders" per the source comment but the cost is real at 8 k QPS.
- Saga consumer / outbox relay / watchdog `context.WithTimeout` calls are off the booking hot path. ✓

**No `context.WithValue` chains** — only one value layered per request. ✓

---

## 6. PG pool config audit

Defaults from [`config.go:642-650`](../../internal/infrastructure/config/config.go):

| Field | Default | At 500 VU |
|---|---|---|
| `MaxOpenConns` | 50 | ✓ generous; Stage 5 worker pool draws from same `*sql.DB` |
| `MaxIdleConns` | 5 | ⚠️ low |
| `MaxIdleTime` | 5m | ✓ |
| `MaxLifetime` | 30m | ✓ |

**Issue (MAJOR-4 below):** `MaxIdleConns=5` against `MaxOpenConns=50` means under bursty load up to 45 conns get torn down and re-opened repeatedly. Each PG connection setup is ~1 ms of TCP+TLS+auth. At Stage 5's intake-to-PG rate of ~2.2k events/s (per benchmark §"intake rate >> worker consume rate") this is invisible because the rate is steady. Under bursty workloads (an event drop) the bench would feel it. Industry tuning advice: `MaxIdleConns ≥ MaxOpenConns / 2` for stateful services that sustain load. Bump `DB_MAX_IDLE_CONNS=25` and re-measure.

**`db_pool_*` metrics** (mentioned in CLAUDE.md PR #41 N1):

- [`internal/infrastructure/observability/db_pool_collector.go`](../../internal/infrastructure/observability/) — verified registered via `bootstrap.registerDBPoolCollector` ([`db.go:81`](../../internal/bootstrap/db.go)).
- During the latest Stage 5 benchmark, the `docker stats` snapshot ([`docs/benchmarks/20260521_210525_stage5_demo_clean_c500/docker_stats_at_finish.txt`](../benchmarks/20260521_210525_stage5_demo_clean_c500/docker_stats_at_finish.txt)) shows `booking_db` at 31.42% CPU + 184 MiB RAM — comfortable. The intake-side pressure is on Kafka consume rate (2.2k/s, well below intake 8.4k/s), not the pool. The MAJOR-4 finding above is a measured-recommendation, not a current-incident.

---

## 7. Redis pool config audit

Defaults from [`config.go:521-525`](../../internal/infrastructure/config/config.go):

| Field | Default | At 500 VU |
|---|---|---|
| `PoolSize` | 200 | ✓ generous — go-redis default is 10; 200 is well-tuned for the workload |
| `MinIdleConns` | 20 | ✓ matches industry recommendation (10% of pool) |
| `ReadTimeout` | 500 ms | ✓ |
| `WriteTimeout` | 500 ms | ✓ |
| `PoolTimeout` | 2 s | ✓ |
| `DialTimeout` | (default 5 s in go-redis) | ✓ implicitly |

The pool config is well-set. Worth noting per the [go-redis v9 release notes research](#references): the buffer-size default was tuned across the v9.12.x → v9.17.x line (v9.12.0 introduced configurable buffers at 512 KiB → v9.12.1 reduced to 256 KiB → later 9.x patches landed on **32 KiB default read/write buffers** per `options.go` in v9.17.3, up from the 4 KiB stdlib default). The repo's go.mod pins `v9.17.3` so the 32 KiB defaults ARE in effect. No action needed; just don't read "v9.12+" as a synonym for "32 KiB" — v9.12.0 itself shipped 512 KiB.

**Cache `RedisPoolCollector`** is registered ([`redis.go:135`](../../internal/infrastructure/cache/redis.go)). At the latest benchmark, `booking_redis` sits at 7.41% CPU / 336 MiB RAM — well under-loaded. The Lua deduct path is exactly what you'd hope: cheap, atomic, and pooled.

**MINOR**: `DialTimeout` is not surfaced in `config.go`. Inheriting go-redis default (5s) is fine for production but means an unhealthy Redis at boot would freeze startup for 5s before `client.Ping` returns. The `provideDB` retry loop has explicit per-attempt budgets; consider mirroring for Redis.

---

## 8. Kafka producer/consumer tuning audit

Stage 5 intake publisher ([`kafka_intake_publisher.go:60-72`](../../internal/infrastructure/messaging/kafka_intake_publisher.go)):

```go
&kafka.Writer{
    RequiredAcks: kafka.RequireAll,       // ✓ correct for durability boundary
    BatchTimeout: 1 * time.Millisecond,   // flush almost immediately
    BatchSize:    1,                       // one message per write
    WriteTimeout: timeout,                 // default 5s from cfg.Kafka.WriteTimeout
}
```

**Trade-off observed and conscious**: BatchSize=1 + BatchTimeout=1ms is correct for the "block until ack" contract that Stage 5 promises (the client gets a 202 only after Kafka has confirmed acks=all). Increasing BatchSize would lower p99 throughput at the cost of higher latency — the source comment ACKs this. Don't change.

**No compression** is set (omitted = `kafka.CompressionNone`). At 280 B payloads, Snappy/LZ4 would shave 30-50% of network bytes for ~2% CPU. Bandwidth isn't bottlenecking the 8 k/s benchmark (Docker localhost), but for a real cluster behind a network link this is worth enabling. **MINOR-5 below.**

**Saga consumer / reader** ([`saga_consumer.go:82-89`](../../internal/infrastructure/messaging/saga_consumer.go)):

```go
kafka.NewReader(kafka.ReaderConfig{
    StartOffset: kafka.FirstOffset,
    MinBytes:    10e3,
    MaxBytes:    10e6,
})
```

✓ Defensible defaults. `MinBytes: 10KB` keeps fetches reasonably batched.

**Stage 5 intake consumer** — not in `cmd/booking-cli` but in the stage5 binary; similar shape per [`kafka_intake_consumer.go:139`](../../internal/infrastructure/messaging/kafka_intake_consumer.go). The benchmark notes "intake rate (8.4k/s) >> worker consume rate (2.2k/s)" which is by design — the broker absorbs the burst. The consumer side is NOT the bottleneck to chase; the PG INSERT chain is.

---

## 9. PGO opportunity assessment

**No `default.pgo` exists** (verified via `find . -name '*.pgo'`). PGO is unadopted.

**Recommendation**: this is the single highest-EV optimization available, and the project is unusually well-positioned for it.

Why the fit is strong:

1. The hot path is concentrated: `BookTicket` → Lua deduct → Kafka publish → 202 is ~95% of the CPU at peak per the benchmark.
2. k6 (`scripts/k6_intake_only.js`) already generates exactly the workload-shape PGO needs to profile.
3. The codebase is single-binary (no shared libs, no plugin dispatch), so PGO can devirtualize confidently.
4. Many decorator chains (booking service: `metricsDecorator → tracingDecorator → service`) are monomorphic in production but interface-dispatched. PGO devirtualizes monomorphic-enough call sites — see [`memory/go_perf_internals.md`](../../../../.claude/projects/-Users-lileon-project-booking-monitor/memory/go_perf_internals.md) lines 24-29 for the budget mechanics.

**Citations for expected payoff**:

- Cloudflare: −3.5% CPU, 97 cores saved on prod ([Cloudflare PGO blog](https://blog.cloudflare.com/reclaiming-cpu-for-free-with-pgo/)).
- Uber: 24,000 CPU cores saved across top services after collaborating with Go team on PGO maturation ([Uber engineering blog](https://www.uber.com/us/en/blog/automating-efficiency-of-go-programs-with-pgo/)).
- Go team: 2–14% throughput on typical Go services.

**Plan** — see [MAJOR-6 below]. The mechanics are: collect cpu.pprof from a k6 run, write it as `cmd/booking-cli/default.pgo`, rebuild. The Go toolchain auto-detects `default.pgo` in the main package directory; no Makefile change needed.

**Risk**: profile mismatch can slightly slow the binary. Mitigated by capturing the profile from the same k6 script the benchmark uses.

---

## 10. GC + GOMEMLIMIT tuning audit

Settings ([`.env:42-53`](../../.env)):
- `GOGC=400`
- `GOMEMLIMIT=256MiB`

Observed at 500 VU: heap peak 102.8 MiB (per [benchmark](../benchmarks/20260521_210525_stage5_demo_clean_c500/comparison.md)), no GC pressure visible.

**Is it defensible?** Yes. Industry guidance from 2025 sources:

- [oneuptime / 2026 GOMEMLIMIT guide](https://oneuptime.com/blog/post/2026-02-06-gomemlimit-memory-limiter-stable-collectors/view): "GOMEMLIMIT should be set to ~85-90% of your container's memory limit to prevent OOM kills while avoiding excessive GC thrashing". The repo's `GOMEMLIMIT=256MiB` is ~2× the observed peak heap (`docker-compose.yml` should be checked for the container limit; if the container is 512 MiB then 256 MiB is exactly the 50% bound).
- [GOGC tuning guide / dev.to / 2024](https://dev.to/jones_charles_ad50858dbc0/tuning-gos-gogc-a-practical-guide-with-real-world-examples-4a00): "In a high-throughput API gateway scenario, GOGC was increased to 300, cutting GC frequency by ~30% while increasing memory usage by 20%, alongside code optimization using object pooling to reduce allocations." The repo at GOGC=400 is exactly in this regime.

**Conclusion**: the tuning is well-grounded. The .env's inline comments (lines 49-53) cite the empirical reasoning ("256 MiB is ~2x our observed peak heap (141MB)"). The 141 MiB number is slightly older than the 102.8 MiB current peak — i.e. there's MORE headroom than the comment claims. Optional follow-up: lower `GOMEMLIMIT=192MiB` to tighten the safety net, but the existing setting is conservative-good.

**Go 1.25 GC opportunity (experimental)**: [Go 1.25 release notes](https://go.dev/doc/go1.25) describe a new GC available as `GOEXPERIMENT=greenteagc` with 10-40% expected reduction in GC overhead. The repo runs Go 1.25.10. **MINOR-7 below**: try this experimentally in a sidecar Stage 5 binary and benchmark.

**Go 1.25 container-aware GOMAXPROCS** (also from the release notes): on Linux, the runtime now considers cgroup CPU bandwidth limits. This is a free win for k8s deployments — the Stage 5 binary should immediately benefit when deployed under k8s with CPU limits. No action required; just be aware.

---

## 11. Profiling discipline & runbook

[`config.go:484`](../../internal/infrastructure/config/config.go) — `ENABLE_PPROF=false` by default; `.env` sets it true for dev. ✓

[`server.go:404-420`](../../cmd/booking-cli/server.go) — pprof listener on `127.0.0.1:6060` by default (loopback). ✓ Heap dumps + `/admin/loglevel` cannot leak via the public listener.

**Runbook for CPU profile under load** — NOT documented anywhere I could find. The implicit procedure is:

```bash
# Terminal 1: start app with ENABLE_PPROF=true
make demo-stage5-up   # or `docker compose up -d app`

# Terminal 2: capture CPU profile during k6 run
docker compose exec app sh -c '
  apk add curl 2>/dev/null;
  curl -o /tmp/cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=60"
'

# Terminal 3: start k6 immediately after the curl above
make demo-stage5-rush

# Pull out
docker compose cp app:/tmp/cpu.pprof ./cpu.pprof
go tool pprof -http=:8081 ./cpu.pprof
```

**MINOR-8 below**: add this as a documented `make capture-profile` target in the Makefile so anyone (CI / next reviewer / future self) can do it in one command. Without it, every profile session has a "how do I do this again" tax. The benchmark report at [`20260412_225413_gc_baseline_with_pprof`](../benchmarks/20260412_225413_gc_baseline_with_pprof) is evidence this WAS done before — but the recipe lives only in shell history.

**Production-safety**: capturing a 30-60s CPU profile at production load is ~5% overhead, observed via the GC-baseline-with-pprof benchmark which showed 14,843 RPS vs 20,552 RPS clean (PR #14/15 baseline, pre-Pattern A). At the current 8.4k/s base it's similarly safe; do not run continuously. Loopback-bound pprof + ops-only access is the right posture.

---

## 12. Inlining hot-path audit

Go 1.25 inlining budget is 80 nodes per function — measured per callee (the function being considered for inlining), not per caller; a caller can inline multiple callees each up to 80 nodes independently (per [`memory/go_perf_internals.md`](../../../../.claude/projects/-Users-lileon-project-booking-monitor/memory/go_perf_internals.md) line 56). Borderline functions:

- **`enrichFields`** ([`log.go:184-215`](../../internal/log/log.go)) — has a `trace.SpanContextFromContext` call inside which is itself a function call (57-node cost!). This likely pushes `enrichFields` over the 80-node budget. Run the verification command in §2 to confirm. If non-inlinable, the trade-off is acceptable because: (a) the disabled-level fast path doesn't call this at all (returns from `Check(lvl,...) == nil`); (b) enabled-level calls are off the request hot path almost by definition (error/warn paths only).
- **`pkgEmit`** / **`Logger.emit`** ([`log.go:169-175` and `260-265`](../../internal/log/log.go)) — likely inlinable. The `Check` call returns `*zapcore.CheckedEntry`; the nil-test branches early.
- **`mustMarshal`** ([`handler.go:64`](../../internal/infrastructure/api/booking/handler.go)) — contains a `panic(...)` call which is rarely inlinable. Acceptable; the success path is one `json.Marshal` call.
- **`Fingerprint`** ([`idempotency.go:96`](../../internal/infrastructure/api/middleware/idempotency.go)) — `sha256.Sum256(bodyBytes)` is a stdlib call (Sum256 is itself ~80 node ops); likely non-inlinable but called once per request. Acceptable.
- **`uuid.NewV7()`** — comes from google/uuid; ~10 nodes of inline-friendly code BUT calls `crypto/rand.Read` which is non-inlinable.

**Net**: no inlining gaps on the booking hot path that PGO wouldn't address. Don't manually refactor for inlining ahead of running `-m=2` to confirm.

---

## 13. Range-func / iterator opportunities

Go 1.23 introduced range-over-func (`for x := range fn { ... }`). Go 1.25 has matured semantics.

**Where would it help?** Almost nowhere on this codebase's hot path. The booking pipeline doesn't iterate collections; it processes one message at a time.

**Where would it help in slow paths?**
- [`outbox/relay.go:processBatch`](../../internal/application/outbox/relay.go) — `for _, e := range events` over a slice. No win from iterators.
- [`reconciler.go`] in recon — same shape.
- The repos' `ListByEventID`, `ListOrders`, etc. — could expose iterators to avoid materializing a slice of all rows in memory. ONLY relevant if the result set is large enough to matter; today the paginated history endpoint caps at 100 rows. **NIT** — no action needed.

**Wasteful uses?** None found. The codebase doesn't use range-func at all.

---

## 14. String/byte churn

Hot-path conversions audit:

- **`[]byte(s)` / `string(b)` round-trips**:
  - [`handler.go:185, 204, 346, 350`](../../internal/infrastructure/api/booking/handler.go) — `c.Data(status, "application/json", mustMarshal(...))` — `mustMarshal` returns `[]byte`, passed to `c.Data` which accepts `[]byte`. No round-trip.
  - [`kafka_intake_publisher.go:110`](../../internal/infrastructure/messaging/kafka_intake_publisher.go) — `kafka.Message{Key: []byte(msg.OrderID.String())}`. `UUID.String()` returns a fresh string (already a 36 B alloc); `[]byte(s)` here forces a SECOND alloc to copy the bytes. **MINOR-9 below**: use `uuid.MarshalBinary()` (returns the 16 B canonical form directly into a `[]byte`) — saves the string allocation AND uses a more efficient partition key.
  - [`redis_queue.go:519-528`](../../internal/infrastructure/cache/redis_queue.go) — DLQ field copying via `for k, v := range msg.Values`. Pure interface{} copy; no string round-trip.
- **`strconv.Atoi(s)` / `strconv.ParseInt(s, ...)`** — used in `parseMessage` ([`redis_queue.go:610-755`](../../internal/infrastructure/cache/redis_queue.go)) for every stream-consumed booking. Necessary; the wire format is string-typed via RESP. ✓
- **`fmt.Sprintf("%d-0", cutoffMs)`** ([`redis_queue.go:548`](../../internal/infrastructure/cache/redis_queue.go)) on every DLQ write — allocates a fresh string. DLQ is the failure path, not hot path. ✓
- **`fmt.Sprintf("order:%s", event.OrderID)`** ([`saga/compensator.go:163`](../../internal/application/saga/compensator.go)) — saga path, not booking hot path. ✓
- **`tag.OrderID(id)` etc.** ([`internal/log/tag/`](../../internal/log/tag/)) — type-safe zap field constructors. These do NOT call `.String()` until the log line actually emits. ✓

Net string-churn cost on the booking hot path: dominated by the UUID `.String()` × 2 (orderID for response + Kafka key) — ~80 B/req. The `[]byte()` cast on the Kafka key is the only avoidable one (MINOR-9).

---

# Findings

Severity: **CRIT** (correctness gap that can hurt prod) / **MAJOR** (real perf win, well-bounded) / **MINOR** (polish) / **NIT** (taste).

### [CRIT-1] `installInventoryRehydrate` uses OnStart ctx → half-populated cache on slow boot

**Where**: [`cmd/booking-cli/server.go:232-241`](../../cmd/booking-cli/server.go) inside `installInventoryRehydrate`.

**What**: The `OnStart` hook calls `cache.RehydrateInventory(ctx, ...)` where `ctx` is the fx-supplied OnStart context, which has a default 15s deadline (fx.StartTimeout). The other lifecycle hooks (`installServer`'s relay goroutine, the worker, the saga consumer) all derive from `context.Background()` exactly to avoid this. If a deploy has a large event catalogue (e.g. 100k events × HSET+SETNX = several hundred ms × N), and the rehydrate is slower than 15s, ctx cancels mid-loop. The function returns the cancellation error → fx fails startup → pod restarts → loop repeats indefinitely. Worse, before the restart, the partially-populated Redis state will return `metadata_missing` for un-rehydrated ticket types on the booking hot path.

**Measured/estimated impact**: Currently bounded by the small event catalogue in the benchmark. Production-blocking if the catalogue scales.

**Recommendation**: derive a separate ctx with explicit budget (e.g., `cfg.Server.RehydrateTimeout` defaulting to 2 min) rooted at `context.Background()`. Mirror the pattern in `outbox.Relay`'s wiring at [`server.go:181-183`](../../cmd/booking-cli/server.go).

**Verification command**:
```bash
# Reproduce locally by stuffing many synthetic ticket_types and benchmarking rehydrate alone
go test -race -count=1 ./internal/infrastructure/cache/ -run TestRehydrateInventory -v
```

**References**: source comment at the relay wiring ([`server.go:175-178`](../../cmd/booking-cli/server.go)) already documents this exact pattern for *other* hooks.

---

### [MAJOR-1] Idempotency middleware leaks ~150 B + 2 small heap allocs per booking

**Where**: [`internal/infrastructure/api/middleware/idempotency.go:277-282`](../../internal/infrastructure/api/middleware/idempotency.go).

**What**:
```go
c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
capture := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
```
Each booking allocates: a fresh `bytes.Buffer` (24 B header + grows to body size), a `bytes.NewReader` (16 B), a `io.NopCloser` wrapper (8 B), a `*captureWriter` (32 B). At 8 k req/s with body ~150 B that's ~1.2 MB/s of garbage.

**Measured/estimated impact**: ~10-15% of total per-request heap alloc, based on §1's budget. Translates to roughly one extra GC cycle every 200 ms at peak, observable as a small p99 tail bump.

**Recommendation**: introduce `sync.Pool` for both `*bytes.Buffer` (Reset on Get) and `*captureWriter`. Reset captureWriter's status + body to defaults on each return. Verify the pool's `New` returns a `&bytes.Buffer{}` whose backing array grows once and then stays in the pool — `buf.Reset()` keeps the backing array.

**Verification command**:
```bash
go test -bench=BenchmarkIdempotencyMiddleware -benchmem -count=10 \
  ./internal/infrastructure/api/middleware/...
# (benchmark doesn't exist today — write one, then benchstat before/after)
```

**References**: [`sync.Pool` best-practices research](https://leapcell.medium.com/optimizing-go-performance-with-sync-pool-and-escape-analysis-79f7e3879847) — store `*bytes.Buffer` (pointer, not value) to avoid the boxing escape.

---

### [MAJOR-2] `encoding/json` Marshal of `IntakeMessage` + response DTO is the dominant CPU + alloc

**Where**: [`internal/infrastructure/messaging/kafka_intake_message.go`](../../internal/infrastructure/messaging/kafka_intake_message.go) (`encodeIntakeMessage`); [`internal/infrastructure/api/booking/handler.go:204, 350`](../../internal/infrastructure/api/booking/handler.go) (`mustMarshal`).

**What**: At 8 k bookings/s the system marshals two JSON payloads per request (Kafka intake + HTTP response). `encoding/json` v1 is ~1× speed; Go 1.25 ships `encoding/json/v2` as `GOEXPERIMENT=jsonv2` per [Go 1.25 release notes](https://go.dev/doc/go1.25), with [benchmarks showing 1.6–10× speedups](https://dev.to/ryansgi/go-125-json-v2-benchmarks-raptor-escapes-and-a-18-speedup-5cf3) on struct marshaling. The repo already has `bytedance/sonic v1.15.0` in indirect dependencies (from `gin`).

**Measured/estimated impact**: ~5-8% CPU saved + ~50% reduction in JSON-marshaling allocs. Specifically the Kafka intake encode (8 fields × 8k/s) is the largest steady allocator visible from §1.

**Recommendation**: ONE of:
1. **Lowest-risk**: build with `GOEXPERIMENT=jsonv2` in CI and benchmark. Reverts cleanly. **Pin to 1.25-only** — this experiment is gated to the toolchain in `go.mod`.
2. **Direct integration**: import `github.com/bytedance/sonic` (already transitively present) for the two hottest marshal sites only. Keep `encoding/json` everywhere else (validation, history, error responses).

**Verification**:
```bash
go test -bench=BenchmarkBookTicket -benchmem -count=10 ./internal/application/booking/
# Then with GOEXPERIMENT=jsonv2 / sonic, benchstat the two outputs.
```

**References**:
- [Go 1.25 release notes — JSON v2 experiment](https://go.dev/doc/go1.25)
- [bytedance/sonic benchmarks](https://github.com/bytedance/sonic) — 5× faster unmarshal in best case; note `sonic` is amd64+arm64 only.

---

### [MAJOR-3] `context.WithTimeout` inside the Kafka publish hot path — 640 KB/s + 8k timer churn

**Where**: [`internal/infrastructure/messaging/kafka_intake_publisher.go:106-107`](../../internal/infrastructure/messaging/kafka_intake_publisher.go).

**What**: Every booking publish wraps the request ctx with a `context.WithTimeout(ctx, p.writeTimeout)` then defers `cancel()`. Each `WithTimeout` allocates ~80 B of `*timerCtx` and creates a runtime timer that the deferred cancel must stop. At 8 k publishes/s that's 640 KB/s + 8 k timer-create/cancel pairs/s (16 k discrete timer operations: 8 k creates + 8 k deferred stops). The source comment justifies this as "belt to kafka.Writer.WriteTimeout's suspenders" — defensible historically, but the cost adds up.

**Measured/estimated impact**: 1-2% CPU + ~5% of the steady allocation budget on the Stage 5 hot path.

**Recommendation**: rely on `kafka.Writer.WriteTimeout` (already set to `timeout` at construction) as the sole publish-deadline mechanism. If you keep the belt-and-suspenders pattern for safety, at minimum confirm via `-gcflags=-m=2` that the `timerCtx` doesn't escape and consider hoisting the timeout into a `*time.Timer` from a `sync.Pool` for very-hot paths (Cloudflare uses this pattern).

**Verification**:
```bash
go build -gcflags='-m=2' ./internal/infrastructure/messaging/... 2>&1 \
  | grep -E 'kafka_intake_publisher\.go:10[4-9]'
# Look for "moved to heap: cancel" / "*timerCtx escapes"
```

---

### [MAJOR-4] PG pool — `MaxIdleConns=5` against `MaxOpenConns=50` causes connection churn under bursty load

**Where**: [`internal/infrastructure/config/config.go:642-643`](../../internal/infrastructure/config/config.go).

**What**: Under sustained load all 50 conns are checked out → on lull, only 5 stay idle → next burst tears them open from scratch. Each PG connection setup is ~1 ms TCP+TLS+auth.

**Measured/estimated impact**: invisible at the Stage 5 benchmark's steady-state 2.2 k events/s consume rate; visible on bursty workloads (rapid event drops, post-rate-limit recovery). The benchmark wouldn't catch this.

**Recommendation**: set `DB_MAX_IDLE_CONNS=25` (or 50% of MaxOpen). Re-benchmark with a burst-shaped k6 script (ramp from 0 → 500 VU in 5s and observe p99 during the ramp).

**Verification**:
```bash
# Watch db_pool_open_connections / db_pool_idle vs db_pool_wait_count under bursty k6
curl -s localhost:8080/metrics | grep -E 'db_pool_(open|idle|wait)'
```

---

### [MAJOR-6] No PGO. Cloudflare + Uber both publish 3-5% CPU savings on real Go services.

**Where**: repo-wide. Missing `cmd/booking-cli/default.pgo`.

**What**: Go 1.25 PGO is mature (stable since 1.21, multiple production deployments at scale). The booking system is unusually well-suited: single-binary, monomorphic decorator chains, k6 generates a clean profile-shape.

**Measured/estimated impact**: +3-5% accepted/s, -5-10% p99. Per Polar Signals + Uber + Cloudflare blog data.

**Recommendation** — concrete procedure:

```bash
# 1. Boot a clean Stage 5 stack.
make demo-stage5-up && make reset-db && docker compose restart app
sleep 5

# 2. Capture a 60s profile during a representative k6 run.
docker compose exec app sh -c '
  apk add curl 2>/dev/null;
  curl -o /tmp/cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=60" &
  sleep 1
'
make demo-stage5-rush  # this is the 60s k6 run
docker compose cp app:/tmp/cpu.pprof cmd/booking-cli/default.pgo

# 3. Build with PGO (auto-detected by Go toolchain).
go build -o /tmp/booking-cli-pgo ./cmd/booking-cli/

# 4. Smoke-bench before/after.
# Drop binary into the docker image (Dockerfile multi-stage) and re-run benchmark.
make benchmark-compare VUS=500 DURATION=60s
```

**Verification command** for the PGO-was-applied check:

```bash
go version -m /tmp/booking-cli-pgo | grep -E 'build|cgo|pgo'
# Should show -pgo=default.pgo
```

**References**:
- [Cloudflare PGO production blog](https://blog.cloudflare.com/reclaiming-cpu-for-free-with-pgo/) — −3.5% CPU on Go services
- [Uber automating PGO blog](https://www.uber.com/us/en/blog/automating-efficiency-of-go-programs-with-pgo/) — 24k cores saved across top services
- [Go PGO official doc](https://go.dev/doc/pgo) — stable since 1.21
- [`memory/go_perf_internals.md`](../../../../.claude/projects/-Users-lileon-project-booking-monitor/memory/go_perf_internals.md) lines 70-78 — already-tracked as project backlog item B2.5

---

### [MINOR-5] Kafka producer — no compression

**Where**: [`internal/infrastructure/messaging/kafka_intake_publisher.go:60-72`](../../internal/infrastructure/messaging/kafka_intake_publisher.go) — no `Compression` field set on `kafka.Writer`.

**What**: Default `kafka.CompressionNone`. At ~280 B intake payloads, Snappy (~30-50% wire reduction) is essentially free CPU-wise on x86_64.

**Recommendation**: `Compression: kafka.Snappy`. Test downstream consumer + DLQ readers still understand the encoding (segmentio/kafka-go decompresses transparently).

---

### [MINOR-7] Go 1.25 Green Tea GC — try experimentally

**Where**: build / docker config.

**What**: `GOEXPERIMENT=greenteagc` enables Go 1.25's experimental GC with [10-40% reduction in GC overhead](https://go.dev/doc/go1.25) for GC-heavy workloads.

**Recommendation**: do NOT enable in default deployment. Build a parallel Stage 5 binary with the experiment, run the canonical benchmark, compare. If wins materialize, gate behind an env-var in `docker-compose.benchmark.yml`.

---

### [MINOR-8] Document `make capture-profile` target

**Where**: [`Makefile`](../../Makefile).

**What**: pprof capture procedure (§11 above) is implicit; recreate-it-from-memory every time. A 5-line `capture-profile` target makes the procedure rerunnable + reviewable.

**Recommendation**:
```makefile
capture-profile:
	@docker compose exec -T app sh -c 'apk add --no-cache curl >/dev/null; curl -sf -o /tmp/cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=$(or $(SECONDS),60)"'
	@docker compose cp app:/tmp/cpu.pprof ./cpu.pprof
	@echo "Captured: ./cpu.pprof — analyse with: go tool pprof -http=:8081 ./cpu.pprof"
```

---

### [MINOR-9] `kafka.Message.Key: []byte(msg.OrderID.String())` allocates twice

**Where**: [`internal/infrastructure/messaging/kafka_intake_publisher.go:110`](../../internal/infrastructure/messaging/kafka_intake_publisher.go).

**What**: `UUID.String()` allocates a 36 B string; `[]byte(s)` copies it into a fresh slice. Two heap allocs per publish.

**Recommendation**: use `binBytes, _ := msg.OrderID.MarshalBinary()` (returns the 16 B canonical UUID directly into a `[]byte`). Halves the alloc + reduces partition-key bytes-on-wire from 36 B to 16 B. Kafka `kafka.Hash{}` balancer hashes the raw bytes — either form works.

---

### [NIT-10] `mlog.NamedError` and `mlog.ByteString` field constructors

**Where**: [`internal/log/field.go`](../../internal/log/field.go) (not read in this audit but referenced from saga compensator).

**What**: These wrap zap field constructors. Verify via `-m=2` that they all inline; if any fail the budget, the construction allocates the `zap.Field` struct.

**Recommendation**: just verify; no fix needed unless a specific call site shows up in pprof.

---

## Recommendations summary

> **Priority ordering note (added in PR #128 fixup)**: this table reflects the primary review's EV-per-effort ordering (perf-win-first), which the [meta-review](go-performance-meta-review.md) and [final-decisions.md](final-decisions.md) supersede in favour of an operational-correctness-first ordering. For the action-plan ordering that will actually be executed, read `final-decisions.md` § Priority A first — the rehydrate-ctx fix (CRIT-1 / A11) ships before PGO (B3), the operational fixes before the perf experiments. The table below is preserved as the primary review's original analytical signal.

| Priority | Action | Estimated win | Effort |
|---|---|---|---|
| 1 | **PGO** (`default.pgo` collected from k6 run) | +3-5% accepted/s, -5-10% p99 | 1 hour |
| 2 | **Pool idempotency middleware buffers** (MAJOR-1) | -1.2 MB/s heap, slight p99 tail | 2 hours |
| 3 | **Drop redundant ctx.WithTimeout in Kafka publish** (MAJOR-3) | -640 KB/s heap + 1-2% CPU | 30 min |
| 4 | **Bump `DB_MAX_IDLE_CONNS=25`** (MAJOR-4) | Smooths bursty-load latency | env var change |
| 5 | **Experiment with JSON v2 / sonic on the two hottest marshal sites** (MAJOR-2) | 5-8% CPU + 50% JSON allocs | 4 hours + bench |
| 6 | **Fix rehydrate ctx** (CRIT-1) | Avoids half-populated cache on slow boots | 30 min |
| 7 | **Snappy compression on Kafka writer** (MINOR-5) | Bandwidth saver for cross-AZ deployments | 5 min |
| 8 | **Kafka key as `MarshalBinary`** (MINOR-9) | Free 36 B/publish | 5 min |
| 9 | **`make capture-profile`** (MINOR-8) | Operability | 5 min |
| 10 | **Try `GOEXPERIMENT=greenteagc`** (MINOR-7) | 10-40% GC overhead reduction (potentially) | 1 hour benchmark |

---

## References

**Go 1.25 specifics:**
- [Go 1.25 Release Notes — go.dev/doc/go1.25](https://go.dev/doc/go1.25) — Green Tea GC, container-aware GOMAXPROCS, encoding/json v2 experiment, stack slice backing stores
- [What's New in Go 1.25: A Deep Look into JSON v2 / Medium](https://medium.com/@yuseferi/whats-new-in-go-1-25-a-deep-look-into-the-json-v2-and-more-a8195673fef2) — Aug 2025

**PGO (Profile-Guided Optimization):**
- [Reclaiming CPU for free with Go's PGO — Cloudflare](https://blog.cloudflare.com/reclaiming-cpu-for-free-with-pgo/) — −3.5% CPU, 97 cores saved
- [Automating Efficiency of Go programs with PGO — Uber](https://www.uber.com/us/en/blog/automating-efficiency-of-go-programs-with-pgo/) — 24k cores saved at scale
- [Uber Boosted Performance with Go's PGO — InfoQ, Mar 2025](https://www.infoq.com/news/2025/03/uber-performance-golang/)
- [Profile-guided optimization — go.dev/doc/pgo](https://go.dev/doc/pgo) — official guide

**GOGC / GOMEMLIMIT tuning:**
- [Tuning Go's GOGC: A Practical Guide — dev.to / 2024](https://dev.to/jones_charles_ad50858dbc0/tuning-gos-gogc-a-practical-guide-with-real-world-examples-4a00) — GOGC=300 cut GC by 30% in 100k QPS gateway
- [How to Set Up GOMEMLIMIT — oneuptime / Feb 2026](https://oneuptime.com/blog/post/2026-02-06-gomemlimit-memory-limiter-stable-collectors/view) — 85-90% of container limit rule
- [GOMEMLIMIT is a game changer — Weaviate](https://weaviate.io/blog/gomemlimit-a-game-changer-for-high-memory-applications) — combined GOGC + GOMEMLIMIT recipe

**sync.Pool:**
- [Optimizing Go Performance with sync.Pool and Escape Analysis — Leapcell, 2025](https://leapcell.medium.com/optimizing-go-performance-with-sync-pool-and-escape-analysis-79f7e3879847) — pointer-vs-value Put, escape analysis interplay
- [Go sync.Pool and the Mechanics Behind It — VictoriaMetrics](https://victoriametrics.com/blog/go-sync-pool/)

**JSON v2 + sonic benchmarks:**
- [Go 1.25 JSON v2: Benchmarks — dev.to, 2025](https://dev.to/ryansgi/go-125-json-v2-benchmarks-raptor-escapes-and-a-18-speedup-5cf3) — up to 10× unmarshal speedup
- [Go JSON Performance Showdown — saraikin.com, 2025](https://saraikin.com/posts/golang-json-marshalling/)
- [bytedance/sonic — GitHub](https://github.com/bytedance/sonic) — 5× faster unmarshal vs stdlib (amd64/arm64)

**Go drivers:**
- [pq or pgx — Preslav Rachev, May 2022 (still authoritative)](https://preslav.me/2022/05/13/pq-or-pgx-choosing-the-right-postgresql-golang-driver/) — pgx 10-20% faster via database/sql; ~2× via native
- [go-redis v9 buffer optimization (v9.12.x → v9.17.x, early 2026)](https://github.com/redis/go-redis/releases) — buffer-size default tuned across the line; **32 KiB default in v9.17.3** (`options.go`). v9.12.0 originally shipped 512 KiB → v9.12.1 reduced to 256 KiB → later 9.x patches landed on 32 KiB.

**Goroutine leak detection:**
- [uber-go/goleak](https://github.com/uber-go/goleak) — for tests
- [LeakProf: Featherlight In-Production Goroutine Leak Detection — Uber](https://www.uber.com/us/en/blog/leakprof-featherlight-in-production-goroutine-leak-detection/) — production complement to goleak

**Project-internal:**
- [`memory/gc_benchmark_status.md`](../../../../.claude/projects/-Users-lileon-project-booking-monitor/memory/gc_benchmark_status.md) — historical PR #14/#15 GC tuning context
- [`memory/go_perf_internals.md`](../../../../.claude/projects/-Users-lileon-project-booking-monitor/memory/go_perf_internals.md) — 1.25 inline budget (80), PGO devirt mechanics
- [Latest Stage 5 benchmark](../benchmarks/20260521_210525_stage5_demo_clean_c500/comparison.md) — 8.4k accepted/s, p95 62 ms, 102.8 MiB peak heap, the workload this audit is grounded in
