// Package observability builds the structured JSON logger shared by all components.
package observability

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey struct{}

// NewLogger returns a JSON slog logger writing to w at the given level.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})).
		With("service", "iacp-api")
}

// WithLogger stores l in ctx so request-scoped attributes travel with the context.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Logger returns the request-scoped logger from ctx, or slog.Default.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
