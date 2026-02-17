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

const BASE_URL = 'http://app:8080/api/v1'; // Access app service internally in docker network

export function setup() {
    // Create a new event for this test run
    const eventName = `K6 Load Test Event ${Date.now()}`;
    const payload = JSON.stringify({
        name: eventName,
        total_tickets: 50000, // Enough tickets for the test duration
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

    check(res, {
        'setup event created': (r) => r.status === 201,
    });

    const event = res.json();
    return { eventID: event.id };
}

export default function (data) {
    const payload = JSON.stringify({
        user_id: randomIntBetween(1, 10000),
        event_id: data.eventID,
        quantity: randomIntBetween(1, 5),
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
        },
    };

    const res = http.post(`${BASE_URL}/book`, payload, params);

    // Record error only if status is NOT 200 AND NOT 409
    businessErrors.add(res.status !== 200 && res.status !== 409);

    check(res, {
        'status is 200 or 409': (r) => r.status === 200 || r.status === 409,
        'is sold out': (r) => r.status === 409 && r.json('error') === 'sold out',
        'is duplicate': (r) => r.status === 409 && r.json('error') === 'user already bought ticket',
    });

    // Short sleep to simulate user think time (optional, remove for max stress)
    // sleep(0.1); 
}
