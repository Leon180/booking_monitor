package logger

import (
	"context"

	"go.uber.org/zap"
)

type ctxKey struct{}

// FromCtx returns the SugaredLogger associated with the context.
// If no logger is associated, it returns the global SugaredLogger.
func FromCtx(ctx context.Context) *zap.SugaredLogger {
	if l, ok := ctx.Value(ctxKey{}).(*zap.SugaredLogger); ok {
		return l
	}
	return zap.S()
}

// WithCtx returns a copy of parent context in which the valid SugaredLogger is associated.
func WithCtx(ctx context.Context, l *zap.SugaredLogger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}
