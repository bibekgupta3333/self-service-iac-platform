// Package teamsync turns GitHub team membership into platform memberships.
//
// Mapping (docs/07 section 7), by GitHub team slug, where <slug> is a platform team's
// github_team_slug:
//
//	iacp-<slug>-viewers  -> viewer
//	<slug>               -> developer
//	<slug>-maintainers   -> maintainer
//	<admin team>         -> platform admin (default iacp-admins)
//
// A person in several of a team's GitHub teams gets the highest role. The mapping and the
// diff against current memberships are pure; Syncer applies them in one transaction.
package teamsync

import (
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Team is a platform team as far as the mapping is concerned.
type Team struct {
	ID         uuid.UUID
	Slug       string // platform slug (audit and logs)
	GitHubSlug string // github_team_slug
}

// Source values for memberships.source.
const (
	SourceSync   = "github_sync"
	SourceManual = "manual"
)

// Membership is one row of memberships.
type Membership struct {
	TeamID uuid.UUID
	Role   rbac.Role
	Source string
}

// Mapping holds the configurable parts of the role mapping.
type Mapping struct {
	// AdminTeam is the GitHub team slug whose members are platform admins.
	AdminTeam string
}

// Suffixes and prefixes of the GitHub team naming convention.
const (
	viewerPrefix     = "iacp-"
	viewerSuffix     = "-viewers"
	maintainerSuffix = "-maintainers"
)

// DesiredRoles returns the role per platform team implied by the user's GitHub team slugs.
// Slugs are compared case-insensitively. Teams with no matching GitHub team are absent. Pure.
func DesiredRoles(teams []Team, ghSlugs []string) map[uuid.UUID]rbac.Role {
	have := make(map[string]struct{}, len(ghSlugs))
	for _, s := range ghSlugs {
		have[strings.ToLower(s)] = struct{}{}
	}
	in := func(s string) bool { _, ok := have[strings.ToLower(s)]; return ok }

	out := map[uuid.UUID]rbac.Role{}
	for _, t := range teams {
		if t.GitHubSlug == "" {
			continue
		}
		role := rbac.RoleNone
		if in(viewerPrefix + t.GitHubSlug + viewerSuffix) {
			role = rbac.Max(role, rbac.RoleViewer)
		}
		if in(t.GitHubSlug) {
			role = rbac.Max(role, rbac.RoleDeveloper)
		}
		if in(t.GitHubSlug + maintainerSuffix) {
			role = rbac.Max(role, rbac.RoleMaintainer)
		}
		if role != rbac.RoleNone {
			out[t.ID] = role
		}
	}
	return out
}

// IsAdmin reports whether the user belongs to the admin team. Pure.
func (m Mapping) IsAdmin(ghSlugs []string) bool {
	if m.AdminTeam == "" {
		return false
	}
	for _, s := range ghSlugs {
		if strings.EqualFold(s, m.AdminTeam) {
			return true
		}
	}
	return false
}

// Change is one membership difference.
type Change struct {
	TeamID uuid.UUID
	From   rbac.Role // RoleNone when added
	To     rbac.Role // RoleNone when removed
}

// Plan diffs the desired roles against current memberships. Manual memberships are never
// touched, in either direction: a manual row for a team blocks sync for that team. Pure.
func Plan(current []Membership, desired map[uuid.UUID]rbac.Role) (changes []Change) {
	seen := map[uuid.UUID]bool{}
	for _, c := range current {
		seen[c.TeamID] = true
		if c.Source == SourceManual {
			continue
		}
		want, ok := desired[c.TeamID]
		switch {
		case !ok:
			changes = append(changes, Change{TeamID: c.TeamID, From: c.Role, To: rbac.RoleNone})
		case want != c.Role:
			changes = append(changes, Change{TeamID: c.TeamID, From: c.Role, To: want})
		}
	}
	for id, want := range desired {
		if !seen[id] {
			changes = append(changes, Change{TeamID: id, From: rbac.RoleNone, To: want})
		}
	}
	slices.SortFunc(changes, func(a, b Change) int { return strings.Compare(a.TeamID.String(), b.TeamID.String()) })
	return changes
}

// AdminDecision is the outcome of reconciling users.is_platform_admin with the admin team.
type AdminDecision struct {
	Admin   bool // the value to store
	Changed bool
	// Blocked is true when removing admin was prevented because the user is the last one.
	Blocked bool
}

// ReconcileAdmin decides the stored admin flag. Granting is always allowed. Revoking is
// refused when the user is the last active platform admin, so the platform can never be left
// without one by a sync. Pure.
func ReconcileAdmin(has, want bool, otherActiveAdmins int) AdminDecision {
	switch {
	case has == want:
		return AdminDecision{Admin: has}
	case want:
		return AdminDecision{Admin: true, Changed: true}
	case otherActiveAdmins == 0:
		return AdminDecision{Admin: true, Blocked: true}
	}
	return AdminDecision{Admin: false, Changed: true}
}
