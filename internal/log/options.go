package log

import (
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Options configures a new Logger. The zero value is NOT valid — at
// minimum Level must be set. Fields with nil/zero values receive
// sensible production defaults via fillDefaults.
type Options struct {
	// Level is the minimum log level emitted. Use ParseLevel to convert
	// from a config string.
	Level Level

	// EncoderConfig customises field names, time format, etc. If zero,
	// a production JSON config with ISO8601 time is used.
	EncoderConfig zapcore.EncoderConfig

	// Output is where log entries are written. Nil means os.Stdout.
	// Tests typically pass a *bytes.Buffer here.
	Output io.Writer

	// Sampling controls log sampling. Nil means no sampling (every log
	// line is written). At very high log volume consider setting this.
	Sampling *zap.SamplingConfig

	// Development switches to the human-readable console encoder with
	// coloured levels and multi-line stack traces. Intended only for
	// local development; production should keep this false so logs stay
	// machine-parseable JSON.
	Development bool
}

// encoderConfigIsZero reports whether an EncoderConfig was left as its
// zero value so fillDefaults knows to overwrite it. We can't use == on
// the struct directly because it contains functions (EncodeTime etc.)
// that are not comparable, so we spot-check a key field.
func encoderConfigIsZero(ec zapcore.EncoderConfig) bool {
	return ec.TimeKey == "" && ec.LevelKey == "" && ec.MessageKey == ""
}

// fillDefaults mutates opts to fill in any zero-valued field with its
// production default.
func (opts *Options) fillDefaults() {
	if encoderConfigIsZero(opts.EncoderConfig) {
		opts.EncoderConfig = defaultEncoderConfig()
	}
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
}

// defaultEncoderConfig mirrors zap's NewProductionEncoderConfig but
// renames Time/Level/Message keys to match the previous logger output
// so downstream log shippers (Grafana, Loki, Elastic) don't need
// reindexing when this refactor ships.
func defaultEncoderConfig() zapcore.EncoderConfig {
	ec := zap.NewProductionEncoderConfig()
	ec.EncodeTime = zapcore.ISO8601TimeEncoder
	ec.TimeKey = "time"
	ec.LevelKey = "level"
	ec.MessageKey = "msg"
	return ec
}
