// Package httpapi is the HTTP adapter: chi router, middleware, problem+json errors
// and thin handlers (decode, call service, encode).
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ratelimit"
)

// Pinger reports whether a dependency is reachable (the database pool in production).
type Pinger interface {
	Ping(ctx context.Context) error
}

// RouteRegistrar mounts a handler's routes.
type RouteRegistrar interface {
	Routes(r chi.Router)
}

// Deps are the collaborators the router needs.
type Deps struct {
	Logger *slog.Logger
	// Auth authenticates users on /v1.
	Auth Authenticator
	// Internal authenticates automation on /v1/internal; nil means DenyAuthenticator.
	Internal Authenticator
	Ready    Pinger
	Catalog  *CatalogHandler
	// Public lists further handlers mounted under authenticated /v1.
	Public []RouteRegistrar
	// InternalHandlers are mounted under /v1/internal behind the Internal authenticator.
	InternalHandlers []RouteRegistrar
	// PipelineHandlers are GitHub Actions callbacks outside /internal (POST /v1/runs);
	// they also use the Internal authenticator, never the user one.
	PipelineHandlers []RouteRegistrar
	// Webhooks authenticate each delivery themselves (HMAC signature), so they are mounted
	// under /v1 without the user or machine authenticator.
	Webhooks []RouteRegistrar
	// Metrics, when set, instruments every request and serves GET /metrics behind
	// MetricsAccess (loopback or the metrics token).
	Metrics       MetricsSource
	MetricsAccess MetricsAccess
	// PublicHandlers are unauthenticated user-facing routes under /v1 (OAuth login).
	PublicHandlers []RouteRegistrar
	// Policies is the route RBAC table enforced on the authenticated user routes; Roles
	// resolves team roles for ScopeAnyTeam. DenialAudit, when set, records rbac.denied.
	Policies    PolicyTable
	Roles       RoleLookup
	DenialAudit EventRecorder
	// DenialLimiter bounds denial audit events per user (nil: DenialAuditPerMinute).
	DenialLimiter *ratelimit.Limiter
}

// MetricsSource records requests and serves the exposition (observability.Metrics).
type MetricsSource interface {
	RequestObserver
	Handler() http.Handler
}

var probeMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions}

// allowedMethods lists the methods r routes for path, for the Allow header.
func allowedMethods(r chi.Routes, path string) []string {
	return fp.Filter(probeMethods, func(m string) bool { return r.Match(chi.NewRouteContext(), m, path) })
}

// NewRouter builds the HTTP handler tree.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	// accessLog wraps recoverer so requests that panic are still logged with their 500.
	r.Use(requestContext(d.Logger))
	if d.Metrics != nil {
		r.Use(instrument(d.Metrics)) // outside recoverer, so panics count as 500s
	}
	r.Use(accessLog, recoverer)
	if d.Metrics != nil {
		r.Get("/metrics", metricsHandler(d.Metrics.Handler(), d.MetricsAccess))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := d.Ready.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "up"})
	})

	internal := d.Internal
	if internal == nil {
		internal = DenyAuthenticator{}
	}
	r.Route("/v1", func(v1 chi.Router) {
		// Machine endpoints never fall under the user authenticator (dev mode included).
		v1.Route("/internal", func(in chi.Router) {
			in.Use(requireAuth(internal))
			if d.Catalog != nil {
				d.Catalog.InternalRoutes(in)
			}
			for _, h := range d.InternalHandlers {
				h.Routes(in)
			}
		})
		for _, h := range d.Webhooks {
			h.Routes(v1)
		}
		v1.Group(func(pipeline chi.Router) {
			pipeline.Use(requireAuth(internal))
			for _, h := range d.PipelineHandlers {
				h.Routes(pipeline)
			}
		})
		v1.Group(func(public chi.Router) {
			for _, h := range d.PublicHandlers {
				h.Routes(public)
			}
		})
		v1.Group(func(user chi.Router) {
			user.Use(requireAuth(d.Auth))
			if d.DenialAudit != nil {
				user.Use(auditDenials(d.DenialAudit, d.DenialLimiter))
			}
			if len(d.Policies) > 0 {
				user.Use(authorize(d.Policies, d.Roles))
			}
			if d.Catalog != nil {
				d.Catalog.Routes(user)
			}
			for _, h := range d.Public {
				h.Routes(user)
			}
		})
	})
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeError(w, req, apperr.NotFound("no route for %s %s", req.Method, req.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		allowed := allowedMethods(r, req.URL.Path)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeError(w, req, &apperr.Error{Kind: apperr.KindMethodNotAllowed, Code: "method_not_allowed",
			Message: fmt.Sprintf("%s is not allowed on %s; allowed: %s", req.Method, req.URL.Path, strings.Join(allowed, ", "))})
	})
	return r
}
