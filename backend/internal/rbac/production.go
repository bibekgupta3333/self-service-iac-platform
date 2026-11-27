package rbac

import "time"

// Production gates (docs/07 section 7, WBS 8.8). The role thresholds of the matrix are in
// MinRole; the gates below are the extra checks a production change meets in the API. The
// approvals themselves (one maintainer who is not the author, two for a destructive change,
// never the iacp-approver machine user) are enforced by GitHub (CODEOWNERS, the production
// Environment) and again by the pre-apply OPA gate (policies/opa/prod_approval.rego), because
// the API never sees a review.

// ReauthWindow is how recent the caller's sign-in must be for an action that needs a fresh
// authentication (NeedsFreshAuth). A session older than this must sign in again.
const ReauthWindow = 15 * time.Minute

// clockSkew tolerates an authentication time slightly in the future (another replica's clock).
const clockSkew = time.Minute

// IsProduction reports whether env is held to the production rules: production itself and
// every unknown or empty environment (fail closed, as in MinRole). Pure.
func IsProduction(env string) bool {
	return env != EnvDevelopment && env != EnvStaging
}

// NeedsFreshAuth reports whether action a in env requires a recent sign-in: destroying a
// production stack (docs/07 section 2, "re-auth for prod destroy"). Pure.
func NeedsFreshAuth(a Action, env string) bool {
	return a == ActionRequestDestroy && IsProduction(env)
}

// FreshAuth reports whether a sign-in at authAt is recent enough at now: within window, and
// not further in the future than the tolerated clock skew. A zero authAt (a caller with no
// known sign-in time, such as a machine identity) is never fresh. Pure.
func FreshAuth(authAt, now time.Time, window time.Duration) bool {
	if authAt.IsZero() || authAt.After(now.Add(clockSkew)) {
		return false
	}
	return now.Sub(authAt) <= window
}

// DecideFresh is Decide followed by the fresh-authentication gate: an action that
// NeedsFreshAuth is denied with ReasonReauth unless the caller signed in within ReauthWindow.
// The role check comes first, so a caller who could never act is told about the role, not
// asked to sign in again. Pure.
func DecideFresh(role Role, a Action, res Resource, authAt, now time.Time) Decision {
	d := Decide(role, a, res)
	if !d.Allowed || !NeedsFreshAuth(a, res.Environment) {
		return d
	}
	if !FreshAuth(authAt, now, ReauthWindow) {
		return Decision{Need: d.Need, Reason: ReasonReauth}
	}
	return d
}
