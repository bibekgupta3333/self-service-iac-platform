package recovery

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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// PostgresRepository implements RetryRepository with pgx.
type PostgresRepository struct {
	tx    *db.TxRunner
	audit *audit.Writer
}

var _ RetryRepository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool, w *audit.Writer) *PostgresRepository {
	return &PostgresRepository{tx: db.NewTxRunner(pool), audit: w}
}

// InTx implements RetryRepository.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(RetryStore) error) error {
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(&pgStore{tx: tx, audit: r.audit}) })
}

type pgStore struct {
	tx    pgx.Tx
	audit *audit.Writer
}

// target locks the stack row first, then the request row: the order every stack-lock
// operation uses, so recovery cannot deadlock with lock acquisition, run reports or
// request transitions.
func (s *pgStore) target(ctx context.Context, where string, arg any) (Target, error) {
	var stackID uuid.UUID
	err := s.tx.QueryRow(ctx, `SELECT stack_id FROM requests WHERE `+where, arg).Scan(&stackID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, apperr.NotFound("request %v not found", arg)
	}
	if err != nil {
		return Target{}, fmt.Errorf("query request: %w", err)
	}
	if _, err := s.tx.Exec(ctx, `SELECT 1 FROM stacks WHERE id = $1 FOR UPDATE`, stackID); err != nil {
		return Target{}, fmt.Errorf("lock stack row: %w", err)
	}
	var (
		t            Target
		kind, status string
	)
	err = s.tx.QueryRow(ctx, `
		SELECT r.id, r.short_id, r.kind::text, r.status::text, r.retry_count, r.fencing_token,
		       coalesce(r.approved_plan_hash, ''), s.id, s.path, s.team_id
		FROM requests r JOIN stacks s ON s.id = r.stack_id
		WHERE r.`+where+` FOR UPDATE OF r`, arg).
		Scan(&t.ID, &t.ShortID, &kind, &status, &t.RetryCount, &t.FencingToken, &t.ApprovedPlanHash,
			&t.StackID, &t.StackPath, &t.TeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, apperr.NotFound("request %v not found", arg)
	}
	if err != nil {
		return Target{}, fmt.Errorf("lock request: %w", err)
	}
	t.Kind, t.Status = requests.Kind(kind), requests.Status(status)
	return t, nil
}

func (s *pgStore) TargetByShortID(ctx context.Context, shortID string) (Target, error) {
	return s.target(ctx, `short_id = $1`, shortID)
}

func (s *pgStore) TargetByID(ctx context.Context, id uuid.UUID) (Target, error) {
	return s.target(ctx, `id = $1`, id)
}

func (s *pgStore) CurrentLock(ctx context.Context, stackID uuid.UUID) (*locks.Lock, error) {
	return locks.NewTxStore(s.tx, s.audit).Current(ctx, stackID)
}

func (s *pgStore) InsertFailure(ctx context.Context, f FailureRecord) (bool, error) {
	var (
		retry *int
		state *string
	)
	if f.RetryNumber > 0 {
		n, st := f.RetryNumber, DispatchScheduled
		retry, state = &n, &st
	}
	tag, err := s.tx.Exec(ctx, `INSERT INTO apply_failures (id, request_id, stack_id, github_run_id, github_run_attempt,
			stage, terraform_exit_code, failure_class, reason, decision, retry_number, retry_at, dispatch_state,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
		ON CONFLICT (request_id, github_run_id, github_run_attempt) DO NOTHING`,
		f.ID, f.RequestID, f.StackID, f.Run.RunID, f.Run.Attempt, f.Stage, f.ExitCode, string(f.Class.Class),
		f.Class.Reason, f.Decision, retry, f.RetryAt, state, f.At)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *pgStore) IncrementRetry(ctx context.Context, requestID uuid.UUID, from int, at time.Time) error {
	tag, err := s.tx.Exec(ctx, `UPDATE requests SET retry_count = retry_count + 1, updated_at = $3
		WHERE id = $1 AND status = 'applying' AND retry_count = $2`, requestID, from, at)
	if err != nil {
		return fmt.Errorf("increment retry_count: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return apperr.Conflict("concurrent_update", fmt.Sprintf("request %s is no longer applying with retry_count %d", requestID, from))
	}
	return nil
}

func (s *pgStore) ClaimDispatch(ctx context.Context, requestID uuid.UUID, n int, at time.Time) (bool, error) {
	tag, err := s.tx.Exec(ctx, `UPDATE apply_failures SET dispatch_state = 'dispatching', updated_at = $3
		WHERE request_id = $1 AND retry_number = $2 AND dispatch_state = 'scheduled'`, requestID, n, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *pgStore) FinishDispatch(ctx context.Context, requestID uuid.UUID, n int, state, detail string, at time.Time) error {
	tag, err := s.tx.Exec(ctx, `UPDATE apply_failures SET dispatch_state = $3, dispatch_detail = nullif($4, ''),
			dispatched_at = CASE WHEN $3 = 'dispatched' THEN $5 ELSE dispatched_at END, updated_at = $5
		WHERE request_id = $1 AND retry_number = $2 AND dispatch_state = 'dispatching'`, requestID, n, state, detail, at)
	if err != nil {
		return fmt.Errorf("record dispatch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return apperr.Conflict("concurrent_update", fmt.Sprintf("retry %d of request %s is not being dispatched", n, requestID))
	}
	return nil
}

func (s *pgStore) Enqueue(ctx context.Context, m outbox.Message) (bool, error) {
	return outbox.Enqueue(ctx, s.tx, m)
}

func (s *pgStore) AppendAudit(ctx context.Context, e audit.Event) error {
	_, err := s.audit.Append(ctx, s.tx, e)
	return err
}
