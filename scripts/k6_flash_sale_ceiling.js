// k6_flash_sale_ceiling.js — push open-model arrival rate to find break point.
//
// Difference from k6_flash_sale_realistic.js:
//   - Single 5M-ticket pool so the ENTIRE 5-min run is in accept phase
//   - Step-up arrival rate ladder: 10k → 20k → 40k → 60k → 80k
//   - Watch for: 5xx onset, accept-rate plateau, p95 hockey-stick
//
// Output: per-stage metrics show where the system starts to degrade.
//
// Usage:
//   docker run --rm -i --network=booking_monitor_default \
//       -v $(pwd)/scripts/k6_flash_sale_ceiling.js:/script.js \
//       grafana/k6 run /script.js

import http from 'k6/http';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import exec from 'k6/execution';

export const acceptedBookings = new Counter('flash_accepted_bookings');
export const soldOutResponses = new Counter('flash_sold_out_responses');
export const failureResponses = new Counter('flash_failure_responses');
export const bookingLatency   = new Trend('flash_booking_latency_ms');

export const options = {
    scenarios: {
        ceiling_hunt: {
            executor: 'ramping-arrival-rate',
            startRate: 1000,
            timeUnit: '1s',
            preAllocatedVUs: 5000,
            maxVUs: 30000,
            stages: [
                // Stage 1 — calibrate at 10k (above our previous 8k LARGE test)
                { duration: '15s', target: 10000 },
                { duration: '30s', target: 10000 },
                // Stage 2 — 20k
                { duration: '10s', target: 20000 },
                { duration: '30s', target: 20000 },
                // Stage 3 — 40k
                { duration: '10s', target: 40000 },
                { duration: '30s', target: 40000 },
                // Stage 4 — 60k
                { duration: '10s', target: 60000 },
                { duration: '30s', target: 60000 },
                // Stage 5 — 80k (closed-loop ceiling was 59k; this overshoots)
                { duration: '10s', target: 80000 },
                { duration: '30s', target: 80000 },
                { duration: '5s',  target: 0 },
            ],
            gracefulStop: '30s',
        },
    },
    thresholds: {
        // No threshold — just observe
    },
};

const BASE_URL = 'http://app:8080/api/v1';

export function setup() {
    const eventName = `Ceiling Hunt ${Date.now()}`;
    const r = http.post(
        `${BASE_URL}/events`,
        JSON.stringify({
            name: eventName,
            total_tickets: 5_000_000,  // big enough to never deplete in 5 min @ 80k/s
            price_cents: 2000,
            currency: 'usd',
        }),
        { headers: { 'Content-Type': 'application/json' } },
    );
    check(r, { 'setup event created': (res) => res.status === 201 });
    if (r.status !== 201) throw new Error(`setup failed: ${r.status} ${r.body}`);
    return { ticketTypeID: r.json().ticket_types[0].id, startTime: Date.now() };
}

export default function (data) {
    const uid = 1_000_000 + exec.scenario.iterationInTest;
    const t0 = Date.now();
    const res = http.post(
        `${BASE_URL}/book`,
        JSON.stringify({ user_id: uid, ticket_type_id: data.ticketTypeID, quantity: 1 }),
        { headers: { 'Content-Type': 'application/json' }, timeout: '15s' },
    );
    bookingLatency.add(Date.now() - t0);

    if (res.status === 202) {
        acceptedBookings.add(1);
    } else if (res.status === 409) {
        soldOutResponses.add(1);
    } else if (res.status >= 500 || res.status === 0) {
        failureResponses.add(1);
    }
}
