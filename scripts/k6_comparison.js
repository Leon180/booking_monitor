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
    const payload = JSON.stringify({
        name: `Comparison Benchmark ${Date.now()}`,
        total_tickets: 500000,
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

    const start = Date.now();
    const res = http.post(`${BASE_URL}/book`, payload, {
        headers: { 'Content-Type': 'application/json' },
    });
    bookingDuration.add(Date.now() - start);

    businessErrors.add(res.status !== 200 && res.status !== 409);

    check(res, {
        'status is 200 or 409': (r) => r.status === 200 || r.status === 409,
        'booking accepted': (r) => r.status === 200,
    });
}
