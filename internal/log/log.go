package log

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger is a wrapper around *zap.Logger that owns its own AtomicLevel
// so the level can be changed at runtime (see LevelHandler).
//
// Three APIs are exposed:
//
//   - L() returns the underlying *zap.Logger — the zero-allocation fast
//     path, used with zap.Field helpers (or this package's tag/ helpers).
//   - S() returns the sugared *zap.SugaredLogger — the ergonomic
//     reflection-based path, kept for gradual migration of call sites
//     that still use "key", value pairs.
//   - With(fields...) returns a new Logger with additional baked-in
//     fields. The receiver is never mutated.
//
// Construct via New(); do not instantiate the struct directly.
type Logger struct {
	z     *zap.Logger
	level zap.AtomicLevel
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
		// See zap docs on SamplingConfig.
		core = zapcore.NewSamplerWithOptions(
			core,
			time.Second,
			opts.Sampling.Initial,
			opts.Sampling.Thereafter,
		)
	}

	zlog := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return &Logger{z: zlog, level: atomic}, nil
}

// L returns the underlying *zap.Logger for the hot, zero-alloc path.
//
//	log.L().Error("booking failed", tag.Error(err), tag.OrderID(id))
func (l *Logger) L() *zap.Logger {
	return l.z
}

// S returns the sugared logger for ergonomic "key", value call sites.
// Prefer L() + tag helpers on the hot path — sugar uses reflection and
// allocates per call.
//
//	log.S().Infow("processed", "msg_id", id, "latency_ms", ms)
func (l *Logger) S() *zap.SugaredLogger {
	return l.z.Sugar()
}

// With returns a new Logger with the given fields baked in. The
// AtomicLevel is shared between the original and the derived logger
// so runtime level changes propagate. The receiver is not modified.
func (l *Logger) With(fields ...zap.Field) *Logger {
	if len(fields) == 0 {
		return l
	}
	return &Logger{
		z:     l.z.With(fields...),
		level: l.level,
	}
}

// Level returns the AtomicLevel backing this logger. Callers can
// SetLevel() on it to change the effective level at runtime without a
// restart. The HTTP endpoint in LevelHandler wires this up.
func (l *Logger) Level() zap.AtomicLevel {
	return l.level
}

// Sync flushes any buffered log entries. Call this from main's shutdown
// path (fx OnStop, defer in simpler apps) before the process exits, or
// the last few log lines may be lost.
//
// Zap's Sync on os.Stdout can return EINVAL on some platforms because
// tty / pipe file descriptors do not support fsync. That is not an
// operational error — callers who care can check the returned error
// but typically ignore it.
func (l *Logger) Sync() error {
	return l.z.Sync()
}
