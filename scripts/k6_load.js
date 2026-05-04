import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

// Custom metric: failures (excluding 409)
export const businessErrors = new Rate('business_errors');

// Helper function to replace k6/utils
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
        http_req_failed: [], // Ignore raw HTTP failures (since 409 is expected)
        business_errors: ['rate<0.01'], // Fail if real errors > 1%
        http_req_duration: ['p(95)<200'], // Expect <200ms response time at 500 VUs
    },
};

// Access app service internally in docker network to bypass Nginx Rate Limiting during pure backend capacity benchmarks
const BASE_URL = 'http://app:8080/api/v1';

export function setup() {
    // Create a new event for this test run.
    // D4.1: POST /api/v1/events now requires `price_cents` + `currency`
    // for the auto-provisioned default ticket_type; iteration body
    // POSTs against `ticket_type_id` (KKTIX 票種), NOT `event_id`.
    const eventName = `K6 Load Test Event ${Date.now()}`;
    const payload = JSON.stringify({
        name: eventName,
        total_tickets: 50000, // Enough tickets for the test duration
        price_cents: 2000,
        currency: 'usd',
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
        },
    };

    const res = http.post(`${BASE_URL}/events`, payload, params);

    if (res.status !== 201) {
        console.error(`Setup failed: ${res.status} ${res.body}`);
        throw new Error(`Setup failed: ${res.status} ${res.body}`);
    }

    const body = res.json();
    if (!body.ticket_types || body.ticket_types.length === 0) {
        throw new Error(`Setup failed: response missing ticket_types[] — D4.1 contract regression? body=${res.body}`);
    }

    check(res, {
        'setup event created': (r) => r.status === 201,
    });

    return { ticketTypeID: body.ticket_types[0].id };
}

export default function (data) {
    const payload = JSON.stringify({
        user_id: randomIntBetween(1, 10000),
        ticket_type_id: data.ticketTypeID,
        quantity: randomIntBetween(1, 5),
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
        },
    };

    const res = http.post(`${BASE_URL}/book`, payload, params);

    // POST /api/v1/book returns 202 Accepted (PR #47 — Pattern A async
    // pipeline). 409 = sold out / duplicate purchase. Anything else is
    // a real error. Pre-PR-47 versions of this script checked for 200;
    // that was a bug carried across multiple PRs and only surfaced
    // with the D4.1 cross-commit review.
    businessErrors.add(res.status !== 202 && res.status !== 409);

    check(res, {
        'status is 202 or 409': (r) => r.status === 202 || r.status === 409,
        'is sold out': (r) => r.status === 409 && r.json('error') === 'sold out',
        'is duplicate': (r) => r.status === 409 && r.json('error') === 'user already bought ticket',
    });

    // Short sleep to simulate user think time (optional, remove for max stress)
    // sleep(0.1); 
}
