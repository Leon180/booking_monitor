package logger

import (
	"booking_monitor/internal/infrastructure/config"
	"os"

	"go.uber.org/fx"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a new Zap SugaredLogger with JSON encoding and the specified log level.
func New(level string) *zap.SugaredLogger {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // Human readable time
	encoderConfig.TimeKey = "time"
	encoderConfig.LevelKey = "level"
	encoderConfig.MessageKey = "msg"

	var l zapcore.Level
	switch level {
	case "debug":
		l = zap.DebugLevel
	case "info":
		l = zap.InfoLevel
	case "warn":
		l = zap.WarnLevel
	case "error":
		l = zap.ErrorLevel
	default:
		l = zap.InfoLevel
	}

	atomicLevel := zap.NewAtomicLevelAt(l)

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.Lock(os.Stdout),
		atomicLevel,
	)

	// AddCaller() adds file/line number.
	logger := zap.New(core, zap.AddCaller())
	sugar := logger.Sugar()
	// Register as the global logger so zap.S() / zap.L() work for
	// background goroutines that don't carry a context-injected logger.
	zap.ReplaceGlobals(logger)
	return sugar
}

// Module exports the Logger module
var Module = fx.Module("logger",
	fx.Provide(func(cfg *config.Config) *zap.SugaredLogger {
		return New(cfg.App.LogLevel)
	}),
)
