package requests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// stackTarget loads a stack with its team and environment names; lockClause is
// appended verbatim (e.g. "FOR UPDATE OF s").
func stackTarget(ctx context.Context, q db.DBTX, id uuid.UUID, lockClause string) (StackTarget, error) {
	var t StackTarget
	var status string
	err := q.QueryRow(ctx, `
		SELECT s.id, s.team_id, t.slug, s.environment_id, e.name::text, s.name, s.path, s.region, s.status::text
		FROM stacks s
		JOIN teams t ON t.id = s.team_id
		JOIN environments e ON e.id = s.environment_id
		WHERE s.id = $1 `+lockClause, id).
		Scan(&t.ID, &t.TeamID, &t.TeamSlug, &t.EnvironmentID, &t.Environment, &t.Name, &t.Path, &t.Region, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return StackTarget{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found",
			Message: fmt.Sprintf("stack %s not found", id)}
	}
	if err != nil {
		return StackTarget{}, fmt.Errorf("query stack: %w", err)
	}
	t.Status = StackStatus(status)
	return t, nil
}

// StackTarget implements Repository.
func (r *PostgresRepository) StackTarget(ctx context.Context, id uuid.UUID) (StackTarget, error) {
	return stackTarget(ctx, r.pool, id, "")
}

// LockStackTarget implements Store: the stacks row is locked first, the order every
// stack-lock operation uses.
func (s *pgStore) LockStackTarget(ctx context.Context, id uuid.UUID) (StackTarget, error) {
	return stackTarget(ctx, s.tx, id, "FOR UPDATE OF s")
}

// OpenRequestKinds implements Store.
func (s *pgStore) OpenRequestKinds(ctx context.Context, stackID uuid.UUID) ([]Kind, error) {
	rows, err := s.tx.Query(ctx, `SELECT kind::text FROM requests WHERE stack_id = $1 AND completed_at IS NULL`, stackID)
	if err != nil {
		return nil, fmt.Errorf("query open requests: %w", err)
	}
	kinds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Kind, error) {
		var k string
		err := row.Scan(&k)
		return Kind(k), err
	})
	if err != nil {
		return nil, fmt.Errorf("scan open requests: %w", err)
	}
	return kinds, nil
}
