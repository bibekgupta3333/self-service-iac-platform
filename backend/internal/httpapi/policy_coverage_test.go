package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// exemptFromUserPolicy reports why a route does not need a DefaultPolicies entry, or "".
// Only routes that never pass through the user authenticator qualify.
func exemptFromUserPolicy(path string) string {
	switch {
	case path == "/healthz" || path == "/readyz" || path == "/metrics":
		return "probe"
	case path == "/v1/auth/login" || path == "/v1/auth/callback" || path == "/v1/auth/logout":
		return "public login flow"
	case strings.HasPrefix(path, "/v1/internal/") || path == "/v1/runs":
		return "machine route (OIDC or internal token, never a session)"
	case strings.HasPrefix(path, "/v1/webhooks/"):
		return "webhook (HMAC signature)"
	}
	return ""
}

// TestEveryUserRouteHasAPolicy walks the real router: each route is either exempt for a
// stated reason or has a DefaultPolicies entry, and every entry names a real route.
func TestEveryUserRouteHasAPolicy(t *testing.T) {
	ah := &AuthHandler{Login: &flowLogin{}, Profiles: flowProfiles{}}
	h := NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DenyAuthenticator{}, Ready: okPinger{},
		Catalog: NewCatalogHandler(nil),
		Public: []RouteRegistrar{NewRequestHandler(nil, nil), NewRequestEventsHandler(nil, StreamOptions{}),
			NewStackHandler(nil), NewTTLHandler(nil), NewAuditHandler(nil), ah},
		PublicHandlers:   []RouteRegistrar{ah.Public()},
		InternalHandlers: []RouteRegistrar{NewLockHandler(nil), NewBudgetContextHandler(nil), NewTTLReaperHandler(nil)},
		PipelineHandlers: []RouteRegistrar{NewRunHandler(nil)},
		Webhooks:         []RouteRegistrar{NewWebhookHandler(nil, "", nil)},
		Policies:         DefaultPolicies(),
		Roles:            noRoles{},
	})
	mux, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("router is not a chi.Routes")
	}
	policies := DefaultPolicies()
	seen := map[string]bool{}
	var missing []string
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.ReplaceAll(route, "/*/", "/")
		key := method + " " + route
		seen[key] = true
		if exemptFromUserPolicy(route) != "" {
			return nil
		}
		if _, ok := policies[key]; !ok {
			missing = append(missing, key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("routes without a DefaultPolicies entry (add one, or exempt a non-user route):\n  %s", strings.Join(missing, "\n  "))
	}
	for key := range policies {
		if !seen[key] {
			t.Errorf("DefaultPolicies has an entry for a route that does not exist: %s", key)
		}
	}
}
