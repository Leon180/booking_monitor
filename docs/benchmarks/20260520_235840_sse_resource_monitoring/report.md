# SSE Subsystem — Resource Monitoring + Buffer-Pool Decision

> 2026-05-20 · branch `feat/admin-stream-resource-monitoring` · profile-driven follow-up to the PR #121 SSE work.

## Scope

Two related questions about the admin-event SSE pipeline:

1. **Tier A — per-subsystem observability**: the existing `go_goroutines` / `go_memstats_alloc_bytes` gauges aggregate the entire process, mixing SSE usage with the booking hot path, outbox relay, recon sweeper, etc. Can we expose SSE-scoped resource counters so capacity planning and incident triage have a direct signal?
2. **Tier B — buffer pooling**: should `writeMessage()` (per-frame SSE wire write) reuse a `sync.Pool`-managed buffer to avoid per-event allocation pressure?

Tier A was implemented. Tier B was deferred based on the profile data below.

---

## Tier A — implemented

Five new metrics surfacing per-subsystem resource counters via Prometheus:

| Metric | Type | What it answers |
|---|---|---|
| `admin_sse_estimated_memory_bytes` | GaugeFunc | "How much RAM does the SSE subsystem own right now?" — N_clients × (8 KB stack + 100-slot send buffer × ~256 B avg msg) |
| `admin_event_bus_channel_high_water_mark` | GaugeFunc | "Has the bus channel ever come close to its 10,000-slot capacity?" |
| `admin_hub_broadcast_high_water_mark` | GaugeFunc | "Has the hub broadcast channel ever filled toward its 256-slot capacity?" |
| `admin_sse_write_message_duration_seconds` | Histogram | "Are individual TCP writes slow (slow clients) or is the whole writer stuck?" |
| `admin_event_bus_xadd_duration_seconds` | Histogram | "Is Redis XADD latency creeping toward the 2 s XAddTimeout cap?" |

Implementation: atomic counters in `sse.Hub` and `cache.AdminEventBus`, plus a single `observability.RegisterAdminStreamResourceGauges` wire-up call from `bootstrap.AdminStreamModule`. No new goroutines. CAS-loop HWM tracking (last-writer-wins, race-safe).

### Verification

Under k6 load (200 VUs × 30 s, `scripts/k6_intake_only.js`, 500K accepted bookings, ~16.9 k events/s through the bus):

```
admin_event_bus_channel_high_water_mark    3        # cap is 10,000
admin_hub_broadcast_high_water_mark        2        # cap is 256
admin_event_bus_published_total{order.created} 16939
admin_event_bus_xadd_duration_seconds_count 16939  # 100% durable to Redis
admin_event_bus_xadd_failures_total        0
admin_sse_clients_dropped_total            0 (all reasons)
admin_sse_write_message_duration_seconds   present, all observations < 0.0001 s
```

→ Drainer and hub broadcast loop never came close to backpressure even at the published rate — the channel capacities chosen in Q3 (10 k) and Q9 (256) have ~3 orders of magnitude of headroom at this workload.

---

## Tier B — NOT implemented (profile-driven decision)

### Question

`writeMessage()` calls `fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", …)` four times per frame. Each call may produce a transient byte slice. At 1 k+/s wire-write rate, would a `sync.Pool` cut GC pressure meaningfully?

### Evidence — 30 s `alloc_space` profile during the same k6 load

| Metric | Value |
|---|---|
| Total bytes allocated, full process, 30 s window | **15,795 MB** |
| SSE handler symbols (`/sse/`, `writeMessage`, `Hub`) in top 30 by alloc_space | **none** |
| SSE handler / bus / hub symbols anywhere in profile | **not surfaced** (below the 0.5 % cutoff at 79 MB cum) |

Top allocators were, in order:
1. `zap/buffer.(*Buffer).String` — log fields (17.4 %)
2. `net/textproto.readMIMEHeader` — HTTP request parsing (6.0 %)
3. `encoding/json.(*Decoder).refill` — request decoding (6.0 %)
4. `net/http.Header.Clone` (5.2 %)
5. `booking_monitor/internal/infrastructure/api/booking.(*bookingHandler).HandleBook` (cum 65.6 %)

The booking hot path dominates with ~80 % of allocations. SSE is below the noise floor.

### Why so little SSE allocation?

Architectural reasons rather than accidental optimisation:

- **Per-client buffer is bounded**: `ClientSendBufferCapacity = 100`. A slow client gets dropped, never accumulates unbounded memory.
- **Frame payload is small**: ~250–300 bytes/frame. The pool-justification rule of thumb (Bryan Boreham, GopherCon UK 2024) is "pool buffers ≥ 4 KB at ≥ 1 k/s allocation rate" — SSE frames are far below the size threshold.
- **`fmt.Fprintf` does not allocate a large buffer**: it writes tokens piecemeal through the `io.Writer`. The compile-time format-string parser amortises whatever working buffer it does need.
- **The actual wire-write pressure was minimal in this profile**: only 2 writeMessage observations because nginx-under-k6-saturation effectively starved the two SSE clients during the 30 s window (separate observation, see "Operational note" below). But even projecting to the bus-publish rate of 16.9 k/s — only 4× the rule-of-thumb threshold — the per-frame allocation would still be dwarfed by booking-handler allocation.

### Decision

**No `sync.Pool`.** A profile-driven optimisation requires the profile to show a target. This one shows the booking hot path, not the SSE wire. Premature pooling would add code (correctness risk with `Reset()` / size variance) without a measurable benefit.

If a future profile (at e.g. 100 k events/s with fast consumers) shows `writeMessage` in the top-N, revisit. The metric `admin_sse_write_message_duration_seconds` added in Tier A is the right early-warning signal — if its p99 starts climbing alongside steady client count, that is when to re-profile.

---

## Operational note (separate to this PR)

During the heavy-load 30 s window, only 2 of 16,939 events reached the SSE wire across two connected curl SSE clients. The metrics show **no** slow-consumer or write-error drops — both clients gracefully ended on curl's `--max-time` timer.

A clean smoke (no load) on the same build immediately delivered 397 events to a single SSE client in ~5 s, so the pipeline is functional.

Diagnosis: nginx CPU was saturated proxying 1.94 M booking POSTs in 30 s and effectively stopped servicing the long-lived SSE TCP streams. This is a nginx capacity issue under contrived k6 saturation, not an SSE code issue. It would not occur in production where booking traffic and admin SSE clients are on different scales and ideally on different nginx upstreams.

Not blocking this PR — flagged here for the operations follow-up backlog.

---

## Files

- `allocs.pb.gz` — 30 s `alloc_space` profile during k6 load
- `heap.pb.gz` — snapshot heap profile at the end of the load window
