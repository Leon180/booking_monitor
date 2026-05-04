import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

export const businessErrors = new Rate('business_errors');
export const bookingDuration = new Trend('booking_duration', true);

// acceptedBookings — counter incremented only on 202 (the booking
// hot path). Pairs with the total `http_reqs/s` rate so reports
// show BOTH numbers: total RPS (capacity at the load-shed gate) and
// accepted-bookings/s (the path that actually exercises Redis Lua
// deduct + worker queue + DB persistence). Without this, headline
// RPS is dominated by the cheap 409 fast path once the pool depletes,
// which the senior-review checkpoint flagged.
//
// We report acceptedBookings as a Counter rather than a Rate because
// k6 prints Counters as both totals and per-second rates by default,
// and the per-second rate is what operators want when comparing
// "accepted booking throughput" across runs.
export const acceptedBookings = new Counter('accepted_bookings');

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
    // total_tickets is sized to a realistic flash-sale event scale
    // (500,000 — large concert / popular ticketing event). This is
    // the operating regime the system is designed to simulate; an
    // unrealistically large pool would just measure sustained
    // throughput, not the contention behavior that distinguishes
    // a flash-sale system.
    //
    // The senior-review-checkpoint Critical #5 concern was that
    // headline RPS heavily weighted the cheap 409 sold-out fast
    // path once the pool depleted. The fix is methodology — we
    // export `accepted_bookings` as a separate k6 metric so reports
    // distinguish "total HTTP RPS at the load-shed gate" from
    // "accepted bookings / s through the booking hot path". Both
    // numbers are operationally meaningful; collapsing them into
    // a single headline was the bug, not the pool size.
    //
    // D4.1: POST /api/v1/events now requires `price_cents` + `currency`
    // (the auto-provisioned default ticket_type's price snapshot).
    // The response surfaces `ticket_types[]` with the new ticket_type's
    // id — the iteration body POSTs against `ticket_type_id`, NOT
    // `event_id` (KKTIX 票種 model — pricing + inventory live on the
    // ticket_type entity).
    const payload = JSON.stringify({
        name: `Comparison Benchmark ${Date.now()}`,
        total_tickets: 500000,
        price_cents: 2000,
        currency: 'usd',
    });

    const res = http.post(`${BASE_URL}/events`, payload, {
        headers: { 'Content-Type': 'application/json' },
    });

    if (res.status !== 201) {
        throw new Error(`Setup failed: ${res.status} ${res.body}`);
    }

    const body = res.json();
    if (!body.ticket_types || body.ticket_types.length === 0) {
        throw new Error(`Setup failed: response missing ticket_types[] — D4.1 contract regression? body=${res.body}`);
    }

    check(res, { 'setup event created': (r) => r.status === 201 });
    return { ticketTypeID: body.ticket_types[0].id };
}

export default function (data) {
    const payload = JSON.stringify({
        user_id: randomIntBetween(1, 9999999),
        ticket_type_id: data.ticketTypeID,
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
    if (res.status === 202) {
        acceptedBookings.add(1);
    }

    check(res, {
        'status is 202 or 409': (r) => r.status === 202 || r.status === 409,
        'booking accepted': (r) => r.status === 202,
    });
}
