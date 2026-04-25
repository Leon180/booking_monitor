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

// Target through Nginx so the rate-limit zone applies — that's the
// whole point of this script. Other k6 scripts (load, comparison,
// soldout) target the app directly to bypass the limit during
// capacity benchmarks.
const NGINX_URL = 'http://booking_nginx:80/api/v1';

// setup creates a fresh event and returns its UUID. Identical pattern
// to k6_load.js / k6_comparison.js / k6_verify_soldout.js. Required
// post-PR-34 — event_id is now a UUID v7 string, hardcoded `1` no
// longer parses on the server.
export function setup() {
    const eventName = `K6 Rate Limit Event ${Date.now()}`;
    const payload = JSON.stringify({
        name: eventName,
        total_tickets: 100000, // High enough that no rate-limited request gets a "sold out" by accident.
    });
    const params = { headers: { 'Content-Type': 'application/json' } };

    const res = http.post(`${NGINX_URL}/events`, payload, params);

    if (res.status !== 201) {
        console.error(`Setup failed: ${res.status} ${res.body}`);
        throw new Error(`Setup failed: ${res.status} ${res.body}`);
    }
    check(res, { 'setup event created': (r) => r.status === 201 });

    const event = res.json();
    return { eventID: event.id };
}

export default function (data) {
    // Generate a random payload — event_id comes from setup()'s UUID.
    const payload = JSON.stringify({
        user_id: Math.floor(Math.random() * 10000) + 1,
        event_id: data.eventID,
        quantity: 1,
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
        },
    };

    const res = http.post(`${NGINX_URL}/book`, payload, params);

    // Verify Nginx Rate Limiting (Should start returning 429s after the 200 burst is exhausted)
    // The actual HTTP status could be 202 (Accepted), 409 (Sold out/Conflict), or 429 (Too Many Requests)
    check(res, {
        'Status is NOT 500 (Server Error)': (r) => r.status !== 500,
        'Nginx Ratelimited (429)': (r) => r.status === 429,
        'Success or Conflict (202/409)': (r) => r.status === 202 || r.status === 409,
    });
}
