package log

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger is a wrapper around *zap.Logger that owns its own AtomicLevel
// so the level can be changed at runtime (see LevelHandler).
//
// Three styles of API are exposed. Prefer the ctx-aware methods on
// the hot path — they auto-inject correlation_id + OTEL trace_id /
// span_id from ctx without cloning the underlying zap core:
//
//   - Debug/Info/Warn/Error/Fatal(ctx, msg, fields...) —
//     ctx-aware, zero allocation when the level is disabled
//   - L() returns the underlying *zap.Logger — the escape hatch for
//     code that wants absolute control and does not need ctx
//     enrichment
//   - S() returns *zap.SugaredLogger — reflection-based "key",value
//     pairs; convenient but slower. Keep for init code and
//     non-hot paths
//
// With(fields...) returns a new Logger with additional baked-in
// fields. Use it for CONSTRUCTION-TIME scoping (e.g. the
// `component="saga_consumer"` field on a struct-owned logger), NOT
// on the per-request hot path — each call clones zap's internal core
// (~1.2 KB). For per-request tags, pass them via the fields slice to
// the ctx-aware method above.
type Logger struct {
	z        *zap.Logger // skip=0, used by L()/S()/With()
	zCtxSkip *zap.Logger // skip=2, used by emit() so caller = user code
	level    zap.AtomicLevel
}

// New builds a Logger from opts. It performs validation but has no
// side effects — in particular it does NOT touch zap's globals. If you
// want zap.S() / zap.L() to route through this logger, call
// zap.ReplaceGlobals(logger.L()) explicitly in main.
func New(opts Options) (*Logger, error) {
	opts.fillDefaults()

	atomic := zap.NewAtomicLevelAt(opts.Level)

	var encoder zapcore.Encoder
	if opts.Development {
		encoder = zapcore.NewConsoleEncoder(opts.EncoderConfig)
	} else {
		encoder = zapcore.NewJSONEncoder(opts.EncoderConfig)
	}

	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(opts.Output),
		atomic,
	)

	if opts.Sampling != nil {
		// zap.SamplingConfig values are per-second (Initial + Thereafter).
		core = zapcore.NewSamplerWithOptions(
			core,
			time.Second,
			opts.Sampling.Initial,
			opts.Sampling.Thereafter,
		)
	}

	zlog := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return &Logger{
		z: zlog,
		// zCtxSkip adds 2 frames to skip (Logger.Error → emit). This
		// makes zap.AddCaller report the USER's file:line when logs are
		// emitted through the ctx-aware methods.
		zCtxSkip: zlog.WithOptions(zap.AddCallerSkip(2)),
		level:    atomic,
	}, nil
}

// L returns the underlying *zap.Logger for the escape-hatch path.
// Prefer the ctx-aware methods (Error/Info/...) where ctx is in
// scope — they emit the same fields plus correlation_id and OTEL
// trace / span ids.
func (l *Logger) L() *zap.Logger { return l.z }

// S returns the sugared logger for ergonomic "key", value call sites.
// Prefer the ctx-aware methods + tag helpers on the hot path — sugar
// uses reflection and allocates per call.
func (l *Logger) S() *zap.SugaredLogger { return l.z.Sugar() }

// With returns a new Logger with the given fields baked in on both
// internal zap loggers (the skip=0 L()/S() view and the skip=2 ctx
// view). The AtomicLevel is shared so runtime level changes propagate.
// The receiver is not modified.
//
// Intended for CONSTRUCTION-TIME scoping (e.g. component labels on
// struct-owned loggers). Avoid on the per-request hot path — each
// call clones zap's internal core (~1.2 KB).
func (l *Logger) With(fields ...zap.Field) *Logger {
	if len(fields) == 0 {
		return l
	}
	return &Logger{
		z:        l.z.With(fields...),
		zCtxSkip: l.zCtxSkip.With(fields...),
		level:    l.level,
	}
}

// Level returns the AtomicLevel backing this logger. Callers can
// SetLevel() on it to change the effective level at runtime without a
// restart. The HTTP endpoint in LevelHandler wires this up.
func (l *Logger) Level() zap.AtomicLevel { return l.level }

// Sync flushes any buffered log entries. Call this from main's shutdown
// path (fx OnStop, defer in simpler apps) before the process exits, or
// the last few log lines may be lost.
//
// Zap's Sync on os.Stdout can return EINVAL on some platforms because
// tty / pipe file descriptors do not support fsync. That is not an
// operational error — callers who care can check the returned error
// but typically ignore it.
func (l *Logger) Sync() error { return l.z.Sync() }

// ─── Ctx-aware methods ──────────────────────────────────────────────
//
// These methods:
//   1. Call Check() first — if the level is disabled the method
//      returns with ZERO allocation; no enrichFields, no field slice.
//   2. On emit, prepend correlation_id from ctx + OTEL trace_id /
//      span_id if an active OTEL span is in ctx.
//   3. Do NOT clone the receiver. One *Logger can serve the entire
//      process; per-request metadata travels via ctx instead.

// Debug logs at DebugLevel with per-call ctx enrichment.
func (l *Logger) Debug(ctx context.Context, msg string, fields ...zap.Field) {
	l.emit(ctx, zapcore.DebugLevel, msg, fields)
}

// Info logs at InfoLevel with per-call ctx enrichment.
func (l *Logger) Info(ctx context.Context, msg string, fields ...zap.Field) {
	l.emit(ctx, zapcore.InfoLevel, msg, fields)
}

// Warn logs at WarnLevel with per-call ctx enrichment.
func (l *Logger) Warn(ctx context.Context, msg string, fields ...zap.Field) {
	l.emit(ctx, zapcore.WarnLevel, msg, fields)
}

// Error logs at ErrorLevel with per-call ctx enrichment.
func (l *Logger) Error(ctx context.Context, msg string, fields ...zap.Field) {
	l.emit(ctx, zapcore.ErrorLevel, msg, fields)
}

// Fatal logs at FatalLevel, then os.Exit(1). Ctx is still enriched so
// the last words include correlation_id + trace_id.
func (l *Logger) Fatal(ctx context.Context, msg string, fields ...zap.Field) {
	l.emit(ctx, zapcore.FatalLevel, msg, fields)
}

// emit is the shared zero-alloc-when-disabled hot path.
func (l *Logger) emit(ctx context.Context, lvl zapcore.Level, msg string, fields []zap.Field) {
	ce := l.zCtxSkip.Check(lvl, msg)
	if ce == nil {
		return
	}
	ce.Write(enrichFields(ctx, fields)...)
}

// enrichFields prepends ctx-carried fields (correlation_id + OTEL
// trace_id / span_id) to the user-supplied fields. When the ctx
// carries neither, it returns fields unchanged — zero allocation.
//
// Field order: correlation_id first, then trace_id, span_id, then
// user fields. This keeps the request identifiers near the start of
// every JSON line, making log exploration easier.
func enrichFields(ctx context.Context, user []zap.Field) []zap.Field {
	if ctx == nil {
		return user
	}

	var cid string
	if v, ok := ctx.Value(ctxKey{}).(ctxValue); ok {
		cid = v.correlationID
	}

	sc := trace.SpanContextFromContext(ctx)
	validSpan := sc.IsValid()

	// Fast path: no enrichment needed.
	if cid == "" && !validSpan {
		return user
	}

	// Grow the slice once to fit up to 3 injected fields.
	out := make([]zap.Field, 0, len(user)+3)
	if cid != "" {
		out = append(out, zap.String(CorrelationIDKey, cid))
	}
	if validSpan {
		out = append(out,
			zap.Stringer("trace_id", sc.TraceID()),
			zap.Stringer("span_id", sc.SpanID()),
		)
	}
	out = append(out, user...)
	return out
}

// ─── Package-level helpers ──────────────────────────────────────────
//
// These pull the Logger out of ctx via FromContext. Use them where
// you don't already hold a *Logger (e.g. request handlers). If you
// already hold a struct-owned logger, prefer the method form so the
// construction-time fields (component=..., etc.) still apply.

// Debug is the package-level ctx-aware shorthand.
func Debug(ctx context.Context, msg string, fields ...zap.Field) {
	FromContext(ctx).emit(ctx, zapcore.DebugLevel, msg, fields)
}

// Info is the package-level ctx-aware shorthand.
func Info(ctx context.Context, msg string, fields ...zap.Field) {
	FromContext(ctx).emit(ctx, zapcore.InfoLevel, msg, fields)
}

// Warn is the package-level ctx-aware shorthand.
func Warn(ctx context.Context, msg string, fields ...zap.Field) {
	FromContext(ctx).emit(ctx, zapcore.WarnLevel, msg, fields)
}

// Error is the package-level ctx-aware shorthand.
func Error(ctx context.Context, msg string, fields ...zap.Field) {
	FromContext(ctx).emit(ctx, zapcore.ErrorLevel, msg, fields)
}

// Fatal is the package-level ctx-aware shorthand.
func Fatal(ctx context.Context, msg string, fields ...zap.Field) {
	FromContext(ctx).emit(ctx, zapcore.FatalLevel, msg, fields)
}
