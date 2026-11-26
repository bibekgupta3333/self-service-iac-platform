package requests

import "github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"

// Role is a team role. The matrix lives in package rbac; these aliases keep the request
// service's vocabulary.
type Role = rbac.Role

// Team roles (docs/07 RBAC matrix).
const (
	RoleNone       = rbac.RoleNone
	RoleViewer     = rbac.RoleViewer
	RoleDeveloper  = rbac.RoleDeveloper
	RoleMaintainer = rbac.RoleMaintainer
	RoleAdmin      = rbac.RoleAdmin
)

// ActionFor maps a request kind to the RBAC action needed to create it. Pure.
func ActionFor(k Kind) rbac.Action {
	if k == KindDestroy {
		return rbac.ActionRequestDestroy
	}
	return rbac.ActionRequestCreate
}

// RequiredRole returns the minimum team role needed to create a request of kind k
// in environment env, from the rbac matrix. Pure. (docs/03 §6.4: developer for any
// environment; maintainer for production destroys.)
func RequiredRole(k Kind, env string) Role {
	need, _ := rbac.MinRole(ActionFor(k), env)
	return need
}
