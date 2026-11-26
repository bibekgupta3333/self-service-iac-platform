package ttl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
)

// PostgresRepository implements Repository with pgx.
type PostgresRepository struct {
	pool  *pgxpool.Pool
	tx    *db.TxRunner
	audit *audit.Writer
}

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool, auditWriter *audit.Writer) *PostgresRepository {
	return &PostgresRepository{pool: pool, tx: db.NewTxRunner(pool), audit: auditWriter}
}

// InTx implements Repository.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(Store) error) error {
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(&pgStore{tx: tx, audit: r.audit}) })
}

const stackCols = `s.id, s.team_id, t.slug, e.name::text, s.name, s.path, s.status::text, s.is_ephemeral,
	s.expires_at, s.ttl_warned_at, s.created_at, s.ttl_extended_hours, e.max_ttl_hours`

const stackFrom = ` FROM stacks s JOIN teams t ON t.id = s.team_id JOIN environments e ON e.id = s.environment_id`

func scanStack(row pgx.Row) (Stack, error) {
	var s Stack
	err := row.Scan(&s.ID, &s.TeamID, &s.Team, &s.Environment, &s.Name, &s.Path, &s.Status, &s.IsEphemeral,
		&s.ExpiresAt, &s.WarnedAt, &s.CreatedAt, &s.TTLExtendedHours, &s.MaxTTLHours)
	return s, err
}

func loadStack(ctx context.Context, q db.DBTX, id uuid.UUID, lock string) (Stack, error) {
	s, err := scanStack(q.QueryRow(ctx, `SELECT `+stackCols+stackFrom+` WHERE s.id = $1 `+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Stack{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found", Message: fmt.Sprintf("stack %s not found", id)}
	}
	if err != nil {
		return Stack{}, fmt.Errorf("query stack: %w", err)
	}
	return s, nil
}

// Stack implements Repository.
func (r *PostgresRepository) Stack(ctx context.Context, id uuid.UUID) (Stack, error) {
	return loadStack(ctx, r.pool, id, "")
}

// Candidates implements Repository. The stacks_expiry_idx partial index serves the filter.
func (r *PostgresRepository) Candidates(ctx context.Context, horizon time.Time, limit int) ([]Stack, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+stackCols+stackFrom+`
		WHERE s.is_ephemeral AND s.status NOT IN ('destroying', 'destroyed')
		  AND e.name::text <> $1 AND s.expires_at <= $2
		  AND NOT EXISTS (SELECT 1 FROM requests q
		                  WHERE q.stack_id = s.id AND q.kind = 'destroy' AND q.completed_at IS NULL)
		ORDER BY s.expires_at, s.id
		LIMIT $3`, ProductionEnvironment, horizon, limit)
	if err != nil {
		return nil, fmt.Errorf("query ttl candidates: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Stack, error) { return scanStack(row) })
	if err != nil {
		return nil, fmt.Errorf("scan ttl candidates: %w", err)
	}
	return out, nil
}

// SystemUserID implements Repository.
func (r *PostgresRepository) SystemUserID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `SELECT id FROM users WHERE is_system AND github_login = 'iacp-bot'`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNoSystemUser
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("query system user: %w", err)
	}
	return id, nil
}

// LatestPullRequest implements Repository.
func (r *PostgresRepository) LatestPullRequest(ctx context.Context, id uuid.UUID) (int, bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT github_pr_number FROM requests
		WHERE stack_id = $1 AND github_pr_number IS NOT NULL
		ORDER BY created_at DESC, id DESC LIMIT 1`, id).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query latest pull request: %w", err)
	}
	return n, true, nil
}

type pgStore struct {
	tx    pgx.Tx
	audit *audit.Writer
}

var _ Store = (*pgStore)(nil)

func (s *pgStore) LockStack(ctx context.Context, id uuid.UUID) (Stack, error) {
	return loadStack(ctx, s.tx, id, "FOR UPDATE OF s")
}

func (s *pgStore) SetExpiry(ctx context.Context, id uuid.UUID, e Extension) error {
	tag, err := s.tx.Exec(ctx, `UPDATE stacks SET expires_at = $2, ttl_extended_hours = $3, ttl_warned_at = NULL,
		updated_at = now() WHERE id = $1`, id, e.ExpiresAt, e.ExtendedHours)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("stack %s vanished", id)
	}
	return nil
}

func (s *pgStore) MarkWarned(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := s.tx.Exec(ctx, `UPDATE stacks SET ttl_warned_at = $2 WHERE id = $1`, id, at)
	return err
}

func (s *pgStore) HasOpenDestroy(ctx context.Context, id uuid.UUID) (bool, error) {
	var open bool
	err := s.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM requests WHERE stack_id = $1 AND kind = 'destroy'
		AND completed_at IS NULL)`, id).Scan(&open)
	return open, err
}

func (s *pgStore) AppendAudit(ctx context.Context, e audit.Event) error {
	_, err := s.audit.Append(ctx, s.tx, e)
	return err
}

func (s *pgStore) Enqueue(ctx context.Context, m outbox.Message) (bool, error) {
	return outbox.Enqueue(ctx, s.tx, m)
}
