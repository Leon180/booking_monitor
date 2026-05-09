// k6 scenario for INTAKE-ONLY booking-layer ceiling (D12.5 Slice 8).
//
// Aligns with the operator-experience benchmark methodology used by
// Ticketmaster (Q1 2023: 250 tickets/sec peak intake), Stripe (Q3'24:
// PaymentIntent rate measured separately from settlement rate), and
// Shopify (BFCM 2024: edge req/s reported separately from accepted
// orders/s). Each layer of a flash-sale booking funnel scales
// differently, so the comparable headline RPS is layer-isolated, not
// full-flow throttled.
//
// vs k6_two_step_flow.js:
//   two_step_flow:  full-flow per VU (book → poll → pay → confirm →
//                   poll paid OR abandon → poll compensated). Each
//                   VU spends most of its time NOT hammering /book —
//                   the intake/s number is full-flow-throttled.
//   intake_only:    each VU hammers POST /book → next iteration as
//                   fast as possible. NO poll, NO pay, NO confirm.
//                   Measures the booking-layer ceiling — what the
//                   Redis Lua deduct (Stages 2-4) or the SELECT FOR
//                   UPDATE row-lock (Stage 1) can sustain when no
//                   downstream work competes for VU slots.
//
// REQUIRED API STACK ENV: same as the full-flow scenario; this
// script doesn't need ENABLE_TEST_ENDPOINTS or webhook config but
// it doesn't hurt to keep them for orchestration parity.
//
// Custom metrics (subset of full-flow's):
//   accepted_bookings   counter   202 from POST /book
//   business_errors     rate      non-202 / non-409 responses
//
// Note: 409 is NOT a business error — it's the expected
// sold-out fast path. Stage 4's saga-path metrics aren't
// exercised in this scenario by design (no abandonments,
// no payment failures).

import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

export const acceptedBookings = new Counter('accepted_bookings');
export const businessErrors = new Rate('business_errors');

const API_ORIGIN = __ENV.API_ORIGIN || 'http://app:8080';
const v1Base = `${API_ORIGIN}/api/v1`;

const TICKET_POOL = parseInt(__ENV.TICKET_POOL || '500000', 10);

export const options = {
    scenarios: {
        intake_only: {
            executor: 'constant-vus',
            vus: parseInt(__ENV.VUS || '100', 10),
            duration: __ENV.DURATION || '60s',
        },
    },
    thresholds: {
        // 409 sold-out is expected; gate via business_errors instead.
        http_req_failed: [],
        // Stage 4 will breach this under saturation — that's
        // informational, not fatal (the orchestration script
        // distinguishes threshold-breach from real k6 failure).
        business_errors: ['rate<0.05'],
    },
};

function randInt(min, max) {
    return Math.floor(Math.random() * (max - min + 1) + min);
}

export function setup() {
    // Same setup() as k6_two_step_flow.js — create a fresh event
    // with the configured ticket pool and capture its ticket_type_id.
    const eventName = `D12.5 Intake-Only Bench ${Date.now()}`;
    const payload = JSON.stringify({
        name: eventName,
        total_tickets: TICKET_POOL,
        price_cents: 2000,
        currency: 'usd',
    });
    const res = http.post(`${v1Base}/events`, payload, {
        headers: { 'Content-Type': 'application/json' },
    });
    check(res, { 'event created': (r) => r.status === 201 || r.status === 200 });
    if (res.status !== 201 && res.status !== 200) {
        throw new Error(`setup: event create failed status=${res.status} body=${res.body}`);
    }
    const body = res.json();
    const ticketTypeID = body.ticket_types && body.ticket_types[0] && body.ticket_types[0].id;
    if (!ticketTypeID) {
        throw new Error(`setup: event response missing ticket_types[0].id; body=${JSON.stringify(body)}`);
    }
    console.log(`[setup] event_id=${body.id} ticket_type_id=${ticketTypeID} pool=${TICKET_POOL}`);
    return { ticketTypeID };
}

export default function (data) {
    const userID = randInt(1, 1_000_000);
    const bookPayload = JSON.stringify({
        user_id: userID,
        ticket_type_id: data.ticketTypeID,
        quantity: 1,
    });
    const headers = { 'Content-Type': 'application/json' };

    const res = http.post(`${v1Base}/book`, bookPayload, { headers });

    if (res.status === 409) {
        // Sold out — expected fast path; not a business error.
        // (Pool is 500k by default but stage-1's row-lock plateau
        // will run out faster than stages 2-4's Lua deduct.)
        return;
    }
    if (res.status === 202) {
        acceptedBookings.add(1);
        businessErrors.add(0);
        return;
    }
    // Anything else: treat as business error.
    check(res, { 'book 202 or 409': (r) => r.status === 202 || r.status === 409 });
    businessErrors.add(1);
}
