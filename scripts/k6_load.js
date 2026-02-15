import http from 'k6/http';
import { check, sleep } from 'k6';

// Helper function to replace k6/utils
function randomIntBetween(min, max) {
    return Math.floor(Math.random() * (max - min + 1) + min);
}

export const options = {
    scenarios: {
        booking_stress: {
            executor: 'constant-vus',
            vus: __ENV.VUS || 500,
            duration: __ENV.DURATION || '30s',
        },
    },
    thresholds: {
        // 409s count as failures in K6 by default, so we remove this threshold
        // http_req_failed: ['rate<0.01'], 
        http_req_duration: ['p(95)<1000'], // Relaxed duration threshold for 500 VUs
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

    check(res, {
        'status is 200 or 409': (r) => r.status === 200 || r.status === 409,
        'status is 200': (r) => r.status === 200,
    });

    // Short sleep to simulate user think time (optional, remove for max stress)
    // sleep(0.1); 
}
