package requests

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// TeamVisibility resolves the teams a caller may read (identity.UserStore).
type TeamVisibility interface {
	VisibleTeams(ctx context.Context, p identity.Principal) (identity.Visibility, error)
}

// Access answers read-visibility questions for the request routes: which teams the caller
// sees, and whether one request belongs to one of them.
type Access struct {
	pool  *pgxpool.Pool
	teams TeamVisibility
}

// NewAccess returns an Access.
func NewAccess(pool *pgxpool.Pool, teams TeamVisibility) *Access {
	return &Access{pool: pool, teams: teams}
}

// Visible returns the caller's visible teams.
func (a *Access) Visible(ctx context.Context, p identity.Principal) (identity.Visibility, error) {
	return a.teams.VisibleTeams(ctx, p)
}

// CanView reports whether request id belongs to a team in v. An unknown id is simply not
// viewable, so callers answer 404 in both cases and existence is not revealed.
func (a *Access) CanView(ctx context.Context, v identity.Visibility, id uuid.UUID) (bool, error) {
	all, ids := v.SQLArgs()
	var ok bool
	err := a.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM requests r JOIN stacks s ON s.id = r.stack_id
		               WHERE r.id = $1 AND ($2 OR s.team_id = ANY($3::uuid[])))`, id, all, ids).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check request visibility: %w", err)
	}
	return ok, nil
}
