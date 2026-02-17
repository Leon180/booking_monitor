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
            vus: 50, // Lower VUs to make it easier to follow
            duration: '10s', // Short run just to prove the logic
        },
    },
    thresholds: {
        http_req_failed: [],
        business_errors: ['rate<0.01'],
    },
};

const BASE_URL = 'http://app:8080/api/v1';

export function setup() {
    // Create a new event for this test run with FEW tickets
    const eventName = `K6 Verify SoldOut ${Date.now()}`;
    const payload = JSON.stringify({
        name: eventName,
        total_tickets: 100, // <--- LOW INVENTORY to force Sold Out
    });

    const params = { headers: { 'Content-Type': 'application/json' } };
    const res = http.post(`${BASE_URL}/events`, payload, params);

    if (res.status !== 201) {
        throw new Error(`Setup failed: ${res.body}`);
    }

    const event = res.json();
    return { eventID: event.id };
}

export default function (data) {
    const payload = JSON.stringify({
        user_id: randomIntBetween(1, 10000), // 10k users
        event_id: data.eventID,
        quantity: 1,
    });

    const params = { headers: { 'Content-Type': 'application/json' } };
    const res = http.post(`${BASE_URL}/book`, payload, params);

    // Record error only if status is NOT 200 AND NOT 409
    businessErrors.add(res.status !== 200 && res.status !== 409);

    check(res, {
        'status is 200 or 409': (r) => r.status === 200 || r.status === 409,
        'is sold out': (r) => r.status === 409 && r.json('error') === 'sold out',
        'is duplicate': (r) => r.status === 409 && r.json('error') === 'user already bought ticket',
    });
}
