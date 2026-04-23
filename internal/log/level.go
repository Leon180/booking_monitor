package log

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

// Level is an alias for zapcore.Level so callers don't have to import
// zapcore just to hold or compare a log level.
type Level = zapcore.Level

// Level constants re-exported for convenience.
const (
	DebugLevel Level = zapcore.DebugLevel
	InfoLevel  Level = zapcore.InfoLevel
	WarnLevel  Level = zapcore.WarnLevel
	ErrorLevel Level = zapcore.ErrorLevel
	FatalLevel Level = zapcore.FatalLevel
)

// ParseLevel converts a config string to a Level. Returns an error for
// unrecognized values instead of silently falling back — a typo in
// config (e.g. "wanr") should fail startup, not become info level.
//
// Accepted values: "debug", "info", "warn", "error", "dpanic", "panic", "fatal".
// Delegates to zapcore's own UnmarshalText so we stay in sync with zap.
func ParseLevel(s string) (Level, error) {
	if s == "" {
		return 0, fmt.Errorf("log: level is empty (use debug|info|warn|error|fatal)")
	}
	var lvl Level
	if err := lvl.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("log: invalid level %q (use debug|info|warn|error|fatal): %w", s, err)
	}
	return lvl, nil
}
