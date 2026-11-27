// Package rbac is the platform's permission matrix: a pure table of role x action x
// environment (docs/07 section 7). Nothing here performs I/O, so it is tested without mocks
// and shared by the HTTP route policies and the request service.
//
// Roles are per team; a platform admin counts as admin everywhere. The role thresholds the
// matrix states for production are in MinRole; the extra production gates the API applies
// (a fresh sign-in for a production destroy) are in production.go. Approvals are enforced by
// GitHub and the pre-apply OPA gate (WBS 8.8).
package rbac

import "slices"

// Role is a team role (enum team_role), ordered from least to most privileged.
type Role string

// Team roles.
const (
	RoleNone       Role = ""
	RoleViewer     Role = "viewer"
	RoleDeveloper  Role = "developer"
	RoleMaintainer Role = "maintainer"
	RoleAdmin      Role = "admin"
)

// Roles lists every real role, least privileged first.
var Roles = []Role{RoleViewer, RoleDeveloper, RoleMaintainer, RoleAdmin}

var roleOrder = []Role{RoleNone, RoleViewer, RoleDeveloper, RoleMaintainer, RoleAdmin}

// Valid reports whether r is one of the four roles.
func (r Role) Valid() bool { return slices.Contains(Roles, r) }

// AtLeast reports whether r grants at least the privileges of min. Unknown roles grant nothing.
func (r Role) AtLeast(min Role) bool {
	ri, mi := slices.Index(roleOrder, r), slices.Index(roleOrder, min)
	return ri >= 0 && mi >= 0 && ri >= mi
}

// Max returns the more privileged of a and b; an unknown role loses to a known one. Pure.
func Max(a, b Role) Role {
	switch {
	case !a.Valid() && !b.Valid():
		return RoleNone
	case !a.Valid():
		return b
	case !b.Valid():
		return a
	case b.AtLeast(a):
		return b
	}
	return a
}

// Action is something a caller can try to do.
type Action string

// Actions. They follow the rows of the docs/07 matrix.
const (
	ActionRead              Action = "read"                           // browse catalog; view team stacks, requests, plans, drift
	ActionRequestCreate     Action = "request.create"                 // create / update / upgrade
	ActionRequestDestroy    Action = "request.destroy"                // create a destroy request
	ActionStatefulDestroy   Action = "request.allow_stateful_destroy" // let a request delete stateful resources
	ActionRequestCancelOwn  Action = "request.cancel_own"             // cancel a request the caller authored
	ActionRequestCancelAny  Action = "request.cancel_any"             // cancel someone else's request
	ActionApprove           Action = "request.approve"                // approve a PR / Environment
	ActionRetry             Action = "request.retry"                  // retry a failed apply, rollback
	ActionTTLExtend         Action = "stack.extend_ttl"               // extend an ephemeral stack's TTL
	ActionDriftReconcile    Action = "drift.reconcile"                // reconcile or codify drift
	ActionDriftIgnore       Action = "drift.ignore"                   // ignore drift
	ActionExceptionRequest  Action = "exception.request"              // request a policy exception
	ActionBudgetSet         Action = "budget.set"                     // set a team budget
	ActionBudgetOverride    Action = "budget.override"                // grant a budget override
	ActionAuditViewTeam     Action = "audit.view_team"                // view the audit log of the caller's team
	ActionAuditViewAll      Action = "audit.view_all"                 // view all audit events, verify the chain, export
	ActionModuleDeprecate   Action = "module.deprecate"               // deprecate a module version
	ActionLockForceRelease  Action = "lock.force_release"             // force-release a stack lock
	ActionEnvironmentManage Action = "environment.manage"             // manage environments and allowed regions
)

// Actions lists every action, for exhaustive tests and docs.
var Actions = []Action{
	ActionRead, ActionRequestCreate, ActionRequestDestroy, ActionStatefulDestroy, ActionRequestCancelOwn, ActionRequestCancelAny,
	ActionApprove, ActionRetry, ActionTTLExtend, ActionDriftReconcile, ActionDriftIgnore,
	ActionExceptionRequest, ActionBudgetSet, ActionBudgetOverride, ActionAuditViewTeam, ActionAuditViewAll,
	ActionModuleDeprecate, ActionLockForceRelease, ActionEnvironmentManage,
}

// Environment names (env_name).
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Resource describes what the action is applied to.
type Resource struct {
	// Environment is the target environment; empty or unknown values are treated as
	// production, the strictest.
	Environment string
	// OwnedBySubject is true when the caller authored the request or PR. Approving your own
	// work is never allowed below admin.
	OwnedBySubject bool
}

// Decision is the outcome of Decide.
type Decision struct {
	Allowed bool
	// Need is the minimum role that would be allowed (RoleNone for an unknown action).
	Need Role
	// Reason names why a request was denied; empty when allowed.
	Reason string
}

// Denial reasons.
const (
	ReasonRole     = "role_too_low"
	ReasonSelf     = "own_work"
	ReasonNoAction = "unknown_action"
	ReasonReauth   = "reauth_required"
)

// MinRole returns the minimum team role that may perform a in env, and false for an
// unknown action. Pure.
func MinRole(a Action, env string) (Role, bool) {
	nonProd := env == EnvDevelopment || env == EnvStaging
	switch a {
	case ActionRead:
		return RoleViewer, true
	case ActionRequestCreate, ActionRequestCancelOwn, ActionTTLExtend, ActionDriftReconcile:
		return RoleDeveloper, true
	case ActionRequestDestroy, ActionStatefulDestroy, ActionApprove:
		if nonProd {
			return RoleDeveloper, true
		}
		return RoleMaintainer, true
	case ActionRequestCancelAny, ActionRetry, ActionDriftIgnore, ActionExceptionRequest, ActionAuditViewTeam:
		return RoleMaintainer, true
	case ActionBudgetSet, ActionBudgetOverride, ActionAuditViewAll, ActionModuleDeprecate,
		ActionLockForceRelease, ActionEnvironmentManage:
		return RoleAdmin, true
	}
	return RoleNone, false
}

// Decide evaluates the matrix for a caller holding role. Unknown actions and unknown roles
// are denied. Pure.
func Decide(role Role, a Action, res Resource) Decision {
	need, ok := MinRole(a, res.Environment)
	if !ok {
		return Decision{Reason: ReasonNoAction}
	}
	if !role.Valid() || !role.AtLeast(need) {
		return Decision{Need: need, Reason: ReasonRole}
	}
	if a == ActionApprove && res.OwnedBySubject && role != RoleAdmin {
		return Decision{Need: need, Reason: ReasonSelf}
	}
	return Decision{Allowed: true, Need: need}
}

// Allowed is Decide(...).Allowed.
func Allowed(role Role, a Action, res Resource) bool { return Decide(role, a, res).Allowed }

// Permissions lists the actions role may perform in env on work it did not author. Pure.
func Permissions(role Role, env string) []Action {
	var out []Action
	for _, a := range Actions {
		if Allowed(role, a, Resource{Environment: env}) {
			out = append(out, a)
		}
	}
	return out
}
