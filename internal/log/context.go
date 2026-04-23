package log

import "context"

// ctxKey is the unexported type used as the context.Value key. Using a
// zero-sized struct as the key avoids collisions with any string-based
// key from another package.
type ctxKey struct{}

// NewContext returns a derived context that carries l. Callers typically
// invoke this in request-scoped middleware, e.g.
//
//	logger := baseLogger.With(tag.CorrelationID(corID))
//	ctx := log.NewContext(c.Request.Context(), logger)
//	c.Request = c.Request.WithContext(ctx)
func NewContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the Logger associated with ctx, or a Nop logger
// if none is present. Returning Nop (not a global) means an uninjected
// caller logs nothing instead of silently writing to a hidden global —
// which makes missing plumbing loud during tests and quiet in prod.
func FromContext(ctx context.Context) *Logger {
	if ctx == nil {
		return NewNop()
	}
	if l, ok := ctx.Value(ctxKey{}).(*Logger); ok && l != nil {
		return l
	}
	return NewNop()
}

// CorrelationIDKey is the canonical zap field key used by middleware
// that baked the correlation id into the context-carried logger via
// With(tag.CorrelationID(id)). Exposed as a constant so call sites
// that want to extract it for non-log purposes (response headers,
// metrics) don't hardcode the string.
const CorrelationIDKey = "correlation_id"
