package drift

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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// PostgresRepository implements Repository with pgx.
type PostgresRepository struct {
	pool   *pgxpool.Pool
	tx     *db.TxRunner
	writer *audit.Writer
}

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository; audit events are appended in the
// same transaction as the state change they describe.
func NewPostgresRepository(pool *pgxpool.Pool, w *audit.Writer) *PostgresRepository {
	return &PostgresRepository{pool: pool, tx: db.NewTxRunner(pool), writer: w}
}

// InTx implements Repository.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(Store) error) error {
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(&pgStore{tx: tx, writer: r.writer}) })
}

// entrySelect joins the stack, team and environment a report belongs to.
const entrySelect = `
	SELECT d.id, d.stack_id, s.path, s.team_id, t.slug, e.name::text, d.pipeline_run_id, d.result::text,
	       d.terraform_exit_code, d.resources_drifted, d.resources_updated, d.resources_deleted, d.changed,
	       coalesce(d.plan_hash, ''), coalesce(d.log_url, ''), coalesce(d.error_message, ''), d.status::text,
	       d.detected_at, d.resolved_by, d.resolved_at, coalesce(d.resolution_reason, ''), d.resolution_request_id
	FROM drift_reports d
	JOIN stacks s ON s.id = d.stack_id
	JOIN teams t ON t.id = s.team_id
	JOIN environments e ON e.id = s.environment_id`

func scanEntry(row pgx.Row) (Entry, error) {
	var (
		e            Entry
		result       string
		status       *string
		exit         int16
		changedBytes []byte
	)
	err := row.Scan(&e.ID, &e.StackID, &e.StackPath, &e.TeamID, &e.Team, &e.Environment, &e.PipelineRunID, &result,
		&exit, &e.Summary.Drifted, &e.Summary.Updated, &e.Summary.Deleted, &changedBytes,
		&e.PlanHash, &e.LogURL, &e.Error, &status, &e.DetectedAt, &e.ResolvedBy, &e.ResolvedAt,
		&e.ResolutionNote, &e.ResolutionReqID)
	if err != nil {
		return Entry{}, err
	}
	e.Result, e.TerraformExit, e.Changed = Result(result), int(exit), changedBytes
	if status != nil {
		s := Status(*status)
		e.Status = &s
	}
	return e, nil
}

func reportNotFound(id uuid.UUID) error {
	return &apperr.Error{Kind: apperr.KindNotFound, Code: "drift_report_not_found", Message: fmt.Sprintf("drift report %s not found", id)}
}

// DueStacks implements Repository.
func (r *PostgresRepository) DueStacks(ctx context.Context, limit int) ([]DueStack, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.path, t.slug, e.name::text, s.region
		FROM stacks s
		JOIN teams t ON t.id = s.team_id
		JOIN environments e ON e.id = s.environment_id
		WHERE s.status IN ('active', 'drifted')
		  AND NOT EXISTS (SELECT 1 FROM locks l WHERE l.stack_id = s.id)
		ORDER BY s.last_drift_check_at NULLS FIRST, s.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query due stacks: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DueStack, error) {
		var d DueStack
		err := row.Scan(&d.ID, &d.Path, &d.Team, &d.Environment, &d.Region)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan due stacks: %w", err)
	}
	return out, nil
}

// ListByStack implements Repository.
func (r *PostgresRepository) ListByStack(ctx context.Context, v identity.Visibility, stackID uuid.UUID, limit int) ([]Entry, error) {
	all, ids := v.SQLArgs()
	var visible bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stacks WHERE id = $1 AND ($2 OR team_id = ANY($3::uuid[])))`,
		stackID, all, ids).Scan(&visible); err != nil {
		return nil, fmt.Errorf("check stack visibility: %w", err)
	}
	if !visible {
		return nil, &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found", Message: fmt.Sprintf("stack %s not found", stackID)}
	}
	rows, err := r.pool.Query(ctx, entrySelect+` WHERE d.stack_id = $1 ORDER BY d.detected_at DESC, d.id DESC LIMIT $2`, stackID, limit)
	if err != nil {
		return nil, fmt.Errorf("query drift reports: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Entry, error) { return scanEntry(row) })
	if err != nil {
		return nil, fmt.Errorf("scan drift reports: %w", err)
	}
	return out, nil
}

// Get implements Repository.
func (r *PostgresRepository) Get(ctx context.Context, v identity.Visibility, id uuid.UUID) (Entry, error) {
	all, ids := v.SQLArgs()
	e, err := scanEntry(r.pool.QueryRow(ctx, entrySelect+` WHERE d.id = $1 AND ($2 OR s.team_id = ANY($3::uuid[]))`, id, all, ids))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, reportNotFound(id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("query drift report: %w", err)
	}
	return e, nil
}

type pgStore struct {
	tx     pgx.Tx
	writer *audit.Writer
}

var _ Store = (*pgStore)(nil)

func (s *pgStore) LockStack(ctx context.Context, path string) (StackRow, error) {
	var (
		row    StackRow
		health string
	)
	err := s.tx.QueryRow(ctx, `SELECT id, team_id, status::text, health,
			EXISTS (SELECT 1 FROM locks l WHERE l.stack_id = stacks.id)
		FROM stacks WHERE path = $1 FOR NO KEY UPDATE`, path).
		Scan(&row.ID, &row.TeamID, &row.Status, &health, &row.Locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return StackRow{}, apperr.NotFound("stack %q not found", path)
	}
	if err != nil {
		return StackRow{}, fmt.Errorf("query stack: %w", err)
	}
	row.Health = requests.StackHealth(health)
	return row, nil
}

func (s *pgStore) UpsertRun(ctx context.Context, r RunRow) (uuid.UUID, error) {
	status, stage := "succeeded", "passed"
	if r.Failed {
		status, stage = "failed", "failed"
	}
	var id uuid.UUID
	// The no-op update makes RETURNING yield the existing row; the WHERE refuses a run id
	// that was already reported as another pipeline.
	err := s.tx.QueryRow(ctx, `INSERT INTO pipeline_runs (stack_id, kind, status, github_run_id, github_run_attempt,
			github_job_id, workflow_ref, head_sha, stages, log_url, started_at, finished_at, created_at)
		VALUES ($1, 'drift', $2::pipeline_status, $3, $4, nullif($5::bigint, 0), $6, $7,
		        jsonb_build_array(jsonb_build_object('name', 'summary', 'status', $8::text, 'reported_at', $9::timestamptz)),
		        nullif($10, ''), $9, $9, $9)
		ON CONFLICT (github_run_id, github_run_attempt, stack_id)
		DO UPDATE SET status = pipeline_runs.status WHERE pipeline_runs.kind = 'drift'
		RETURNING id`,
		r.StackID, status, r.GitHub.RunID, r.GitHub.RunAttempt, r.GitHub.JobID, r.WorkflowRef, r.GitHub.HeadSHA,
		stage, r.At, r.LogURL).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, apperr.Conflict("run_kind_mismatch",
			fmt.Sprintf("run %d/%d was already reported as another pipeline", r.GitHub.RunID, r.GitHub.RunAttempt))
	}
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (s *pgStore) InsertReport(ctx context.Context, n NewReport) (bool, error) {
	r := n.Report
	tag, err := s.tx.Exec(ctx, `INSERT INTO drift_reports (id, stack_id, pipeline_run_id, result, terraform_exit_code,
			resources_drifted, resources_updated, resources_deleted, changed, plan_hash, log_url, error_message, status, detected_at)
		VALUES ($1, $2, $3, $4::drift_result, $5, $6, $7, $8, $9::jsonb, nullif($10, ''), nullif($11, ''), nullif($12, ''),
		        CASE WHEN $4 = 'drifted' THEN 'open'::drift_status END, $13)
		ON CONFLICT (pipeline_run_id) DO NOTHING`,
		n.ID, n.StackID, n.PipelineRunID, string(r.Result), r.TerraformExitCode, r.Summary.Drifted, r.Summary.Updated,
		r.Summary.Deleted, []byte(n.Changed), r.PlanHash, r.LogURL, r.Error, n.At)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *pgStore) ReportByRun(ctx context.Context, runID uuid.UUID) (Entry, error) {
	return scanEntry(s.tx.QueryRow(ctx, entrySelect+` WHERE d.pipeline_run_id = $1`, runID))
}

// LockReport locks the report row only. Resolve may open a reconcile request while holding
// it, and that request locks the stack row; locking the stack here would make the two
// wait on each other. A scan holds the stack row and waits for report rows only through
// CloseOpen, which skips locked ones.
func (s *pgStore) LockReport(ctx context.Context, id uuid.UUID) (Entry, error) {
	e, err := scanEntry(s.tx.QueryRow(ctx, entrySelect+` WHERE d.id = $1 FOR UPDATE OF d`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, reportNotFound(id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("lock drift report: %w", err)
	}
	return e, nil
}

func (s *pgStore) NewerRunExists(ctx context.Context, stackID uuid.UUID, runID int64) (bool, error) {
	var newer bool
	err := s.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM drift_reports d JOIN pipeline_runs p ON p.id = d.pipeline_run_id
		WHERE d.stack_id = $1 AND p.github_run_id > $2)`, stackID, runID).Scan(&newer)
	return newer, err
}

func (s *pgStore) NewestDrifted(ctx context.Context, stackID uuid.UUID) (Entry, bool, error) {
	e, err := scanEntry(s.tx.QueryRow(ctx, entrySelect+` WHERE d.stack_id = $1 AND d.result = 'drifted'
		ORDER BY d.detected_at DESC, d.id DESC LIMIT 1`, stackID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

func (s *pgStore) SetStackCheck(ctx context.Context, stackID uuid.UUID, health requests.StackHealth, checkedAt *time.Time) error {
	_, err := s.tx.Exec(ctx, `UPDATE stacks SET health = $2, last_drift_check_at = coalesce($3, last_drift_check_at)
		WHERE id = $1`, stackID, string(health), checkedAt)
	return err
}

func (s *pgStore) CloseOpen(ctx context.Context, stackID uuid.UUID, at time.Time) (int, error) {
	tag, err := s.tx.Exec(ctx, `UPDATE drift_reports SET status = 'reconciled', resolved_at = $2
		WHERE id IN (SELECT id FROM drift_reports WHERE stack_id = $1 AND status = 'open' FOR UPDATE SKIP LOCKED)`, stackID, at)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *pgStore) Resolve(ctx context.Context, r Resolution) (int, error) {
	tag, err := s.tx.Exec(ctx, `UPDATE drift_reports
		   SET status = $3::drift_status, resolved_by = $4, resolved_at = $5, resolution_reason = nullif($6, ''),
		       resolution_request_id = CASE WHEN id = $1 THEN $7::uuid END
		 WHERE stack_id = $2 AND status = 'open'`,
		r.ReportID, r.StackID, string(r.Status), r.By, r.At, r.Reason, r.RequestID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *pgStore) AppendAudit(ctx context.Context, e audit.Event) error {
	_, err := s.writer.Append(ctx, s.tx, e)
	return err
}
