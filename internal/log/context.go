package log

import "context"

// ctxValue is the single payload we store in context.Value. A value
// type (not pointer) with only 24 bytes keeps the per-request cost to
// exactly one context.WithValue allocation — no cloned zap core.
//
// The correlationID is stored as a plain string, NOT baked into the
// logger via zap's With(). That's the hot-path win: happy-path
// requests cost ~40 bytes (valueCtx + this struct), not ~1.2 KB
// (valueCtx + zap core clone).
type ctxValue struct {
	logger        *Logger
	correlationID string
}

type ctxKey struct{}

// NewContext stores the request-scoped logger and correlation id in
// ctx. Both are read at log-write time by the Logger.Error / Info /
// etc. methods — and by enrichFields which adds `correlation_id` plus
// OTEL `trace_id` / `span_id` automatically to every log call.
//
// Callers pass correlationID="" for non-request contexts (tests,
// background goroutines). The enrichment just skips the field.
func NewContext(ctx context.Context, l *Logger, correlationID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, ctxValue{logger: l, correlationID: correlationID})
}

// FromContext returns the Logger associated with ctx, or a Nop logger
// if none is present. Nop keeps missing injection silent; callers who
// forget to wire a logger see "nothing happens" rather than writing to
// a global.
func FromContext(ctx context.Context) *Logger {
	if ctx == nil {
		return NewNop()
	}
	if v, ok := ctx.Value(ctxKey{}).(ctxValue); ok && v.logger != nil {
		return v.logger
	}
	return NewNop()
}

// CorrelationIDFromCtx returns the request-scoped correlation id set
// by the HTTP middleware, or "" if none is present. Log callers do
// NOT need to invoke this — the Logger's ctx-aware methods pick it up
// automatically via enrichFields.
func CorrelationIDFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKey{}).(ctxValue); ok {
		return v.correlationID
	}
	return ""
}

// CorrelationIDKey is the canonical zap field key emitted by
// enrichFields. Exposed so external code (metrics, response headers)
// can reuse the same string constant.
const CorrelationIDKey = "correlation_id"
