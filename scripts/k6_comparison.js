import http from 'k6/http';
import { check } from 'k6';
import { Rate, Trend } from 'k6/metrics';

export const businessErrors = new Rate('business_errors');
export const bookingDuration = new Trend('booking_duration', true);

function randomIntBetween(min, max) {
    return Math.floor(Math.random() * (max - min + 1) + min);
}

export const options = {
    scenarios: {
        booking_stress: {
            executor: 'constant-vus',
            vus: __ENV.VUS || 500,
            duration: __ENV.DURATION || '60s',
        },
    },
    thresholds: {
        business_errors: ['rate<0.01'],
        http_req_duration: ['p(95)<500'],
    },
};

const BASE_URL = 'http://app:8080/api/v1';

export function setup() {
    // total_tickets sized so the pool does NOT deplete during the
    // standard 60s / 500-VU window. Sustained RPS in this stack is
    // 40-50k; over 60s that's 2.4M-3.0M iterations. 5,000,000
    // tickets gives ~2x headroom so the benchmark measures the
    // accepted-booking hot path (POST /api/v1/book → 202) rather
    // than the cheap sold-out fast path (409 short-circuit).
    //
    // Pre-2026-05-02 runs used 500,000 which depleted within ~10s
    // and produced headline RPS heavily weighted on the 409 path —
    // see docs/checkpoints/20260501-senior-multi-agent-review.md
    // critical #5. Historical comparisons against runs from before
    // the bump should note this is no longer apples-to-apples for
    // headline RPS, though p95 / RED-shape metrics remain
    // comparable for the accepted path.
    const payload = JSON.stringify({
        name: `Comparison Benchmark ${Date.now()}`,
        total_tickets: 5000000,
    });

    const res = http.post(`${BASE_URL}/events`, payload, {
        headers: { 'Content-Type': 'application/json' },
    });

    if (res.status !== 201) {
        throw new Error(`Setup failed: ${res.status} ${res.body}`);
    }

    check(res, { 'setup event created': (r) => r.status === 201 });
    return { eventID: res.json().id };
}

export default function (data) {
    const payload = JSON.stringify({
        user_id: randomIntBetween(1, 9999999),
        event_id: data.eventID,
        quantity: 1,
    });

    const headers = { 'Content-Type': 'application/json' };
    // IDEMPOTENCY=true exercises the N4 fingerprint compute path.
    // Each iteration mints a unique key so the handler hits the
    // Redis SETNX + fingerprint-write branch (cold), not the
    // cache-replay branch. To measure the replay branch instead,
    // set a stable key (e.g. `bench-${__VU}`) — that's an
    // expected future variant but not what we measure here.
    if (__ENV.IDEMPOTENCY === 'true') {
        headers['Idempotency-Key'] = `bench-${__VU}-${__ITER}-${Date.now()}`;
    }

    const start = Date.now();
    const res = http.post(`${BASE_URL}/book`, payload, { headers });
    bookingDuration.add(Date.now() - start);

    // POST /api/v1/book returns 202 Accepted (since PR #47 — async pipeline:
    // Redis-side deduct succeeded, DB persistence + payment + saga in flight).
    // 409 = sold out. Anything else is a real error.
    businessErrors.add(res.status !== 202 && res.status !== 409);

    check(res, {
        'status is 202 or 409': (r) => r.status === 202 || r.status === 409,
        'booking accepted': (r) => r.status === 202,
    });
}
