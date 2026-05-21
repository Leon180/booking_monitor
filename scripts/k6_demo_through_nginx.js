// k6 scenario for the Stage 5 dashboard demo when traffic flows
// through nginx (port 80), exercising the edge rate-limit + admission
// control path.
//
// Diverges from `k6_intake_only.js` in two intentional ways:
//
//   1. Default target is `http://nginx` (the edge), NOT `http://app:8080`.
//      nginx's `limit_req zone=api rate=100r/s burst=200 nodelay` caps
//      single-source traffic, so the visible RPS will be ~100/s
//      regardless of how many VUs we throw at it — the point is to
//      show the edge guard working, not to find the service ceiling.
//
//   2. HTTP 429 (Too Many Requests, from nginx's rate-limit module)
//      is treated as an EXPECTED outcome — not a business error. The
//      whole point of the demo is to show nginx rejecting overflow
//      requests cleanly; counting them as errors would breach the
//      threshold immediately. They are still tracked separately via
//      the `rate_limited` counter so the operator can see how many
//      were shed.
//
// The narrative this script supports during a demo:
//
//   "Watch the war-room dashboard — events still stream in real time
//    even while nginx is shedding 95% of the booking POSTs at the
//    edge. The SSE control plane is a long-lived HTTP/1.1 stream that
//    isn't subject to the rate-limit zone (different upstream block,
//    no `limit_req` directive), so operators keep observability
//    during a burst even though the booking path is throttled."
//
// Pairs with `scripts/k6_intake_only.js` (direct-to-app) for the
// side-by-side demo: same script semantics, different network path,
// dramatically different visual on the dashboard.

import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

export const acceptedBookings = new Counter('accepted_bookings');
export const soldOut = new Counter('sold_out_409');
export const rateLimited = new Counter('rate_limited_429');
export const businessErrors = new Rate('business_errors');

const API_ORIGIN = __ENV.API_ORIGIN || 'http://nginx';
const v1Base = `${API_ORIGIN}/api/v1`;

const TICKET_POOL = parseInt(__ENV.TICKET_POOL || '500', 10);

export const options = {
    scenarios: {
        edge_throttled: {
            executor: 'constant-vus',
            vus: parseInt(__ENV.VUS || '500', 10),
            duration: __ENV.DURATION || '30s',
        },
    },
    thresholds: {
        // 429 from the edge is an expected admission-control outcome,
        // not an error. http_req_failed will be very high (most calls
        // fail with 429) and that's the demo's point. Gate via
        // business_errors instead, which excludes both 409 and 429.
        http_req_failed: [],
        business_errors: ['rate<0.05'],
    },
};

function randInt(min, max) {
    return Math.floor(Math.random() * (max - min + 1) + min);
}

export function setup() {
    // Setup hits POST /events ONCE — a single request that fits
    // comfortably inside nginx's burst budget (200), so we don't need
    // a direct-to-app path for setup.
    const eventName = `Stage 5 Demo (through nginx) ${Date.now()}`;
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
    console.log(`[setup] Most POST /book calls will return 429 from nginx (rate-limit at edge).`);
    console.log(`[setup] Watch http://localhost/admin/ — events stream in real time for the few that DO get through.`);
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

    if (res.status === 202) {
        acceptedBookings.add(1);
        businessErrors.add(0);
        return;
    }
    if (res.status === 409) {
        soldOut.add(1);
        businessErrors.add(0);
        return;
    }
    if (res.status === 429) {
        rateLimited.add(1);
        businessErrors.add(0);
        return;
    }
    // Anything else: treat as business error.
    check(res, { 'book 202/409/429': (r) => r.status === 202 || r.status === 409 || r.status === 429 });
    businessErrors.add(1);
}
