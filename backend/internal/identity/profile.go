package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Profile is a user as shown by GET /v1/me.
type Profile struct {
	ID              uuid.UUID
	Login           string
	DisplayName     string
	Email           string
	IsPlatformAdmin bool
	Teams           []TeamRole
}

// TeamRole is the caller's role in one team.
type TeamRole struct {
	Slug string
	Role rbac.Role
}

// Profile loads a user with their team memberships. A disabled or unknown user is not found.
func (s *UserStore) Profile(ctx context.Context, userID uuid.UUID) (Profile, error) {
	var p Profile
	err := s.pool.QueryRow(ctx, `
		SELECT id, github_login::text, coalesce(display_name, github_login::text), coalesce(email::text, ''), is_platform_admin
		FROM users WHERE id = $1 AND disabled_at IS NULL`, userID).
		Scan(&p.ID, &p.Login, &p.DisplayName, &p.Email, &p.IsPlatformAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, apperr.NotFound("user not found")
	}
	if err != nil {
		return Profile{}, fmt.Errorf("load profile: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.slug, m.role::text FROM memberships m JOIN teams t ON t.id = m.team_id
		WHERE m.user_id = $1 ORDER BY t.slug`, userID)
	if err != nil {
		return Profile{}, fmt.Errorf("load memberships: %w", err)
	}
	p.Teams, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TeamRole, error) {
		var tr TeamRole
		var role string
		err := r.Scan(&tr.Slug, &role)
		tr.Role = rbac.Role(role)
		return tr, err
	})
	if err != nil {
		return Profile{}, fmt.Errorf("read memberships: %w", err)
	}
	return p, nil
}

// HighestRole returns the most privileged role the user holds in any team (admin for a
// platform admin), or rbac.RoleNone. It backs routes that need "some team role".
func (s *UserStore) HighestRole(ctx context.Context, userID uuid.UUID) (rbac.Role, error) {
	var admin bool
	var roles []string
	err := s.pool.QueryRow(ctx, `
		SELECT u.is_platform_admin, coalesce(array_agg(m.role::text) FILTER (WHERE m.role IS NOT NULL), '{}')
		FROM users u LEFT JOIN memberships m ON m.user_id = u.id
		WHERE u.id = $1 AND u.disabled_at IS NULL GROUP BY u.id`, userID).Scan(&admin, &roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return rbac.RoleNone, nil
	}
	if err != nil {
		return rbac.RoleNone, fmt.Errorf("load highest role: %w", err)
	}
	return HighestOf(admin, roles), nil
}

// HighestOf folds membership roles (and platform admin) into one role. Pure.
func HighestOf(platformAdmin bool, roles []string) rbac.Role {
	if platformAdmin {
		return rbac.RoleAdmin
	}
	best := rbac.RoleNone
	for _, r := range roles {
		best = rbac.Max(best, rbac.Role(r))
	}
	return best
}
