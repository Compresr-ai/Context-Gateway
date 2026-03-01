// Package monitoring - logger.go provides structured logging via zerolog.
//
// DESIGN: Thin wrapper around zerolog with:
//   - Configurable level, format (json/console), output (stdout/file)
//   - Global() sets the default logger for the entire application
//   - Request ID context helpers for request tracing
package monitoring

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Context keys for request tracking.
type contextKey string

const RequestIDKey contextKey = "request_id"

// Logger wraps zerolog.Logger.
type Logger struct {
	zl zerolog.Logger
}

// New creates a new Logger with the given configuration.
func New(cfg LoggerConfig) *Logger {
	zerolog.TimeFieldFormat = time.RFC3339

	level := zerolog.InfoLevel
	levelName := strings.ToLower(strings.TrimSpace(cfg.Level))
	if levelName == "off" || levelName == "none" || levelName == "disabled" {
		level = zerolog.Disabled
	} else {
		parsed, err := zerolog.ParseLevel(cfg.Level)
		if err == nil {
			level = parsed
		}
	}

	var writer io.Writer
	switch cfg.Output {
	case "stdout", "":
		writer = os.Stdout
	case "stderr":
		writer = os.Stderr
	default:
		f, err := os.OpenFile(cfg.Output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			writer = os.Stdout
		} else {
			writer = f
		}
	}

	if cfg.Format == "console" {
		writer = zerolog.ConsoleWriter{Out: writer, TimeFormat: "15:04:05"}
	}

	zl := zerolog.New(writer).Level(level).With().Timestamp().Logger()
	return &Logger{zl: zl}
}

// Global sets the global zerolog logger.
func Global(cfg LoggerConfig) {
	logger := New(cfg)
	log.Logger = logger.zl
}

// Debug returns a debug event.
func (l *Logger) Debug() *zerolog.Event { return l.zl.Debug() }

// Info returns an info event.
func (l *Logger) Info() *zerolog.Event { return l.zl.Info() }

// Warn returns a warn event.
func (l *Logger) Warn() *zerolog.Event { return l.zl.Warn() }

// Error returns an error event.
func (l *Logger) Error() *zerolog.Event { return l.zl.Error() }

// Fatal returns a fatal event.
func (l *Logger) Fatal() *zerolog.Event { return l.zl.Fatal() }

// RequestIDFromContext retrieves the request ID from context.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

// WithRequestIDContext returns a new context with the request ID.
func WithRequestIDContext(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}
