import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
    scenarios: {
        constant_request_rate: {
            executor: 'constant-arrival-rate',
            rate: 500, // 500 requests per second
            timeUnit: '1s', // The rate is measured over this period
            duration: '5s', // Run the test for 5 seconds
            preAllocatedVUs: 100, // Pre-allocate VUs to ensure accurate arrival rate
            maxVUs: 500,
        },
    },
};

export default function () {
    // Generate a random payload
    const payload = JSON.stringify({
        user_id: Math.floor(Math.random() * 10000) + 1,
        event_id: 1,
        quantity: 1,
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
        },
    };

    // Target the Nginx container (booking_nginx) since k6 runs in the same bridge network
    const res = http.post('http://booking_nginx:80/api/v1/book', payload, params);

    // Verify Nginx Rate Limiting (Should start returning 429s after the 200 burst is exhausted)
    // The actual HTTP status could be 202 (Accepted), 409 (Sold out/Conflict), or 429 (Too Many Requests)
    check(res, {
        'Status is NOT 500 (Server Error)': (r) => r.status !== 500,
        'Nginx Ratelimited (429)': (r) => r.status === 429,
        'Success or Conflict (202/409)': (r) => r.status === 202 || r.status === 409,
    });
}
