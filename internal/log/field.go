package log

import (
	"time"

	"go.uber.org/zap"
)

// Field is an alias for zap.Field. Callers should type their variadic
// field parameters as log.Field so the log package becomes the single
// import that hides zap entirely. Because this is a type alias (not a
// new named type), values of zap.Field and log.Field are
// interchangeable at the call site.
type Field = zap.Field

// The constructors below re-export the small set of zap field helpers
// actually used outside internal/log. They exist so application code
// can import just "booking_monitor/internal/log" (and the typed tag
// package) without also importing go.uber.org/zap for one-off inline
// field names. Keeping the surface small is intentional: frequently
// used domain keys live in internal/log/tag/, and new re-exports
// should only be added when a field constructor is needed in two or
// more packages.
//
// If you need something not listed here (Bool, Duration, Any, ...)
// either use Logger.L() for raw zap access or add a new re-export in
// the same spirit.

// String constructs a Field with a string value.
func String(key, val string) Field { return zap.String(key, val) }

// Int constructs a Field with an int value.
func Int(key string, val int) Field { return zap.Int(key, val) }

// Int64 constructs a Field with an int64 value.
func Int64(key string, val int64) Field { return zap.Int64(key, val) }

// ByteString constructs a Field for a UTF-8 encoded byte slice. Use
// for marshalled payloads where copying to a string would be wasteful.
func ByteString(key string, val []byte) Field { return zap.ByteString(key, val) }

// Duration constructs a Field with a time.Duration value. Mirrors
// zap.Duration; kept here so bootstrap / server / lifecycle callers
// don't need to import zap just to log a timeout.
func Duration(key string, val time.Duration) Field { return zap.Duration(key, val) }

// Err attaches err to the log entry under the canonical "error" key.
// Mirrors zap.Error; kept here so callers don't need to import zap
// just to attach a non-domain error. For the canonical domain-error
// key, prefer tag.Error.
func Err(err error) Field { return zap.Error(err) }

// NamedError attaches err under a custom key — useful when a single
// log line carries more than one error (e.g. a primary failure and a
// cleanup failure). Prefer Err / tag.Error for the single-error case.
func NamedError(key string, err error) Field { return zap.NamedError(key, err) }
