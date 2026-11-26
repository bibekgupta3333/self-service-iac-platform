package httpapi

import (
	"context"
	"testing"

	"github.com/google/uuid"

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

// noRoles is a RoleLookup for tests whose callers are platform admins and never need it.
type noRoles struct{}

func (noRoles) HighestRole(context.Context, uuid.UUID) (rbac.Role, error) { return rbac.RoleNone, nil }

func TestRouterRefusesToStartWithoutAPolicyTable(t *testing.T) {
	if err := (Deps{}).Validate(); err == nil {
		t.Fatal("Validate accepted Deps without a policy table")
	}
	if err := (Deps{Policies: DefaultPolicies()}).Validate(); err == nil {
		t.Fatal("Validate accepted Deps without a role lookup")
	}
	if err := (Deps{Policies: DefaultPolicies(), Roles: noRoles{}}).Validate(); err != nil {
		t.Fatalf("Validate rejected complete Deps: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("NewRouter built a router without a policy table")
		}
	}()
	NewRouter(Deps{})
}
