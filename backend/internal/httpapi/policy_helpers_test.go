package httpapi

import (
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// testPolicies is DefaultPolicies plus the routes the router tests register themselves.
// Tests that are not about RBAC use it with devPrincipal, a platform admin.
func testPolicies() PolicyTable {
	t := DefaultPolicies()
	for _, r := range []string{"GET /v1/things/{id}", "GET /v1/boom", "GET /v1/panic", "GET /v1/panic-in-tx"} {
		t[r] = RoutePolicy{Action: rbac.ActionRead, Scope: ScopeAuthenticated}
	}
	return t
}

func TestRouterRefusesToStartWithoutAPolicyTable(t *testing.T) {
	if err := (Deps{}).Validate(); err == nil {
		t.Fatal("Validate accepted Deps without a policy table")
	}
	if err := (Deps{Policies: DefaultPolicies()}).Validate(); err != nil {
		t.Fatalf("Validate rejected Deps with a policy table: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("NewRouter built a router without a policy table")
		}
	}()
	NewRouter(Deps{})
}
