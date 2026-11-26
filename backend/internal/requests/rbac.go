package requests

import "slices"

// Role is a team role (enum team_role), ordered from least to most privileged.
type Role string

// Team roles (docs/07 RBAC matrix).
const (
	RoleNone       Role = ""
	RoleViewer     Role = "viewer"
	RoleDeveloper  Role = "developer"
	RoleMaintainer Role = "maintainer"
	RoleAdmin      Role = "admin"
)

var roleOrder = []Role{RoleNone, RoleViewer, RoleDeveloper, RoleMaintainer, RoleAdmin}

// AtLeast reports whether r grants at least the privileges of min. Unknown roles grant nothing.
func (r Role) AtLeast(min Role) bool {
	ri, mi := slices.Index(roleOrder, r), slices.Index(roleOrder, min)
	return ri >= 0 && mi >= 0 && ri >= mi
}

// RequiredRole returns the minimum team role needed to create a request of kind k
// in environment env. Pure. (docs/03 §6.4: developer for any environment; maintainer
// for production destroys.)
func RequiredRole(k Kind, env string) Role {
	if k == KindDestroy && env == "production" {
		return RoleMaintainer
	}
	return RoleDeveloper
}
