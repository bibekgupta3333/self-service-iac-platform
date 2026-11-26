package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
)

// RequestObserver records finished requests (implemented by observability.Metrics).
type RequestObserver interface {
	ObserveRequest(method, route string, status int, elapsed time.Duration)
}

// instrument records every request by its chi route pattern, read after routing, so
// /v1/requests/{id} is one series whatever the id. Unmatched paths share one label.
func instrument(obs RequestObserver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			pattern := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				pattern = rctx.RoutePattern()
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // handler wrote nothing: net/http sends 200
			}
			obs.ObserveRequest(r.Method, observability.RouteLabel(pattern), status, time.Since(start))
		})
	}
}

// MetricsAccess decides who may scrape /metrics. The endpoint shares the API listener,
// so it is closed by default: a direct loopback connection (a sidecar scraper in the
// same task or pod) is allowed, and anyone else needs the dedicated metrics token
// (IACP_METRICS_TOKEN). A request carrying proxy forwarding headers is never treated
// as loopback, because a local reverse proxy would make every client look local.
type MetricsAccess struct {
	digest  [sha256.Size]byte
	enabled bool
}

// NewMetricsAccess returns the policy; an empty token allows loopback only.
func NewMetricsAccess(token string) MetricsAccess {
	return MetricsAccess{digest: sha256.Sum256([]byte(token)), enabled: token != ""}
}

// Allow reports whether r may read /metrics.
func (a MetricsAccess) Allow(r *http.Request) bool {
	if presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && presented != "" {
		got := sha256.Sum256([]byte(presented))
		return a.enabled && subtle.ConstantTimeCompare(got[:], a.digest[:]) == 1
	}
	return IsDirectLoopback(r.RemoteAddr, r.Header)
}

// forwardingHeaders mark a request relayed by a proxy.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"}

// IsDirectLoopback reports whether remoteAddr is a loopback peer and no proxy
// forwarding header is present. Pure.
func IsDirectLoopback(remoteAddr string, h http.Header) bool {
	for _, k := range forwardingHeaders {
		if h.Get(k) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// metricsHandler guards h with access.
func metricsHandler(h http.Handler, access MetricsAccess) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !access.Allow(r) {
			writeError(w, r, &apperr.Error{Kind: apperr.KindForbidden, Code: "metrics_forbidden",
				Message: "metrics are served to loopback scrapers or callers with the metrics token"})
			return
		}
		h.ServeHTTP(w, r)
	}
}
