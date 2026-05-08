// k6 scenario for the Pattern A two-step reservation+payment flow (D9-minimal).
//
// Models the realistic conversion-rate-aware booking flow:
//   - 80% of VUs: book → poll-reserved → /pay → confirm-succeeded → poll-paid
//   - 20% of VUs: book → poll-reserved → no /pay → expected to expire (D6) → compensate
//
// Why one script with two scenarios (not two scripts): the abandonment path
// is a *first-class outcome* in Pattern A, not a fault. Mixing them in one
// run gives realistic conversion-rate-aware metrics; running separately
// would overstate paid throughput and understate expiry sweeper load.
//
// REQUIRED API STACK ENV (the abandon path can't finalize within the run
// window with the default 15m reservation TTL + 30s sweep interval):
//   APP_ENV=development
//   ENABLE_TEST_ENDPOINTS=true              # for the test-only confirm endpoint
//   PAYMENT_WEBHOOK_SECRET=demo_secret_local_only
//   BOOKING_RESERVATION_WINDOW=20s          # ~1m total cycle
//   EXPIRY_SWEEP_INTERVAL=5s                # default 30s is too slow for a 90s run
// See `make demo-up` and `make bench-two-step` for the runnable recipe.
//
// API base derivation — two bases needed (D8 lesson):
//   v1Base    = ${API_ORIGIN}/api/v1   product API
//   testBase  = ${API_ORIGIN}/test     mock-confirm test endpoint (root-mounted)
// In Docker (via `make bench-two-step`): API_ORIGIN=http://app:8080 (bypasses nginx).
// On host:                                API_ORIGIN=http://localhost (via nginx).
//
// Custom metrics:
//   accepted_bookings              counter  202 from POST /book
//   payment_intents_created        counter  200 from POST /pay
//   paid_orders                    counter  status=paid observed via polling
//   compensated_abandons           counter  status=compensated observed (saga ran;
//                                           Redis inventory reverted). Increments
//                                           ONLY at the true Pattern A abandon
//                                           terminal — `expired` alone doesn't
//                                           prove compensation, only that D6 fired.
//   expired_seen                   counter  observability-only: `expired` was
//                                           observed transiently during the
//                                           expired→compensated edge. May be 0
//                                           if k6 polling cadence missed the
//                                           transient window — that's fine; the
//                                           authoritative abandon outcome is
//                                           compensated_abandons.
//   book_to_reserved_duration      trend    p95 from 202 → first non-404 GET /orders
//   reserved_to_paid_duration      trend    p95 from /pay 200 → first paid status
//   end_to_end_paid_duration       trend    book → paid, full timeline
//   business_errors                rate     non-202/non-200 paths

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

export const acceptedBookings = new Counter('accepted_bookings');
export const paymentIntentsCreated = new Counter('payment_intents_created');
export const paidOrders = new Counter('paid_orders');
export const compensatedAbandons = new Counter('compensated_abandons');
export const expiredSeen = new Counter('expired_seen');
export const bookToReserved = new Trend('book_to_reserved_duration', true);
export const reservedToPaid = new Trend('reserved_to_paid_duration', true);
export const endToEndPaid = new Trend('end_to_end_paid_duration', true);
export const businessErrors = new Rate('business_errors');

const API_ORIGIN = __ENV.API_ORIGIN || 'http://app:8080';
const v1Base = `${API_ORIGIN}/api/v1`;
const testBase = `${API_ORIGIN}/test`;

const ABANDON_RATIO = parseFloat(__ENV.ABANDON_RATIO || '0.2');
const TICKET_POOL = parseInt(__ENV.TICKET_POOL || '50000', 10);
const POLL_TIMEOUT_MS = parseInt(__ENV.POLL_TIMEOUT_MS || '60000', 10);
const POLL_INTERVAL_MS = parseInt(__ENV.POLL_INTERVAL_MS || '500', 10);

export const options = {
    scenarios: {
        two_step_flow: {
            executor: 'constant-vus',
            vus: parseInt(__ENV.VUS || '100', 10),
            duration: __ENV.DURATION || '90s',
        },
    },
    thresholds: {
        http_req_failed: [],          // 409 sold-out is expected; gate via business_errors instead
        business_errors: ['rate<0.05'],
        // Latency thresholds are observation-only at this baseline.
        end_to_end_paid_duration: [`p(95)<${__ENV.E2E_P95_MS || '30000'}`],
    },
};

function randInt(min, max) {
    return Math.floor(Math.random() * (max - min + 1) + min);
}

export function setup() {
    // Create a fresh event for the run with the configured ticket pool.
    const eventName = `D9 Two-Step Bench ${Date.now()}`;
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

// Poll an order's status until isTerminal returns true, or timeout.
// onObservation (optional) is called with each observed status — useful
// for tracking transient states the caller wants to observe but not gate
// the return on (e.g. `expired` on the way to `compensated`).
// Returns { status, elapsedMs } or null on timeout.
function pollOrder(orderID, isTerminal, onObservation) {
    const start = Date.now();
    while (Date.now() - start < POLL_TIMEOUT_MS) {
        const res = http.get(`${v1Base}/orders/${orderID}`);
        if (res.status === 200) {
            const body = res.json();
            if (body && body.status) {
                if (onObservation) {
                    onObservation(body.status);
                }
                if (isTerminal(body.status)) {
                    return { status: body.status, elapsedMs: Date.now() - start };
                }
            }
        }
        // 404 during the brief async-processing window is expected — keep polling.
        sleep(POLL_INTERVAL_MS / 1000);
    }
    return null;
}

export default function (data) {
    const userID = randInt(1, 1_000_000);
    const bookPayload = JSON.stringify({
        user_id: userID,
        ticket_type_id: data.ticketTypeID,
        quantity: 1,
    });
    const bookHeaders = { 'Content-Type': 'application/json' };
    const bookStart = Date.now();

    // Step 1: book
    const bookRes = http.post(`${v1Base}/book`, bookPayload, { headers: bookHeaders });
    if (bookRes.status === 409) {
        // Sold out — expected fast path. Don't count as business error.
        return;
    }
    if (bookRes.status !== 202) {
        businessErrors.add(1);
        check(bookRes, { 'book 202': (r) => r.status === 202 });
        return;
    }
    acceptedBookings.add(1);
    businessErrors.add(0);
    const orderID = bookRes.json().order_id;
    if (!orderID) {
        businessErrors.add(1);
        return;
    }

    // Step 2: poll until reserved (worker async-persisted)
    const reservedResult = pollOrder(orderID, (s) =>
        s === 'reserved' || s === 'awaiting_payment' || s === 'paid' || s === 'expired' || s === 'compensated'
    );
    if (!reservedResult) {
        businessErrors.add(1);
        return;
    }
    bookToReserved.add(reservedResult.elapsedMs);

    // Branch: abandon vs pay. ABANDON_RATIO controls split.
    const abandon = Math.random() < ABANDON_RATIO;

    if (abandon) {
        // Abandon path: don't pay. Wait for D6 expiry sweeper → saga compensator.
        // Authoritative terminal is `compensated` ONLY — `expired` proves only
        // that D6 fired, not that saga compensation ran. Tracking `expired`
        // transient observations into a separate counter so a broken/backed-up
        // saga consumer surfaces as "expired_seen high, compensated_abandons low"
        // instead of looking successful.
        let sawExpired = false;
        const abandonResult = pollOrder(
            orderID,
            (s) => s === 'compensated',
            (s) => {
                if (s === 'expired') {
                    sawExpired = true;
                }
            },
        );
        if (sawExpired) {
            expiredSeen.add(1);
        }
        if (abandonResult) {
            compensatedAbandons.add(1);
        } else {
            businessErrors.add(1);
        }
        return;
    }

    // Pay path
    const payStart = Date.now();
    const payRes = http.post(`${v1Base}/orders/${orderID}/pay`, '', {
        headers: bookHeaders,
    });
    if (payRes.status !== 200) {
        // 409 if reservation expired between book and pay; rare but possible
        // at high VUs with short reservation windows. Count as business error
        // because the run env is supposed to keep TTL > book→pay latency.
        businessErrors.add(1);
        return;
    }
    paymentIntentsCreated.add(1);

    // Confirm via test endpoint (test-only; gated by ENABLE_TEST_ENDPOINTS).
    const confirmRes = http.post(`${testBase}/payment/confirm/${orderID}?outcome=succeeded`, '', {
        headers: bookHeaders,
    });
    if (confirmRes.status !== 200 && confirmRes.status !== 202 && confirmRes.status !== 204) {
        businessErrors.add(1);
        return;
    }

    // Step 3: poll until paid
    const paidResult = pollOrder(orderID, (s) => s === 'paid');
    if (!paidResult) {
        businessErrors.add(1);
        return;
    }
    paidOrders.add(1);
    reservedToPaid.add(Date.now() - payStart);
    endToEndPaid.add(Date.now() - bookStart);
}
