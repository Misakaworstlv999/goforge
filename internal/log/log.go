// Package log is the cross-cutting structured-logging seam (M7-004). It defines a
// narrow Logger interface so the edge layer (Ring 5: internal/cli, pkg/server)
// can emit structured logs without coupling to a concrete library, with a
// zap-backed default and a no-op for tests. It is injected via constructor
// options — not a global — and inner rings do not depend on it.
package log

import (
	"io"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger is the minimal structured logger the edge layer depends on. Variadic
// kv are alternating key/value pairs (e.g. "run_id", id, "op", op), matching
// zap's SugaredLogger "w" methods.
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}

// zapLogger adapts a zap SugaredLogger to Logger.
type zapLogger struct{ s *zap.SugaredLogger }

func (l zapLogger) Debug(msg string, kv ...any) { l.s.Debugw(msg, kv...) }
func (l zapLogger) Info(msg string, kv ...any)  { l.s.Infow(msg, kv...) }
func (l zapLogger) Warn(msg string, kv ...any)  { l.s.Warnw(msg, kv...) }
func (l zapLogger) Error(msg string, kv ...any) { l.s.Errorw(msg, kv...) }

// New builds a zap-backed Logger writing to w at the given minimum level
// (debug|info|warn|error; unknown ⇒ info) in the given format (json | console;
// unknown ⇒ console). w is typically os.Stderr.
func New(level, format string, w io.Writer) Logger {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	var enc zapcore.Encoder
	if format == "json" {
		enc = zapcore.NewJSONEncoder(cfg)
	} else {
		enc = zapcore.NewConsoleEncoder(cfg)
	}
	core := zapcore.NewCore(enc, zapcore.AddSync(w), parseLevel(level))
	return zapLogger{s: zap.New(core).Sugar()}
}

func parseLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// nopLogger discards all logs. Used as the default when none is injected, so
// logging is opt-in and tests stay quiet.
type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// Nop returns a Logger that discards everything.
func Nop() Logger { return nopLogger{} }
