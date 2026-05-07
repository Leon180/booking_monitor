// Intent-aware terminal display.
//
// Pattern A has multiple legitimate terminal states (paid /
// payment_failed / expired / compensated) that mean very different
// things to the user. A pure "show the latest server status" UI
// flickers and confuses — e.g. during a 1Hz poll the order may go
// awaiting_payment → charging → payment_failed in <1s, and the user
// who clicked "Pay" doesn't see the charging step at all.
//
// We track the user's INTENT (what they asked for) alongside the
// SERVER's observed status, then derive the displayed message from
// the pair. This is the same model Stripe Elements uses internally
// for confirm-card flows.

// Intent splits paying into _succeeded and _failed because the saga
// compensator can flip a `payment_failed` order to `compensated`
// between two 1 Hz polls — and a `(paying, compensated)` pair is
// indistinguishable from `(expiring, compensated)` without the
// requested-outcome bit. Codex round-1 P1: the original `paying`
// intent fell through to the booking display on `compensated` and
// rendered "Expired + compensated" for a declined payment.
export type Intent = 'booking' | 'paying_succeeded' | 'paying_failed' | 'expiring';

export type DisplayKind = 'pending' | 'success' | 'failure' | 'info';

export interface Display {
  kind: DisplayKind;
  title: string;
  detail: string;
}

import type { OrderStatus, OrderResponse } from './api';

export function deriveDisplay(
  intent: Intent,
  order: OrderResponse | null,
): Display {
  // Pre-202 / async-processing window: the worker hasn't persisted
  // the row yet, GET /orders/:id returns 404, and the UI's order
  // is still null. This is a normal ~ms window, not an error.
  if (!order) {
    return {
      kind: 'pending',
      title: 'Booking…',
      detail: 'Reserving inventory in Redis; the worker is async-persisting the row.',
    };
  }

  return forStatus(intent, order.status, order);
}

function forStatus(
  intent: Intent,
  status: OrderStatus,
  order: OrderResponse,
): Display {
  // Intent: paying_succeeded — user clicked "Pay (succeeded)" and we
  // expect the mock webhook to flip the order to paid.
  if (intent === 'paying_succeeded') {
    switch (status) {
      case 'awaiting_payment':
        return pending('Awaiting payment intent…', 'PaymentIntent created; waiting for the mock webhook.');
      case 'charging':
        return pending('Charging…', 'Provider has confirmed; webhook flipping order to paid.');
      case 'paid':
        return success('Paid', `Order ${short(order.id)} settled at ${formatPrice(order)}.`);
      case 'payment_failed':
        return failure(
          'Provider declined unexpectedly',
          'You asked for a successful confirm but the webhook surfaced a decline; saga is reverting.',
        );
      case 'compensated':
        return failure(
          'Provider declined unexpectedly + compensated',
          'Saga compensator restored Redis inventory after an unexpected decline.',
        );
      default:
        return forStatus('booking', status, order);
    }
  }

  // Intent: paying_failed — user clicked "Pay (mock failed)" so we
  // EXPECT the order to land in payment_failed → compensated. The
  // saga can run between polls, so compensated is a legitimate first
  // observed terminal — show it as "Declined + compensated", not
  // "Expired + compensated" (which is the let-it-expire flow).
  if (intent === 'paying_failed') {
    switch (status) {
      case 'awaiting_payment':
        return pending('Declining…', 'PaymentIntent created; mock webhook with outcome=failed in flight.');
      case 'charging':
        return pending('Declining…', 'Provider has confirmed (will fail per request); webhook in flight.');
      case 'payment_failed':
        return failure('Declined', 'Provider declined as requested; saga is reverting Redis inventory.');
      case 'compensated':
        return failure(
          'Declined + compensated',
          'Provider declined as requested; saga compensator restored Redis inventory.',
        );
      case 'paid':
        // Edge: user asked for failure but the webhook somehow
        // succeeded (race between two /test/payment/confirm calls).
        // Show the truth.
        return success(
          'Paid (despite decline request)',
          'A succeeded webhook landed before the failed one — race between two confirm calls.',
        );
      default:
        return forStatus('booking', status, order);
    }
  }

  // Intent: expiring — user clicked "Let it expire" and is watching
  // the reservation_until tick down. Once D6's expiry sweeper fires,
  // the order moves to expired then compensated.
  if (intent === 'expiring') {
    switch (status) {
      case 'awaiting_payment':
        return pending(
          'Reservation pending expiry…',
          `Reservation expires at ${formatReserved(order)}; the D6 sweeper will flip this to expired.`,
        );
      case 'expired':
        return success(
          'Expired (by design)',
          'D6 expiry sweeper closed the reservation; saga is reverting Redis inventory.',
        );
      case 'compensated':
        return success(
          'Expired + compensated',
          'Saga compensator reverted Redis inventory; the inventory is back in the pool.',
        );
      case 'paid':
      case 'charging':
        // Edge: user said "let it expire" but somehow paid (a stale
        // /pay click that landed before the user changed mind). Show
        // the truth, not the intent. Treat as a successful pay since
        // that's what the observed status says happened.
        return forStatus('paying_succeeded', status, order);
      default:
        return info(`Status: ${status}`, 'Unrecognised state for an expiring intent.');
    }
  }

  // Intent: booking — initial state, user hasn't picked Pay or Expire
  // yet. Surface the observed status honestly.
  switch (status) {
    case 'awaiting_payment':
      return info('Reserved', 'Your call: Pay (mock-confirm) or Let it expire.');
    case 'paid':
      return success('Paid', `Order ${short(order.id)} settled at ${formatPrice(order)}.`);
    case 'payment_failed':
      return failure('Payment failed', 'Order closed.');
    case 'expired':
      return info('Expired', 'D6 sweeper closed the reservation.');
    case 'compensated':
      return info('Expired + compensated', 'Inventory restored.');
    default:
      return info(`Status: ${status}`, '');
  }
}

const pending = (title: string, detail: string): Display => ({ kind: 'pending', title, detail });
const success = (title: string, detail: string): Display => ({ kind: 'success', title, detail });
const failure = (title: string, detail: string): Display => ({ kind: 'failure', title, detail });
const info = (title: string, detail: string): Display => ({ kind: 'info', title, detail });

function short(id: string): string {
  return id.slice(0, 8);
}

function formatPrice(o: OrderResponse): string {
  const major = (o.amount_cents / 100).toFixed(2);
  return `${o.currency.toUpperCase()} ${major}`;
}

function formatReserved(o: OrderResponse): string {
  if (!o.reserved_until) return '(unknown)';
  try {
    return new Date(o.reserved_until).toLocaleTimeString();
  } catch {
    return o.reserved_until;
  }
}
