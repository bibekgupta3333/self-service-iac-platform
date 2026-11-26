package httpapi

import (
	"net/http"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

// A valid browser session, even a platform admin's with a matching CSRF token, is never
// accepted on machine routes: they use their own authenticator.
func TestSessionCookieIsRejectedOnMachineRoutes(t *testing.T) {
	f := newFlow(t, true, rbac.RoleAdmin)
	c := f.signIn(t)
	rec := f.store.rows[string(session.HashID(c.Value))]
	rec.PlatformAdmin = true
	f.store.rows[string(session.HashID(c.Value))] = rec

	f.deps.Internal = NewTokenAuthenticator(testInternalToken, releaseBot)
	f.deps.InternalHandlers = []RouteRegistrar{NewLockHandler(nil), NewBudgetContextHandler(nil), NewDriftIngestHandler(nil)}
	f.deps.PipelineHandlers = []RouteRegistrar{NewRunHandler(nil)}
	f.h = NewRouter(f.deps)

	tok := session.CSRFToken(c.Value)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/internal/catalog/index"},
		{"GET", "/v1/internal/budget-context?stack_path=environments/development/a/b"},
		{"GET", "/v1/internal/stacks/00000000-0000-0000-0000-000000000001/lock"},
		{"POST", "/v1/runs"},
		{"POST", "/v1/internal/drift"},
		{"GET", "/v1/internal/drift/stacks"},
	} {
		got := f.req(tc.method, tc.path, "{}", []*http.Cookie{c}, "X-CSRF-Token", tok)
		if got.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a session = %d, want 401: %s", tc.method, tc.path, got.Code, got.Body.String())
		}
	}
	// The machine token is not a session either: it does not open user routes.
	if got := f.req("GET", "/v1/me", "", nil, "Authorization", "Bearer "+testInternalToken); got.Code != http.StatusUnauthorized {
		t.Fatalf("internal token on a user route = %d", got.Code)
	}
}
