package requests

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

var _ Memberships = (*PostgresRepository)(nil)

// RoleInTeam implements Memberships. Platform admins count as team admins; disabled or
// unknown users have no role.
func (r *PostgresRepository) RoleInTeam(ctx context.Context, teamSlug string, userID uuid.UUID) (Role, error) {
	var role string
	err := r.pool.QueryRow(ctx, `
		SELECT coalesce(
		         (SELECT CASE WHEN u.is_platform_admin THEN 'admin' ELSE coalesce(m.role::text, '') END
		          FROM users u
		          LEFT JOIN memberships m ON m.user_id = u.id AND m.team_id = t.id
		          WHERE u.id = $2 AND u.disabled_at IS NULL), '')
		FROM teams t WHERE t.slug = $1`, teamSlug, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleNone, apperr.NotFound("team %q not found", teamSlug)
	}
	if err != nil {
		return RoleNone, fmt.Errorf("query membership: %w", err)
	}
	return Role(role), nil
}

// Get implements Repository.
func (r *PostgresRepository) Get(ctx context.Context, id uuid.UUID) (*Request, error) {
	return getRequest(ctx, r.pool, id)
}

const requestCols = `r.id, r.short_id, r.stack_id, s.path, s.status, r.kind, r.status, r.spec, coalesce(r.justification, ''),
	r.queued_behind, r.fencing_token, coalesce(r.approved_plan_hash, ''), r.approved_by, r.approved_at, r.retry_count,
	r.github_branch, r.github_pr_number, r.github_pr_url, coalesce(r.github_head_sha, ''), coalesce(r.rendered_tree_sha, ''), coalesce(r.merge_commit_sha, ''),
	coalesce(r.failure_reason, ''), r.failure_detail, r.created_by, r.created_at, r.updated_at, r.completed_at`

func scanRequest(row pgx.Row) (*Request, error) {
	var (
		q        Request
		spec     []byte
		branch   *string
		prNumber *int
		prURL    *string
		detail   []byte
		headSHA  string
	)
	err := row.Scan(&q.ID, &q.ShortID, &q.Stack.ID, &q.Stack.Path, &q.Stack.Status, &q.Kind, &q.Status, &spec,
		&q.Justification, &q.QueuedBehind, &q.FencingToken, &q.ApprovedPlanHash, &q.ApprovedBy, &q.ApprovedAt, &q.RetryCount,
		&branch, &prNumber, &prURL, &headSHA, &q.RenderedTreeSHA, &q.MergeCommitSHA, &q.FailureReason, &detail,
		&q.CreatedBy, &q.CreatedAt, &q.UpdatedAt, &q.CompletedAt)
	if err != nil {
		return nil, err
	}
	q.Spec = spec
	if detail != nil {
		q.FailureDetail = detail
	}
	if prNumber != nil {
		q.PR = &PRLink{Number: *prNumber, HeadSHA: headSHA}
		if branch != nil {
			q.PR.Branch = *branch
		}
		if prURL != nil {
			q.PR.URL = *prURL
		}
	}
	return &q, nil
}

func getRequest(ctx context.Context, q db.DBTX, id uuid.UUID) (*Request, error) {
	return queryRequest(ctx, q, `r.id = $1`, id, "")
}

// queryRequest loads one request matching where (with one argument); lockClause is
// appended verbatim (e.g. "FOR UPDATE OF r").
func queryRequest(ctx context.Context, q db.DBTX, where string, arg any, lockClause string) (*Request, error) {
	req, err := scanRequest(q.QueryRow(ctx, `SELECT `+requestCols+`
		FROM requests r JOIN stacks s ON s.id = r.stack_id WHERE `+where+` `+lockClause, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("request %v not found", arg)
	}
	if err != nil {
		return nil, fmt.Errorf("query request: %w", err)
	}
	return req, nil
}

// TeamCostCenter returns the cost center of the team owning a stack (rendered as a tag).
func (r *PostgresRepository) TeamCostCenter(ctx context.Context, stackID uuid.UUID) (string, error) {
	var cc string
	err := r.pool.QueryRow(ctx, `SELECT t.cost_center FROM stacks s JOIN teams t ON t.id = s.team_id WHERE s.id = $1`, stackID).Scan(&cc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.NotFound("stack %s not found", stackID)
	}
	if err != nil {
		return "", fmt.Errorf("query team cost center: %w", err)
	}
	return cc, nil
}

// List implements Repository.
func (r *PostgresRepository) List(ctx context.Context, f ListFilter) ([]*Request, error) {
	var afterTS, afterID any
	if f.After != nil {
		afterTS, afterID = f.After.CreatedAt, f.After.ID
	}
	teamIDs := f.TeamIDs
	if teamIDs == nil {
		teamIDs = []uuid.UUID{}
	}
	rows, err := r.pool.Query(ctx, `SELECT `+requestCols+`
		FROM requests r
		JOIN stacks s ON s.id = r.stack_id
		JOIN teams t ON t.id = s.team_id
		WHERE ($1 = '' OR t.slug = $1)
		  AND ($2 = '' OR r.status::text = $2)
		  AND ($3 = '' OR r.kind::text = $3)
		  AND ($4::uuid IS NULL OR r.created_by = $4)
		  AND ($5::timestamptz IS NULL OR (r.created_at, r.id) < ($5, $6::uuid))
		  AND ($8 OR s.team_id = ANY($9::uuid[]))
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $7`, f.Team, string(f.Status), string(f.Kind), f.CreatedBy, afterTS, afterID, f.Limit, f.AllTeams, teamIDs)
	if err != nil {
		return nil, fmt.Errorf("query requests: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Request, error) { return scanRequest(row) })
	if err != nil {
		return nil, fmt.Errorf("scan requests: %w", err)
	}
	return out, nil
}

// pgStore is a Store bound to one transaction.
type pgStore struct {
	tx    pgx.Tx
	audit *audit.Writer
}

func (s *pgStore) ClaimIdempotencyKey(ctx context.Context, c IdempotencyClaim) (bool, error) {
	if _, err := s.tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE user_id = $1 AND key = $2 AND created_at < $3`,
		c.UserID, c.Key, c.ExpiredBefore); err != nil {
		return false, fmt.Errorf("expire idempotency key: %w", err)
	}
	tag, err := s.tx.Exec(ctx, `INSERT INTO idempotency_keys (user_id, key, request_hash, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, key) DO NOTHING`, c.UserID, c.Key, c.RequestHash, c.At)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *pgStore) GetIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (IdempotencyRecord, error) {
	var (
		rec IdempotencyRecord
		id  string
	)
	err := s.tx.QueryRow(ctx, `SELECT request_hash, response_body->>'request_id' FROM idempotency_keys WHERE user_id = $1 AND key = $2`,
		userID, key).Scan(&rec.RequestHash, &id)
	if err != nil {
		return IdempotencyRecord{}, err
	}
	if rec.RequestID, err = uuid.Parse(id); err != nil {
		return IdempotencyRecord{}, fmt.Errorf("idempotency key without request id: %w", err)
	}
	return rec, nil
}

// PurgeExpiredIdempotencyKeys deletes keys created before cutoff and returns how many
// were removed. The worker (WBS 4.9) calls it periodically; claims also expire keys lazily.
func (r *PostgresRepository) PurgeExpiredIdempotencyKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CompleteIdempotencyKey stores a pointer to the created request rather than a frozen
// HTTP body, so a replay returns the request's current state.
func (s *pgStore) CompleteIdempotencyKey(ctx context.Context, userID uuid.UUID, key string, requestID uuid.UUID) error {
	_, err := s.tx.Exec(ctx, `UPDATE idempotency_keys SET response_code = 202, response_body = jsonb_build_object('request_id', $3::text)
		WHERE user_id = $1 AND key = $2`, userID, key, requestID)
	return err
}

func (s *pgStore) TeamBySlug(ctx context.Context, slug string) (Team, error) {
	var t Team
	err := s.tx.QueryRow(ctx, `SELECT id, slug FROM teams WHERE slug = $1`, slug).Scan(&t.ID, &t.Slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, apperr.NotFound("team %q not found", slug)
	}
	return t, err
}

func (s *pgStore) EnvironmentByName(ctx context.Context, name string) (Environment, error) {
	var e Environment
	err := s.tx.QueryRow(ctx, `SELECT id, name, default_region, allowed_regions, allow_ephemeral, max_ttl_hours
		FROM environments WHERE name::text = $1`, name).
		Scan(&e.ID, &e.Name, &e.DefaultRegion, &e.AllowedRegions, &e.AllowEphemeral, &e.MaxTTLHours)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, apperr.NotFound("environment %q not found", name)
	}
	return e, err
}

func (s *pgStore) StackExists(ctx context.Context, envID, teamID uuid.UUID, name string) (bool, error) {
	var exists bool
	err := s.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stacks WHERE environment_id = $1 AND team_id = $2 AND name = $3)`,
		envID, teamID, name).Scan(&exists)
	return exists, err
}

func (s *pgStore) InsertStack(ctx context.Context, st NewStack) error {
	_, err := s.tx.Exec(ctx, `INSERT INTO stacks (id, team_id, environment_id, name, path, state_key, region,
			is_ephemeral, expires_at, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
		st.ID, st.TeamID, st.EnvironmentID, st.Name, st.Path, st.StateKey, st.Region, st.IsEphemeral, st.ExpiresAt,
		st.CreatedBy, st.CreatedAt)
	if db.IsUniqueViolation(err, "") {
		return apperr.Conflict("stack_exists", "stack "+st.Path+" already exists")
	}
	return err
}

func (s *pgStore) InsertRequest(ctx context.Context, r *Request) error {
	_, err := s.tx.Exec(ctx, `INSERT INTO requests (id, short_id, stack_id, kind, status, spec, justification,
			created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8, $9, $10)`,
		r.ID, r.ShortID, r.Stack.ID, string(r.Kind), string(r.Status), []byte(r.Spec), r.Justification,
		r.CreatedBy, r.CreatedAt, r.UpdatedAt)
	return err
}

func (s *pgStore) GetRequest(ctx context.Context, id uuid.UUID) (*Request, error) {
	return getRequest(ctx, s.tx, id)
}

func (s *pgStore) AppendAudit(ctx context.Context, e audit.Event) error {
	_, err := s.audit.Append(ctx, s.tx, e)
	return err
}

func (s *pgStore) Locks() locks.Store { return locks.NewTxStore(s.tx, s.audit) }

func (s *pgStore) GetRequestByShortID(ctx context.Context, shortID string) (*Request, error) {
	return queryRequest(ctx, s.tx, `r.short_id = $1`, shortID, "")
}

func (s *pgStore) LockRequest(ctx context.Context, id uuid.UUID) (*Request, error) {
	return queryRequest(ctx, s.tx, `r.id = $1`, id, "FOR UPDATE OF r")
}

// UpdateRequest writes the mutable fields of r, guarded by the status it was read in
// (docs/03 §4.1 optimistic check).
func (s *pgStore) UpdateRequest(ctx context.Context, r *Request, from Status) error {
	var (
		branch, url *string
		number      *int
		detail      []byte
		head        string
	)
	if r.PR != nil {
		branch, number, url, head = &r.PR.Branch, &r.PR.Number, &r.PR.URL, r.PR.HeadSHA
	}
	if len(r.FailureDetail) > 0 {
		detail = r.FailureDetail
	}
	tag, err := s.tx.Exec(ctx, `UPDATE requests SET status = $3, retry_count = $4, approved_plan_hash = nullif($5, ''),
			approved_by = $6, approved_at = $7, updated_at = $8, completed_at = $9, github_branch = $10,
			github_pr_number = $11, github_pr_url = $12, rendered_tree_sha = nullif($13, ''),
			merge_commit_sha = nullif($14, ''), failure_reason = nullif($15, ''), failure_detail = $16,
			github_head_sha = nullif($17, '')
		WHERE id = $1 AND status = $2`,
		r.ID, string(from), string(r.Status), r.RetryCount, r.ApprovedPlanHash, r.ApprovedBy, r.ApprovedAt, r.UpdatedAt,
		r.CompletedAt, branch, number, url, r.RenderedTreeSHA, r.MergeCommitSHA, r.FailureReason, detail, head)
	if db.IsUniqueViolation(err, "requests_pr_idx") {
		return apperr.Conflict("pr_number_in_use", fmt.Sprintf("pull request #%d is already linked to another request", r.PR.Number))
	}
	if err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return apperr.Conflict("concurrent_update", fmt.Sprintf("request %s is no longer %s", r.ShortID, from))
	}
	return nil
}

func (s *pgStore) Enqueue(ctx context.Context, m outbox.Message) (bool, error) {
	return outbox.Enqueue(ctx, s.tx, m)
}
