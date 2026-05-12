// k6_flash_sale_ceiling_idem.js — find Path B ceiling (with idempotency middleware).
//
// Path A (k6_flash_sale_ceiling.js) hit a cliff between 20k → 40k arrival rate.
// Path B exercises the full 3-op middleware path: Get → handler → Set.
// Expected to break earlier due to:
//   - 2× extra Redis round-trips per request
//   - Larger idempotency cache entries (full response body + 24h TTL)
//
// Ladder: 2k → 5k → 10k → 15k → 20k req/s (smaller increments than Path A
// because Path B per-request cost is higher).
//
// Usage:
//   docker run --rm -i --network=booking_monitor_default \
//       -v $(pwd)/scripts/k6_flash_sale_ceiling_idem.js:/script.js \
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
        ceiling_idem: {
            executor: 'ramping-arrival-rate',
            startRate: 500,
            timeUnit: '1s',
            preAllocatedVUs: 5000,
            maxVUs: 30000,
            stages: [
                { duration: '10s', target: 2000 },
                { duration: '30s', target: 2000 },
                { duration: '10s', target: 5000 },
                { duration: '30s', target: 5000 },
                { duration: '10s', target: 10000 },
                { duration: '30s', target: 10000 },
                { duration: '10s', target: 15000 },
                { duration: '30s', target: 15000 },
                { duration: '10s', target: 20000 },
                { duration: '30s', target: 20000 },
                { duration: '5s',  target: 0 },
            ],
            gracefulStop: '30s',
        },
    },
};

const BASE_URL = 'http://app:8080/api/v1';

export function setup() {
    const eventName = `Ceiling Idem ${Date.now()}`;
    const r = http.post(
        `${BASE_URL}/events`,
        JSON.stringify({
            name: eventName,
            total_tickets: 3_000_000,
            price_cents: 2000,
            currency: 'usd',
        }),
        { headers: { 'Content-Type': 'application/json' } },
    );
    if (r.status !== 201) throw new Error(`setup failed: ${r.status} ${r.body}`);
    return { ticketTypeID: r.json().ticket_types[0].id };
}

export default function (data) {
    const uid = 1_000_000 + exec.scenario.iterationInTest;
    const headers = {
        'Content-Type': 'application/json',
        'Idempotency-Key': `bench-idem-${uid}-${Date.now()}`,
    };
    const t0 = Date.now();
    const res = http.post(
        `${BASE_URL}/book`,
        JSON.stringify({ user_id: uid, ticket_type_id: data.ticketTypeID, quantity: 1 }),
        { headers, timeout: '15s' },
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
