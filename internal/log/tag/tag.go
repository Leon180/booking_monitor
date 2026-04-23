// Package tag provides strongly-typed zap.Field constructors for the
// canonical log keys used across the booking_monitor codebase.
//
// Why typed tags instead of "key", value pairs:
//
//  1. Compile-time typo protection — tag.OrderID(123) will not compile
//     if the underlying field name ever changes; "order_id", 123 will.
//  2. Consistent key vocabulary — every call site writes `order_id`,
//     not a mix of `order_id` / `orderID` / `orderId`.
//  3. Zero-alloc — each helper returns a zap.Field directly, so the
//     hot path is the ctx-aware methods that skip allocation when
//     the level is disabled via zap.Logger.Check():
//     l.Error(ctx, msg, tag.OrderID(id), tag.Error(err)).
//
// If a new key is used in two or more unrelated packages, add a
// constructor here. One-off keys can stay inline with zap.String / zap.Int.
package tag

import (
	"go.uber.org/zap"
)

// ── Identifiers ──────────────────────────────────────────────────────

// OrderID labels the log entry with a canonical order identifier.
func OrderID(id int) zap.Field { return zap.Int("order_id", id) }

// EventID labels the log entry with a ticketing event identifier.
func EventID(id int) zap.Field { return zap.Int("event_id", id) }

// UserID labels the log entry with the authenticated user id.
func UserID(id int) zap.Field { return zap.Int("user_id", id) }

// (correlation_id is auto-injected by the ctx-aware log methods —
// no tag helper needed. See internal/log/log.go enrichFields.)

// MsgID labels the log entry with a Redis Streams or Kafka message id.
func MsgID(id string) zap.Field { return zap.String("msg_id", id) }

// LockID labels the log entry with a Postgres advisory lock id.
func LockID(id int64) zap.Field { return zap.Int64("lock_id", id) }

// ── Kafka / streams ──────────────────────────────────────────────────

// Topic labels the log entry with a Kafka topic name.
func Topic(name string) zap.Field { return zap.String("topic", name) }

// Partition labels the log entry with a Kafka partition index.
func Partition(p int) zap.Field { return zap.Int("partition", p) }

// Offset labels the log entry with a Kafka offset.
func Offset(off int64) zap.Field { return zap.Int64("offset", off) }

// ── Domain values ────────────────────────────────────────────────────

// Quantity labels the log entry with a booking quantity.
func Quantity(q int) zap.Field { return zap.Int("quantity", q) }

// Amount labels the log entry with a monetary amount.
func Amount(a float64) zap.Field { return zap.Float64("amount", a) }

// Status labels the log entry with an order / event status string.
func Status(s string) zap.Field { return zap.String("status", s) }

// ── Error ────────────────────────────────────────────────────────────

// Error attaches the error to the log entry under the canonical key
// "error". Delegates to zap.Error which also records the stack trace
// when the logger was built with zap.AddStacktrace.
func Error(err error) zap.Field { return zap.Error(err) }
