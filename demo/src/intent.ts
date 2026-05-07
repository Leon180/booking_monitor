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

export type Intent = 'booking' | 'paying' | 'expiring';

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
  // Intent: paying — user clicked "Pay" and we're waiting for the
  // provider webhook to flip the status.
  if (intent === 'paying') {
    switch (status) {
      case 'awaiting_payment':
        return pending('Awaiting payment intent…', 'PaymentIntent created; waiting for the provider webhook.');
      case 'charging':
        return pending('Charging…', 'Provider has confirmed; webhook flipping order to paid.');
      case 'paid':
        return success('Paid', `Order ${short(order.id)} settled at ${formatPrice(order)}.`);
      case 'payment_failed':
        return failure('Payment failed', 'Saga compensator is reverting Redis inventory; the order is closed.');
      default:
        // Edge: user clicked "Pay" but the order moved sideways
        // (manual /test/payment/confirm with outcome=failed elsewhere,
        // or a recon force-fail). Show the observed terminal honestly.
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
        // the truth, not the intent.
        return forStatus('paying', status, order);
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
