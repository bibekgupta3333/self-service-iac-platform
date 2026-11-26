package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// PostgresRepository implements Repository with pgx.
type PostgresRepository struct {
	tx     *db.TxRunner
	driver *requests.ApplyDriver
}

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository. driver applies apply-pipeline
// reports to requests and stacks inside the report's transaction.
func NewPostgresRepository(pool *pgxpool.Pool, driver *requests.ApplyDriver) *PostgresRepository {
	return &PostgresRepository{tx: db.NewTxRunner(pool), driver: driver}
}

// InTx implements Repository.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(Store) error) error {
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(&pgStore{tx: tx, applier: r.driver.ForTx(tx)}) })
}

type pgStore struct {
	tx      pgx.Tx
	applier requests.Applier
}

// Applier implements Store.
func (s *pgStore) Applier() requests.Applier { return s.applier }

// StackIDByPath takes FOR UPDATE on the stacks row before anything touches locks or
// requests, so run reports lock stacks, then locks, then the request: the same order as
// every lock operation and lifecycle transition. The opposite order deadlocks against
// release and promotion, and a weaker lock that is upgraded later deadlocks two reports
// of one stack against each other. It also applies reports of one stack one at a time.
func (s *pgStore) StackIDByPath(ctx context.Context, path string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.tx.QueryRow(ctx, `SELECT id FROM stacks WHERE path = $1 FOR UPDATE`, path).Scan(&id)
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
	err := s.tx.QueryRow(ctx, `SELECT id, stack_id, status::text, fencing_token FROM requests WHERE short_id = $1`, shortID).
		Scan(&r.ID, &r.StackID, &r.Status, &r.FencingToken)
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

// InsertRun inserts the run unless one with the same key exists. A concurrent insert of
// the same key waits for the first transaction and then reports a conflict.
func (s *pgStore) InsertRun(ctx context.Context, r Run) (uuid.UUID, bool, error) {
	var finished any
	if r.Status.Finished() {
		finished = r.At
	}
	var id uuid.UUID
	err := s.tx.QueryRow(ctx, `INSERT INTO pipeline_runs (request_id, stack_id, kind, status, github_run_id, github_run_attempt,
			github_job_id, workflow_ref, head_sha, stages, log_url, started_at, finished_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7::bigint, 0), $8, $9, jsonb_build_array($10::jsonb), nullif($11, ''), $12, $13, $12)
		ON CONFLICT (github_run_id, github_run_attempt, stack_id) DO NOTHING
		RETURNING id`,
		r.RequestID, r.StackID, string(r.Kind), string(r.Status), r.GitHub.RunID, r.GitHub.RunAttempt, r.GitHub.JobID,
		r.WorkflowRef, r.GitHub.HeadSHA, []byte(r.Stage), r.LogURL, r.At, finished).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

func (s *pgStore) LockRun(ctx context.Context, stackID uuid.UUID, gh GitHubRun) (StoredRun, error) {
	var (
		r      StoredRun
		stages []json.RawMessage
	)
	err := s.tx.QueryRow(ctx, `SELECT id, kind::text, status::text, stages FROM pipeline_runs
		WHERE github_run_id = $1 AND github_run_attempt = $2 AND stack_id = $3 FOR UPDATE`,
		gh.RunID, gh.RunAttempt, stackID).Scan(&r.ID, &r.Kind, &r.Status, &stages)
	if err != nil {
		return StoredRun{}, err
	}
	r.Stages = stages
	return r, nil
}

func (s *pgStore) UpdateRun(ctx context.Context, u RunUpdate) error {
	stages, err := json.Marshal(u.Stages)
	if err != nil {
		return fmt.Errorf("encode stages: %w", err)
	}
	_, err = s.tx.Exec(ctx, `UPDATE pipeline_runs
		   SET status        = $2,
		       stages        = $3::jsonb,
		       finished_at   = coalesce(finished_at, $4),
		       github_job_id = coalesce(github_job_id, nullif($5::bigint, 0)),
		       log_url       = coalesce(log_url, nullif($6, '')),
		       request_id    = coalesce(request_id, $7)
		 WHERE id = $1`, u.ID, string(u.Status), stages, u.FinishedAt, u.JobID, u.LogURL, u.RequestID)
	return err
}
