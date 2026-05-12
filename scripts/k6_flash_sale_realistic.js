// k6_flash_sale_realistic.js — open-model arrival-rate benchmark for booking_monitor
//
// Why this script exists:
//   The existing k6_load.js / k6_comparison.js use `constant-vus`, which is
//   a CLOSED loop: each VU waits for its response before firing the next
//   request. When the system slows, VUs naturally throttle themselves —
//   reducing pressure on the system. This is the "coordinated omission"
//   artifact (Tene 2013, ScyllaDB 2021).
//
//   Real flash-sale traffic is OPEN: users refresh, retry, and pile on
//   regardless of how slow your server gets. To simulate that honestly,
//   we use k6's `ramping-arrival-rate` executor, which fires new
//   iterations at a configured rate *independent of system response time*.
//
//   Industry references for this methodology:
//     - https://grafana.com/docs/k6/latest/scenarios/concepts/open-vs-closed/
//     - https://shopify.engineering/performance-testing-shopify (Shopify HHH)
//     - https://stormforge.io/blog/open-closed-workloads/
//
// Each iteration = ONE unique user, ONE booking attempt. No looping per VU.
// Retry semantics match real-user behaviour:
//   - 202 → success, exit
//   - 409 → sold out, exit (do not retry — business correct, real users
//                          who refresh and re-book hit the same gate)
//   - 5xx → retry once with 500ms backoff (real users F5 on errors)
//
// Scenarios (set via SCENARIO env var, default MEDIUM):
//   SMALL  —  5,000 tickets,  ramp 0→500 req/s over 5s,  sustain 25s
//   MEDIUM — 50,000 tickets,  ramp 0→2500 req/s over 10s, sustain 50s
//   LARGE — 500,000 tickets, ramp 0→8000 req/s over 15s, sustain 105s
//
// Output metrics that matter for flash-sale interpretation:
//   - accepted_bookings   (only 202; the real business outcome)
//   - sold_out_responses  (409; demand exceeded supply)
//   - failures            (5xx; system trouble)
//   - retries             (5xx retry count)
//   - time-to-first-sold-out (logged via console)
//
// Run: docker run --rm -i --network=booking_monitor_default \
//        -e SCENARIO=MEDIUM \
//        -v $(pwd)/scripts/k6_flash_sale_realistic.js:/script.js \
//        grafana/k6 run /script.js

import http from 'k6/http';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import exec from 'k6/execution';

// ---------- Scenario presets ----------
const SCENARIO = (__ENV.SCENARIO || 'MEDIUM').toUpperCase();
// WITH_IDEMPOTENCY=true → send a unique Idempotency-Key per iteration,
// exercising the middleware's full 3-op Redis path (Get → handler → Set).
// Default false matches our other benchmarks (intake-only, no middleware ops).
const WITH_IDEMPOTENCY = (__ENV.WITH_IDEMPOTENCY || 'false').toLowerCase() === 'true';

const PRESETS = {
    SMALL: {
        totalTickets: 5000,
        stages: [
            { duration: '5s',  target: 500 },   // ramp 0 → 500 req/s
            { duration: '25s', target: 500 },   // sustain
            { duration: '5s',  target: 0 },     // ramp down
        ],
        preAllocatedVUs: 1000,
        maxVUs: 5000,
    },
    MEDIUM: {
        totalTickets: 50000,
        stages: [
            { duration: '10s', target: 2500 },  // ramp 0 → 2500 req/s
            { duration: '50s', target: 2500 },  // sustain
            { duration: '10s', target: 0 },
        ],
        preAllocatedVUs: 3000,
        maxVUs: 12000,
    },
    LARGE: {
        totalTickets: 500000,
        stages: [
            { duration: '15s',  target: 8000 }, // ramp 0 → 8000 req/s
            { duration: '105s', target: 8000 }, // sustain — push past expected 6k cap
            { duration: '15s',  target: 0 },
        ],
        preAllocatedVUs: 10000,
        maxVUs: 40000,
    },
};

const preset = PRESETS[SCENARIO];
if (!preset) {
    throw new Error(`Unknown SCENARIO=${SCENARIO}; pick SMALL / MEDIUM / LARGE`);
}

// ---------- Custom metrics ----------
export const acceptedBookings    = new Counter('flash_accepted_bookings');
export const soldOutResponses    = new Counter('flash_sold_out_responses');
export const failureResponses    = new Counter('flash_failure_responses');
export const retriesAttempted    = new Counter('flash_retries_attempted');
export const bookingLatency      = new Trend('flash_booking_latency_ms');
export const successUserLatency  = new Trend('flash_success_user_latency_ms');

// ---------- k6 options ----------
export const options = {
    scenarios: {
        flash_sale: {
            executor: 'ramping-arrival-rate',
            startRate: 0,
            timeUnit: '1s',
            preAllocatedVUs: preset.preAllocatedVUs,
            maxVUs: preset.maxVUs,
            stages: preset.stages,
            gracefulStop: '30s',
        },
    },
    thresholds: {
        // Allow high failure rate by default — 409 (sold out) is expected
        // business state during a flash sale. We just record + observe.
        // Real production would set per-endpoint SLOs separately.
        flash_failure_responses: ['count<1000'],  // 5xx should be rare
    },
};

const BASE_URL = 'http://app:8080/api/v1';

// ---------- Setup: create event for this test run ----------
export function setup() {
    const eventName = `Flash Sale ${SCENARIO} ${Date.now()}`;
    const r = http.post(
        `${BASE_URL}/events`,
        JSON.stringify({
            name: eventName,
            total_tickets: preset.totalTickets,
            price_cents: 2000,
            currency: 'usd',
        }),
        { headers: { 'Content-Type': 'application/json' } },
    );
    check(r, { 'setup event created': (res) => res.status === 201 });
    if (r.status !== 201) {
        throw new Error(`event creation failed: ${r.status} ${r.body}`);
    }
    const body = r.json();
    const ticketTypeID = body.ticket_types[0].id;
    console.log(`[setup] scenario=${SCENARIO} tickets=${preset.totalTickets} ticket_type=${ticketTypeID}`);
    return {
        ticketTypeID,
        startTime: Date.now(),
        scenario: SCENARIO,
        totalTickets: preset.totalTickets,
    };
}

// ---------- Per-iteration: ONE unique user, ONE attempt ----------
let firstSoldOutLogged = false;

export default function (data) {
    // Each iteration represents ONE unique user. We derive a user_id
    // from the global iteration number so no two iterations share a
    // user_id. exec.scenario.iterationInTest is monotonic across the
    // whole run, regardless of VU pool reuse.
    const uid = 1_000_000 + exec.scenario.iterationInTest;

    const payload = JSON.stringify({
        user_id: uid,
        ticket_type_id: data.ticketTypeID,
        quantity: 1,
    });
    const headers = { 'Content-Type': 'application/json' };
    if (WITH_IDEMPOTENCY) {
        // Per-iteration unique key — simulates first-time production
        // requests (no retry path). Each request exercises the full
        // 3-op idempotency middleware path: Get → handler → Set.
        headers['Idempotency-Key'] = `bench-${uid}-${Date.now()}`;
    }
    const params = {
        headers,
        // Set timeout so a hung request doesn't tie up the VU pool.
        timeout: '15s',
    };

    const t0 = Date.now();
    let res = http.post(`${BASE_URL}/book`, payload, params);
    bookingLatency.add(Date.now() - t0);

    // Categorise response. 202 = booking accepted (Pattern A reservation).
    // 409 = sold out OR idempotency conflict (sold out dominates in flash).
    // 5xx = real system trouble — retry once.
    if (res.status === 202) {
        acceptedBookings.add(1);
        successUserLatency.add(Date.now() - t0);
    } else if (res.status === 409) {
        soldOutResponses.add(1);
        if (!firstSoldOutLogged) {
            firstSoldOutLogged = true;
            const elapsedSec = ((Date.now() - data.startTime) / 1000).toFixed(2);
            console.log(`[first-sold-out] elapsed=${elapsedSec}s scenario=${data.scenario}`);
        }
    } else if (res.status >= 500) {
        failureResponses.add(1);
        // Retry once with small backoff — real users do F5 on errors.
        retriesAttempted.add(1);
        const t1 = Date.now();
        res = http.post(`${BASE_URL}/book`, payload, params);
        bookingLatency.add(Date.now() - t1);
        if (res.status === 202) {
            acceptedBookings.add(1);
            successUserLatency.add(Date.now() - t0);
        } else if (res.status >= 500) {
            failureResponses.add(1);
        }
    }
    // No sleep, no loop — VU exits immediately. Open-model design.
}

// ---------- Teardown summary ----------
export function teardown(data) {
    const elapsed = ((Date.now() - data.startTime) / 1000).toFixed(1);
    console.log(`[teardown] scenario=${data.scenario} pool=${data.totalTickets} wallclock=${elapsed}s`);
}
