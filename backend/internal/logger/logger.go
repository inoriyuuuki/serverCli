// Package logger configures the process-wide structured logger and provides
// context helpers for request_id correlation.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// New builds a slog logger writing JSON to w at the given level.
func New(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// NewDefault returns a logger to stderr at info level.
func NewDefault() *slog.Logger { return New(os.Stderr, "info") }

// WithRequestID returns a context carrying requestID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, requestID)
}

// RequestID extracts the request ID from ctx, or empty.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// WithCtx returns a logger that attaches request_id and any extra attrs.
func WithCtx(log *slog.Logger, ctx context.Context, attrs ...slog.Attr) *slog.Logger {
	if rid := RequestID(ctx); rid != "" {
		attrs = append(attrs, slog.String("request_id", rid))
	}
	anyAttrs := make([]any, len(attrs))
	for i, a := range attrs {
		anyAttrs[i] = a
	}
	return log.With(anyAttrs...)
}
