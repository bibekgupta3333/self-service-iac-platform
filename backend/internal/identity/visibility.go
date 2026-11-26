package identity

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Visibility is the set of teams whose data a caller may read: every team for a platform
// admin, otherwise the teams they hold a role in. It is the shared helper for "teams visible
// to the caller": handlers fetch it once and services and SQL filter with it, so a read
// route can never forget the question.
//
// The zero value sees nothing (fail closed).
type Visibility struct {
	All     bool
	TeamIDs []uuid.UUID
}

// Allows reports whether team is visible. Pure.
func (v Visibility) Allows(team uuid.UUID) bool { return v.All || slices.Contains(v.TeamIDs, team) }

// SQLArgs returns the two bound parameters for the predicate
//
//	($all OR <team_id column> = ANY($ids::uuid[]))
//
// used by team-scoped queries. The slice is never nil, so ANY gets an empty array. Pure.
func (v Visibility) SQLArgs() (all bool, ids []uuid.UUID) {
	ids = v.TeamIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return v.All, ids
}

// VisibleTeams resolves what p may see. A platform admin (including the dev-mode user,
// whose flag lives on the principal) sees everything without a lookup; anyone else sees the
// teams they are a member of, and a disabled user sees nothing.
func (s *UserStore) VisibleTeams(ctx context.Context, p Principal) (Visibility, error) {
	if p.PlatformAdmin {
		return Visibility{All: true}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.team_id FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.user_id = $1 AND u.disabled_at IS NULL`, p.UserID)
	if err != nil {
		return Visibility{}, fmt.Errorf("query visible teams: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return Visibility{}, fmt.Errorf("scan visible team: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return Visibility{}, fmt.Errorf("read visible teams: %w", err)
	}
	return Visibility{TeamIDs: ids}, nil
}
