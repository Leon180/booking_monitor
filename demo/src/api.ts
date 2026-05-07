// API client for the booking_monitor Pattern A flow.
//
// Two base URLs are derived from a single VITE_API_ORIGIN env var so
// the demo can target nginx (compose stack, default) or the Go binary
// directly (`make run-server`). v1Base is `/api/v1` (the rate-limited
// public surface); testBase is `/test` (the dev-only /test/payment/*
// route group, gated server-side by ENABLE_TEST_ENDPOINTS).
//
// IMPORTANT: testBase exists because this is a demo. The real
// payment confirm flow goes through the provider webhook
// (POST /webhook/payment, HMAC-signed). /test/payment/confirm/:id
// is the codebase's "pretend the provider just delivered a webhook
// for this order" mock — it signs the envelope with the same secret
// and POSTs back to /webhook/payment. This is also what the
// integration tests use.

const apiOrigin = (
  import.meta.env.VITE_API_ORIGIN ?? 'http://localhost'
).replace(/\/+$/, '');

export const v1Base = `${apiOrigin}/api/v1`;
export const testBase = `${apiOrigin}/test`;

// ── Types matching the Go contract ──────────────────────────────────

export type OrderStatus =
  | 'awaiting_payment'
  | 'charging'
  | 'paid'
  | 'payment_failed'
  | 'expired'
  | 'compensated'
  | 'reserved' // legacy 202 response field (mid-flight clients only)
  | 'processing'; // pre-D3 legacy (kept for backwards compat)

export interface TicketType {
  id: string;
  event_id: string;
  name: string;
  total_tickets: number;
  available_tickets: number;
  price_cents: number;
  currency: string;
}

export interface EventResponse {
  id: string;
  name: string;
  total_tickets: number;
  available_tickets: number;
  ticket_types: TicketType[];
}

export interface BookingResponse {
  order_id: string;
  status: 'reserved' | 'processing';
  message: string;
  reserved_until: string;
  expires_in_seconds: number;
  links: { self: string; pay: string };
}

export interface OrderResponse {
  id: string;
  user_id: number;
  ticket_type_id: string;
  quantity: number;
  status: OrderStatus;
  amount_cents: number;
  currency: string;
  created_at: string;
  reserved_until?: string;
  payment_intent_id?: string;
}

export interface PaymentIntentResponse {
  order_id: string;
  payment_intent_id: string;
  client_secret: string;
  amount_cents: number;
  currency: string;
}

// ── Errors ──────────────────────────────────────────────────────────

export class ApiError extends Error {
  status: number;
  bodyText: string;
  constructor(status: number, bodyText: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.bodyText = bodyText;
  }
}

// ── Generic JSON helper ─────────────────────────────────────────────

async function jsonRequest<T>(
  url: string,
  init: RequestInit,
): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init.headers ?? {}),
    },
  });
  const text = await res.text();
  if (!res.ok) {
    throw new ApiError(
      res.status,
      text,
      `${init.method ?? 'GET'} ${url} → ${res.status}`,
    );
  }
  return text ? (JSON.parse(text) as T) : ({} as T);
}

// ── Endpoints ───────────────────────────────────────────────────────

export interface CreateEventRequest {
  name: string;
  total_tickets: number;
  price_cents: number;
  currency: string;
}

export function createEvent(req: CreateEventRequest) {
  return jsonRequest<EventResponse>(`${v1Base}/events`, {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

export interface BookRequest {
  user_id: number;
  ticket_type_id: string;
  quantity: number;
}

export function bookTicket(req: BookRequest, idempotencyKey: string) {
  return jsonRequest<BookingResponse>(`${v1Base}/book`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(req),
  });
}

export function getOrder(orderId: string) {
  return jsonRequest<OrderResponse>(`${v1Base}/orders/${orderId}`, {
    method: 'GET',
  });
}

export function payOrder(orderId: string) {
  return jsonRequest<PaymentIntentResponse>(
    `${v1Base}/orders/${orderId}/pay`,
    { method: 'POST', body: '{}' },
  );
}

// /test/payment/confirm/:order_id — server forges a signed webhook
// envelope and POSTs it to /webhook/payment, mirroring what the real
// provider would do. Only mounted when ENABLE_TEST_ENDPOINTS=true.
// outcome="succeeded" → marks paid; "failed" → marks payment_failed
// + emits order.failed for saga to revert Redis inventory.
export function confirmTestPayment(
  orderId: string,
  outcome: 'succeeded' | 'failed',
) {
  return jsonRequest<{ status: string }>(
    `${testBase}/payment/confirm/${orderId}?outcome=${outcome}`,
    { method: 'POST', body: '{}' },
  );
}
