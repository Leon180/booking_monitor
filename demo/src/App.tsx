import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ApiError,
  bookTicket,
  confirmTestPayment,
  createEvent,
  getOrder,
  payOrder,
  type EventResponse,
  type OrderResponse,
} from './api';
import { deriveDisplay, type Intent } from './intent';
import './App.css';

// Polling cadence for GET /orders/:id. 1Hz is honest about cadence
// without hammering the API; the intent-aware terminal display
// (see intent.ts) handles flicker between adjacent states.
const POLL_INTERVAL_MS = 1000;

// Async-processing window: 202 returns BEFORE the worker has
// persisted the row, so the first few polls may 404. We retry
// silently for this many ticks before surfacing an error.
const TOLERATE_404_TICKS = 8;

type Phase = 'idle' | 'creating' | 'booked' | 'paying' | 'expiring' | 'terminal';

interface State {
  phase: Phase;
  intent: Intent;
  event: EventResponse | null;
  orderId: string | null;
  order: OrderResponse | null;
  error: string | null;
  log: string[];
}

const INITIAL_STATE: State = {
  phase: 'idle',
  intent: 'booking',
  event: null,
  orderId: null,
  order: null,
  error: null,
  log: [],
};

function uuidv4(): string {
  // Browser-native crypto.randomUUID is RFC 9562 v4 — used as the
  // Idempotency-Key value for POST /book. Stripe's idempotency-key
  // contract is "any opaque string up to 255 chars"; v4 is fine.
  return globalThis.crypto.randomUUID();
}

function isTerminal(status: string): boolean {
  return ['paid', 'payment_failed', 'expired', 'compensated'].includes(status);
}

function App() {
  const [state, setState] = useState<State>(INITIAL_STATE);
  const tickRef = useRef(0);

  const log = useCallback((line: string) => {
    setState((s) => ({ ...s, log: [...s.log, `${new Date().toLocaleTimeString()}  ${line}`] }));
  }, []);

  const errorOf = (err: unknown): string => {
    if (err instanceof ApiError) {
      return `${err.message}\n${err.bodyText}`;
    }
    return err instanceof Error ? err.message : String(err);
  };

  // ── Action: start a fresh demo flow ────────────────────────────
  // Creates a new event (with default ticket_type), books 1 ticket
  // for user_id=1, and starts the 1Hz poller. Each click resets
  // state — intentional: the demo is single-shot, replay = restart.
  const startBooking = useCallback(async () => {
    setState({ ...INITIAL_STATE, phase: 'creating' });
    tickRef.current = 0;
    try {
      const evt = await createEvent({
        name: `D8 Demo ${new Date().toISOString()}`,
        total_tickets: 100,
        price_cents: 2000,
        currency: 'usd',
      });
      log(`event created: ${evt.id} (ticket_type ${evt.ticket_types[0].id})`);
      setState((s) => ({ ...s, event: evt }));

      const ticketTypeId = evt.ticket_types[0].id;
      const idempotencyKey = uuidv4();
      const booking = await bookTicket(
        { user_id: 1, ticket_type_id: ticketTypeId, quantity: 1 },
        idempotencyKey,
      );
      log(`book 202: order_id=${booking.order_id}, reserved_until=${booking.reserved_until}`);
      setState((s) => ({
        ...s,
        phase: 'booked',
        orderId: booking.order_id,
      }));
    } catch (err) {
      const msg = errorOf(err);
      log(`ERROR: ${msg}`);
      setState((s) => ({ ...s, phase: 'terminal', error: msg }));
    }
  }, [log]);

  // ── Action: pay (POST /pay → POST /test/payment/confirm) ────────
  // Intent encodes the REQUESTED outcome (succeeded vs failed) so
  // intent.ts can show the right terminal even if the saga flips
  // payment_failed → compensated between two 1 Hz polls. Codex
  // round-1 P1: a single 'paying' intent collapsed both flows and
  // rendered "Expired + compensated" for a declined payment.
  const pay = useCallback(async (outcome: 'succeeded' | 'failed') => {
    if (!state.orderId) return;
    const intent: Intent = outcome === 'succeeded' ? 'paying_succeeded' : 'paying_failed';
    setState((s) => ({ ...s, phase: 'paying', intent }));
    try {
      const intentResp = await payOrder(state.orderId);
      log(`pay 200: payment_intent_id=${intentResp.payment_intent_id}`);
      const confirm = await confirmTestPayment(state.orderId, outcome);
      log(`test/confirm 200: outcome=${outcome}, forwarded=${confirm.forwarded}, webhook_status=${confirm.webhook_status}`);
    } catch (err) {
      const msg = errorOf(err);
      log(`ERROR: ${msg}`);
      setState((s) => ({ ...s, error: msg }));
    }
  }, [state.orderId, log]);

  // ── Action: let the reservation expire ──────────────────────────
  // No API call — we just stop interacting. The D6 expiry sweeper
  // sweeps `awaiting_payment` rows where `reserved_until < now()`
  // every 30s by default, so the UI will tick from "pending expiry"
  // to "expired" → "compensated" within ~1 minute.
  const letItExpire = useCallback(() => {
    setState((s) => ({ ...s, phase: 'expiring', intent: 'expiring' }));
    log('intent: let reservation expire (waiting for D6 sweeper)');
  }, [log]);

  // ── 1Hz poller ──────────────────────────────────────────────────
  useEffect(() => {
    if (!state.orderId) return;
    if (state.order && isTerminal(state.order.status)) return;

    const id = window.setInterval(async () => {
      try {
        const order = await getOrder(state.orderId!);
        tickRef.current = 0;
        setState((s) => {
          // Don't log the same status repeatedly — only on transitions.
          const transitioned = s.order?.status !== order.status;
          return {
            ...s,
            order,
            phase: isTerminal(order.status) ? 'terminal' : s.phase,
            log: transitioned ? [...s.log, `${new Date().toLocaleTimeString()}  poll: ${order.status}`] : s.log,
          };
        });
      } catch (err) {
        // Swallow 404 for the first few ticks (async-processing window).
        if (err instanceof ApiError && err.status === 404 && tickRef.current < TOLERATE_404_TICKS) {
          tickRef.current += 1;
          return;
        }
        log(`poll error: ${errorOf(err)}`);
      }
    }, POLL_INTERVAL_MS);

    return () => window.clearInterval(id);
  }, [state.orderId, state.order, log]);

  const display = deriveDisplay(state.intent, state.order);
  const showActions = state.phase === 'booked' && !isTerminal(state.order?.status ?? '');

  return (
    <main className="demo">
      <header>
        <h1>Booking Monitor — Pattern A demo</h1>
        <p className="sub">
          book → /pay → mock-webhook-confirm <em>or</em> let-expire → saga compensates
        </p>
      </header>

      <section className="control">
        <button onClick={startBooking} disabled={state.phase === 'creating' || state.phase === 'paying'}>
          {state.phase === 'idle' ? 'Start a fresh booking' : 'Restart with a new event'}
        </button>

        {showActions && (
          <div className="action-group">
            <button onClick={() => pay('succeeded')} className="primary">
              Pay (mock confirm: succeeded)
            </button>
            <button onClick={() => pay('failed')}>Pay (mock confirm: failed)</button>
            <button onClick={letItExpire}>Let it expire</button>
          </div>
        )}
      </section>

      <section className={`status status-${display.kind}`}>
        <h2>{display.title}</h2>
        <p>{display.detail}</p>
        {state.order && (
          <dl className="meta">
            <dt>order_id</dt><dd><code>{state.order.id}</code></dd>
            <dt>status</dt><dd><code>{state.order.status}</code></dd>
            {state.order.reserved_until && (<><dt>reserved_until</dt><dd><code>{state.order.reserved_until}</code></dd></>)}
            {state.order.payment_intent_id && (<><dt>payment_intent</dt><dd><code>{state.order.payment_intent_id}</code></dd></>)}
          </dl>
        )}
      </section>

      {state.error && (
        <section className="error">
          <pre>{state.error}</pre>
        </section>
      )}

      <section className="log">
        <h3>Activity log</h3>
        <pre>{state.log.length === 0 ? 'Click "Start a fresh booking" to begin.' : state.log.join('\n')}</pre>
      </section>
    </main>
  );
}

export default App;
