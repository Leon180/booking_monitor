package log

import "go.uber.org/zap"

// NewNop returns a Logger that discards every write. Intended for
// tests and for background paths that haven't been wired with a real
// logger yet (the Nop fallback in FromContext makes missing injection
// fail silently rather than panicking — production code should still
// pass a real logger).
//
// The returned Logger's AtomicLevel is set to ErrorLevel so even a
// caller who does SetLevel(Debug) on it cannot accidentally turn the
// Nop into something that emits.
func NewNop() *Logger {
	z := zap.NewNop()
	return &Logger{
		z:        z,
		zCtxSkip: z, // Nop ignores everything; skip adjustment is irrelevant.
		level:    zap.NewAtomicLevelAt(ErrorLevel),
	}
}
