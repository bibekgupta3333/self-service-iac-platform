package requests

import (
	"context"
	"encoding/json"
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

// LatestSpec implements Store.
func (s *pgStore) LatestSpec(ctx context.Context, stackID uuid.UUID) (json.RawMessage, bool, error) {
	return latestSpec(ctx, s.tx, stackID)
}

// LatestSpec implements Repository.
func (r *PostgresRepository) LatestSpec(ctx context.Context, stackID uuid.UUID) (json.RawMessage, bool, error) {
	return latestSpec(ctx, r.pool, stackID)
}

// latestSpec reads the spec of the stack's newest create, update or upgrade that did not
// fail, get rejected or get cancelled: the resource list the next request starts from.
func latestSpec(ctx context.Context, q db.DBTX, stackID uuid.UUID) (json.RawMessage, bool, error) {
	var spec []byte
	err := q.QueryRow(ctx, `SELECT spec FROM requests
		WHERE stack_id = $1 AND kind IN ('create', 'update', 'upgrade') AND status NOT IN ('failed', 'rejected', 'cancelled')
		ORDER BY created_at DESC, id DESC LIMIT 1`, stackID).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("query latest spec: %w", err)
	}
	return spec, true, nil
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

// UpdateStackApplied implements Store. The stacks row is locked by the caller, so the
// status guard only catches a caller that skipped the lock.
func (s *pgStore) UpdateStackApplied(ctx context.Context, w StackApplied) error {
	var outputs, lastReq any
	if w.Outputs != nil {
		outputs = []byte(w.Outputs)
	}
	if w.Applied {
		lastReq = w.RequestID
	}
	tag, err := s.tx.Exec(ctx, `UPDATE stacks SET
			status = $3::stack_status,
			health = coalesce(nullif($4, ''), health),
			outputs = coalesce($5::jsonb, outputs),
			last_applied_sha = coalesce(nullif($6, ''), last_applied_sha),
			last_applied_request_id = coalesce($7::uuid, last_applied_request_id),
			destroyed_at = CASE WHEN $3::text = 'destroyed' THEN $8 ELSE destroyed_at END,
			updated_at = $8
		WHERE id = $1 AND status = $2::stack_status`,
		w.ID, string(w.From), string(w.To), string(w.Health), outputs, w.CommitSHA, lastReq, w.At)
	if err != nil {
		return fmt.Errorf("update stack: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return apperr.Conflict("concurrent_update", fmt.Sprintf("stack %s is no longer %s", w.ID, w.From))
	}
	return nil
}
