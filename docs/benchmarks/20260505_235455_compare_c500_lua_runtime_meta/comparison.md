# Benchmark Comparison Report

**Date**: 2026-05-06 (Asia/Taipei)  
**Baseline**: `main` at `04362f2` (`feat(cache): read-through Redis cache for TicketTypeRepository.GetByID — recovers PR #89's -40% RPS sold-out path (#90)`)  
**Candidate**: clean worktree off `main` with Lua runtime metadata hot-path optimization (`ticket_type_meta:{id}` + `ticket_type_qty:{id}`)

## Test Conditions

| Setting | Value |
| :--- | :--- |
| Script | `scripts/k6_comparison.js` |
| VUs | 500 |
| Duration | 60s |
| Ticket pool | 500,000 |
| Target | `http://app:8080/api/v1` (direct Docker network path) |
| Idempotency | disabled |

## Results

| Metric | Baseline (`main`) | Candidate (`lua runtime metadata`) | Delta |
| :--- | ---: | ---: | ---: |
| HTTP throughput (`http_reqs/s`) | 39,258.698877/s | 44,491.373953/s | +13.33% |
| Accepted bookings (`accepted_bookings/s`) | 8,332.256727/s | 8,330.909046/s | -0.02% |
| p95 latency (`http_req_duration`) | 22.32ms | 15.71ms | -29.61% |
| Business errors | 0.00% | 0.00% | flat |

## Interpretation

The candidate keeps the accepted-booking ceiling essentially unchanged while materially improving total API throughput and p95 latency. That is the shape we wanted from this change:

- the sold-out / rejected traffic gets cheaper at the HTTP edge
- the booking hot path no longer pays a synchronous Postgres-backed `GetByID` lookup on every accepted reservation
- the accepted-bookings/s ceiling stays bound by downstream async work rather than regressing at the reservation gate

In short: this is a real hot-path win, not just noise-level variance.

## Ticket Conservation Verification

The conservation check was run **after clearing the benchmark-generated Redis stream backlog**, because a post-benchmark queue lag would otherwise prove only that the worker had not reached the synthetic validation event yet.

### Clean validation setup

1. Stop `booking_app`
2. `FLUSHALL` Redis with producers/workers stopped
3. Start `booking_app` and let runtime rehydrate rebuild `ticket_type_meta:*`, `ticket_type_qty:*`, and `orders:group`
4. Create a fresh 100-ticket event
5. Send 120 booking attempts with unique `user_id`s against the new `ticket_type_id`

### Observed result

| Check | Value |
| :--- | :--- |
| `event_id` | `019df8ea-2a66-7a11-a07d-6b0c3938deaa` |
| `ticket_type_id` | `019df8ea-2a66-7a12-a005-cd5530dba900` |
| Accepted (`202`) | `100` |
| Sold out (`409`) | `20` |
| Unexpected statuses | `0` |
| Persisted orders for `ticket_type_id` | `100` |
| Postgres `event_ticket_types.available_tickets` | `0` |
| Redis `ticket_type_qty:{id}` | `0` |
| Order statuses | `awaiting_payment,100` |

### Invariant

`accepted = total_tickets - remaining_qty`

`100 = 100 - 0`

Both Redis and Postgres converged to the same remaining inventory, and all 100 accepted reservations were persisted as `awaiting_payment`. This verifies that the new runtime-metadata path preserves inventory conservation on a clean queue.

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
