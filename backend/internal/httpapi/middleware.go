package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
)

type requestIDKey struct{}

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestIDFrom returns the correlation id of the current request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestContext assigns X-Request-Id (echoing a well-formed inbound one) and attaches a
// request-scoped logger to the context.
func requestContext(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if !requestIDRe.MatchString(id) {
				id = uuid.NewString()
			}
			w.Header().Set("X-Request-Id", id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			ctx = observability.WithLogger(ctx, base.With("request_id", id))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// accessLog writes one structured line per request.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		observability.Logger(r.Context()).InfoContext(r.Context(), "http request",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"bytes", ww.BytesWritten(), "duration_ms", time.Since(start).Milliseconds())
	})
}

// recoverer turns panics into 500 problem responses.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				observability.Logger(r.Context()).ErrorContext(r.Context(), "panic", "panic", rec)
				writeError(w, r, errPanic)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type panicError struct{}

func (panicError) Error() string { return "internal panic" }

var errPanic error = panicError{}
