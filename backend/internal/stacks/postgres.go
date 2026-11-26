package stacks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// PostgresRepository implements Repository with pgx. It only reads.
type PostgresRepository struct{ db db.DBTX }

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository over conn.
func NewPostgresRepository(conn db.DBTX) *PostgresRepository { return &PostgresRepository{db: conn} }

// stackSelect joins what Stack needs and restricts rows to the visible teams: $1 is
// identity.Visibility's "all teams" flag and $2 its team ids.
const stackSelect = `
	SELECT s.id, t.slug, e.name::text, s.name, s.path, s.region, s.status::text, s.is_ephemeral, s.ttl_hours, s.ttl_extended_hours, s.expires_at,
	       s.outputs, coalesce(s.last_applied_sha, ''), s.created_at, s.updated_at,
	       lr.id, lr.short_id, lr.kind::text, lr.status::text, lr.created_at,
	       s.health,
	       lk.request_id, lkr.short_id, lk.holder, lk.acquired_at, lk.expires_at
	FROM stacks s
	JOIN teams t ON t.id = s.team_id
	JOIN environments e ON e.id = s.environment_id
	LEFT JOIN locks lk ON lk.stack_id = s.id
	LEFT JOIN requests lkr ON lkr.id = lk.request_id
	LEFT JOIN LATERAL (
	    SELECT r.id, r.short_id, r.kind, r.status, r.created_at
	    FROM requests r WHERE r.stack_id = s.id
	    ORDER BY r.created_at DESC, r.id DESC LIMIT 1) lr ON true
	WHERE ($1 OR s.team_id = ANY($2::uuid[]))`

func scanStack(row pgx.Row) (Stack, error) {
	var (
		s                  Stack
		status             string
		outputs            []byte
		reqID              *uuid.UUID
		reqShort           *string
		reqKind, reqStatus *string
		reqAt              *time.Time
		health             string
		lockReq            *uuid.UUID
		lockShort, holder  *string
		lockAt, lockExpiry *time.Time
	)
	err := row.Scan(&s.ID, &s.Team, &s.Environment, &s.Name, &s.Path, &s.Region, &status, &s.IsEphemeral, &s.TTLHours, &s.TTLExtended, &s.ExpiresAt,
		&outputs, &s.LastAppliedSHA, &s.CreatedAt, &s.UpdatedAt, &reqID, &reqShort, &reqKind, &reqStatus, &reqAt,
		&health, &lockReq, &lockShort, &holder, &lockAt, &lockExpiry)
	if err != nil {
		return Stack{}, err
	}
	s.Status, s.Outputs, s.Health = requests.StackStatus(status), outputs, requests.StackHealth(health)
	if lockReq != nil {
		s.Lock = &LockInfo{RequestID: *lockReq, RequestShortID: deref(lockShort), Holder: deref(holder),
			AcquiredAt: *lockAt, ExpiresAt: *lockExpiry}
	}
	if reqID != nil {
		s.LatestRequest = &RequestRef{ID: *reqID, ShortID: *reqShort, Kind: requests.Kind(*reqKind),
			Status: requests.Status(*reqStatus), CreatedAt: *reqAt}
	}
	return s, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// List implements Repository.
func (r *PostgresRepository) List(ctx context.Context, f Filter) ([]Stack, error) {
	var afterTS, afterID any
	if f.After != nil {
		afterTS, afterID = f.After.CreatedAt, f.After.ID
	}
	all, ids := f.Visibility.SQLArgs()
	rows, err := r.db.Query(ctx, stackSelect+`
		  AND ($3 = '' OR t.slug = $3)
		  AND ($4 = '' OR e.name::text = $4)
		  AND ($5 = '' OR s.status::text = $5)
		  AND ($6::timestamptz IS NULL OR (s.created_at, s.id) < ($6, $7::uuid))
		ORDER BY s.created_at DESC, s.id DESC
		LIMIT $8`, all, ids, f.Team, f.Environment, string(f.Status), afterTS, afterID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("query stacks: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Stack, error) { return scanStack(row) })
	if err != nil {
		return nil, fmt.Errorf("scan stacks: %w", err)
	}
	return out, nil
}

// Get implements Repository.
func (r *PostgresRepository) Get(ctx context.Context, v identity.Visibility, id uuid.UUID) (Stack, error) {
	all, ids := v.SQLArgs()
	s, err := scanStack(r.db.QueryRow(ctx, stackSelect+` AND s.id = $3`, all, ids, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Stack{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found",
			Message: fmt.Sprintf("stack %s not found", id)}
	}
	if err != nil {
		return Stack{}, fmt.Errorf("query stack: %w", err)
	}
	return s, nil
}
