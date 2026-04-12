package logger

import (
	"context"

	"go.uber.org/zap"
)

// RequestContext holds all per-request metadata in a single struct so we
// need only ONE context.WithValue call instead of N separate calls.
// This is critical for GC: each context.WithValue allocates a 32-byte
// valueCtx struct, and each c.Request.WithContext clones the entire
// *http.Request (~200 bytes). Consolidating N values into 1 struct
// cuts both the WithValue and WithContext cost to 1/N.
type RequestContext struct {
	Logger        *zap.SugaredLogger
	CorrelationID string
}

type reqCtxKey struct{}

// WithRequestContext stores all per-request metadata in a single
// context.WithValue call.
func WithRequestContext(ctx context.Context, rctx *RequestContext) context.Context {
	return context.WithValue(ctx, reqCtxKey{}, rctx)
}

// RequestContextFromCtx retrieves the consolidated request context.
func RequestContextFromCtx(ctx context.Context) *RequestContext {
	if rctx, ok := ctx.Value(reqCtxKey{}).(*RequestContext); ok {
		return rctx
	}
	return nil
}

// FromCtx returns the SugaredLogger associated with the context.
// If no logger is associated, it returns the global SugaredLogger.
func FromCtx(ctx context.Context) *zap.SugaredLogger {
	if rctx := RequestContextFromCtx(ctx); rctx != nil && rctx.Logger != nil {
		return rctx.Logger
	}
	return zap.S()
}

// CorrelationIDFromCtx retrieves the correlation ID from the context.
// Returns "" if not set.
func CorrelationIDFromCtx(ctx context.Context) string {
	if rctx := RequestContextFromCtx(ctx); rctx != nil {
		return rctx.CorrelationID
	}
	return ""
}

// WithCorrelation returns the context logger enriched with the
// correlation_id field (if present). Use this on ERROR PATHS ONLY —
// calling it on every request would re-introduce the zap core clone
// that we removed from CorrelationIDMiddleware for GC reasons.
//
// Usage (in handler error paths):
//
//	logger.WithCorrelation(ctx).Errorw("something failed", "error", err)
func WithCorrelation(ctx context.Context) *zap.SugaredLogger {
	l := FromCtx(ctx)
	if id := CorrelationIDFromCtx(ctx); id != "" {
		return l.With("correlation_id", id)
	}
	return l
}

// --- Legacy compatibility ---
// These are kept so existing code that calls WithCtx / WithCorrelationID
// continues to compile. They delegate to the consolidated RequestContext.

// WithCtx returns a copy of parent context with the logger set.
// Prefer WithRequestContext for new code (consolidates all per-request
// metadata into a single WithValue call).
func WithCtx(ctx context.Context, l *zap.SugaredLogger) context.Context {
	rctx := RequestContextFromCtx(ctx)
	if rctx == nil {
		rctx = &RequestContext{}
	}
	newRctx := &RequestContext{
		Logger:        l,
		CorrelationID: rctx.CorrelationID,
	}
	return WithRequestContext(ctx, newRctx)
}

// WithCorrelationID stores the correlation ID in the context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	rctx := RequestContextFromCtx(ctx)
	if rctx == nil {
		rctx = &RequestContext{}
	}
	newRctx := &RequestContext{
		Logger:        rctx.Logger,
		CorrelationID: id,
	}
	return WithRequestContext(ctx, newRctx)
}
