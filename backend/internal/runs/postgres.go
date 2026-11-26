package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
)

// PostgresRepository implements Repository with pgx.
type PostgresRepository struct{ tx *db.TxRunner }

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{tx: db.NewTxRunner(pool)}
}

// InTx implements Repository.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(Store) error) error {
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(&pgStore{tx: tx}) })
}

type pgStore struct{ tx pgx.Tx }

func (s *pgStore) StackIDByPath(ctx context.Context, path string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.tx.QueryRow(ctx, `SELECT id FROM stacks WHERE path = $1`, path).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, apperr.NotFound("stack %q not found", path)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("query stack: %w", err)
	}
	return id, nil
}

func (s *pgStore) RequestByShortID(ctx context.Context, shortID string) (RequestRef, error) {
	var r RequestRef
	err := s.tx.QueryRow(ctx, `SELECT id, stack_id, status::text FROM requests WHERE short_id = $1`, shortID).
		Scan(&r.ID, &r.StackID, &r.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestRef{}, apperr.NotFound("request %q not found", shortID)
	}
	if err != nil {
		return RequestRef{}, fmt.Errorf("query request: %w", err)
	}
	return r, nil
}

func (s *pgStore) StackLock(ctx context.Context, stackID uuid.UUID) (*locks.Lock, error) {
	var l locks.Lock
	err := s.tx.QueryRow(ctx, `SELECT stack_id, request_id, holder, fencing_token, acquired_at, expires_at
		FROM locks WHERE stack_id = $1 FOR SHARE`, stackID).
		Scan(&l.StackID, &l.RequestID, &l.Holder, &l.FencingToken, &l.AcquiredAt, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query lock: %w", err)
	}
	return &l, nil
}

func (s *pgStore) UpsertRun(ctx context.Context, r Run) (uuid.UUID, Status, error) {
	var finished any
	if r.Status.Finished() {
		finished = r.At
	}
	var (
		id     uuid.UUID
		status Status
	)
	err := s.tx.QueryRow(ctx, `INSERT INTO pipeline_runs (request_id, stack_id, kind, status, github_run_id, github_run_attempt,
			github_job_id, workflow_ref, head_sha, stages, log_url, started_at, finished_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7::bigint, 0), $8, $9, jsonb_build_array($10::jsonb), nullif($11, ''), $12, $13, $12)
		ON CONFLICT (github_run_id, github_run_attempt, stack_id) DO UPDATE
		   SET status        = CASE WHEN pipeline_runs.finished_at IS NULL THEN EXCLUDED.status ELSE pipeline_runs.status END,
		       finished_at   = coalesce(pipeline_runs.finished_at, EXCLUDED.finished_at),
		       stages        = pipeline_runs.stages || EXCLUDED.stages,
		       request_id    = coalesce(pipeline_runs.request_id, EXCLUDED.request_id),
		       github_job_id = coalesce(EXCLUDED.github_job_id, pipeline_runs.github_job_id),
		       log_url       = coalesce(EXCLUDED.log_url, pipeline_runs.log_url)
		 WHERE pipeline_runs.kind = EXCLUDED.kind
		RETURNING id, status::text`,
		r.RequestID, r.StackID, string(r.Kind), string(r.Status), r.GitHub.RunID, r.GitHub.RunAttempt, r.GitHub.JobID,
		r.WorkflowRef, r.GitHub.HeadSHA, []byte(r.Stage), r.LogURL, r.At, finished).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", apperr.Conflict("run_kind_mismatch", fmt.Sprintf("run %d/%d was already reported as another pipeline",
			r.GitHub.RunID, r.GitHub.RunAttempt))
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("upsert run: %w", err)
	}
	return id, status, nil
}
