// Package httpapi is the HTTP adapter: chi router, middleware, problem+json errors
// and thin handlers (decode, call service, encode).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
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
}

// NewRouter builds the HTTP handler tree.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	// accessLog wraps recoverer so requests that panic are still logged with their 500.
	r.Use(requestContext(d.Logger), accessLog, recoverer)

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
		})
		v1.Group(func(user chi.Router) {
			user.Use(requireAuth(d.Auth))
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
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	return r
}
