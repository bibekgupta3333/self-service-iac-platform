package locks

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
//
// Race safety: every mutation first takes the stack row lock (SELECT … FOR UPDATE on
// stacks), so lock operations on one stack run one at a time, and the grant itself is a
// single upsert on the locks primary key.
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
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(NewTxStore(tx, r.audit)) })
}

// Get implements Repository.
func (r *PostgresRepository) Get(ctx context.Context, stackID uuid.UUID) (*Lock, error) {
	return current(ctx, r.pool, stackID)
}

// NewTxStore returns a Store bound to tx, so other repositories can take the lock inside
// their own transaction.
func NewTxStore(tx pgx.Tx, auditWriter *audit.Writer) Store {
	return &pgStore{tx: tx, audit: auditWriter}
}

type pgStore struct {
	tx    pgx.Tx
	audit *audit.Writer
}

func (s *pgStore) Enqueue(ctx context.Context, m outbox.Message) (bool, error) {
	return outbox.Enqueue(ctx, s.tx, m)
}

const lockCols = `stack_id, request_id, holder, fencing_token, acquired_at, expires_at`

func scanLock(row pgx.Row) (Lock, error) {
	var l Lock
	err := row.Scan(&l.StackID, &l.RequestID, &l.Holder, &l.FencingToken, &l.AcquiredAt, &l.ExpiresAt)
	return l, err
}

func current(ctx context.Context, q db.DBTX, stackID uuid.UUID) (*Lock, error) {
	l, err := scanLock(q.QueryRow(ctx, `SELECT `+lockCols+` FROM locks WHERE stack_id = $1`, stackID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query lock: %w", err)
	}
	return &l, nil
}

func (s *pgStore) LockStack(ctx context.Context, stackID uuid.UUID) (uuid.UUID, error) {
	var teamID uuid.UUID
	err := s.tx.QueryRow(ctx, `SELECT team_id FROM stacks WHERE id = $1 FOR UPDATE`, stackID).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, apperr.NotFound("stack %s not found", stackID)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("lock stack row: %w", err)
	}
	return teamID, nil
}

func (s *pgStore) Request(ctx context.Context, id uuid.UUID) (RequestInfo, error) {
	ri := RequestInfo{ID: id}
	err := s.tx.QueryRow(ctx, `SELECT short_id, stack_id, completed_at IS NOT NULL, queued_behind, fencing_token FROM requests WHERE id = $1`, id).
		Scan(&ri.ShortID, &ri.StackID, &ri.Completed, &ri.QueuedBehind, &ri.FencingToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestInfo{}, apperr.NotFound("request %s not found", id)
	}
	if err != nil {
		return RequestInfo{}, fmt.Errorf("query request: %w", err)
	}
	return ri, nil
}

func (s *pgStore) Current(ctx context.Context, stackID uuid.UUID) (*Lock, error) {
	return current(ctx, s.tx, stackID)
}

func (s *pgStore) Waiters(ctx context.Context, stackID uuid.UUID) ([]Waiter, error) {
	rows, err := s.tx.Query(ctx, `SELECT id, created_at FROM requests
		WHERE stack_id = $1 AND queued_behind IS NOT NULL AND completed_at IS NULL`, stackID)
	if err != nil {
		return nil, fmt.Errorf("query waiters: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Waiter, error) {
		var w Waiter
		err := row.Scan(&w.RequestID, &w.CreatedAt)
		return w, err
	})
}

// Grant is the docs/03 AcquireStackLock upsert. The stack row lock held by the caller
// makes the conflict branch safe to run unconditionally: the service has already decided
// that the existing row, if any, is expired.
func (s *pgStore) Grant(ctx context.Context, g Grant) (Lock, error) {
	l, err := scanLock(s.tx.QueryRow(ctx, `INSERT INTO locks (stack_id, request_id, holder, acquired_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (stack_id) DO UPDATE
		   SET request_id    = EXCLUDED.request_id,
		       holder        = EXCLUDED.holder,
		       fencing_token = nextval('lock_fencing_seq'),
		       acquired_at   = EXCLUDED.acquired_at,
		       expires_at    = EXCLUDED.expires_at
		RETURNING `+lockCols, g.StackID, g.RequestID, g.Holder, g.AcquiredAt, g.ExpiresAt))
	if err != nil {
		return Lock{}, fmt.Errorf("upsert lock: %w", err)
	}
	if _, err := s.tx.Exec(ctx, `UPDATE requests SET fencing_token = $2, queued_behind = NULL, updated_at = $3 WHERE id = $1`,
		g.RequestID, l.FencingToken, g.AcquiredAt); err != nil {
		return Lock{}, fmt.Errorf("record fencing token: %w", err)
	}
	return l, nil
}

func (s *pgStore) Extend(ctx context.Context, stackID uuid.UUID, expiresAt time.Time) (Lock, error) {
	l, err := scanLock(s.tx.QueryRow(ctx, `UPDATE locks SET expires_at = $2 WHERE stack_id = $1 RETURNING `+lockCols,
		stackID, expiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return Lock{}, apperr.Conflict("stale_fencing_token", "the stack is not locked")
	}
	return l, err
}

func (s *pgStore) Delete(ctx context.Context, stackID uuid.UUID) error {
	_, err := s.tx.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, stackID)
	return err
}

func (s *pgStore) Queue(ctx context.Context, request, behind uuid.UUID) error {
	_, err := s.tx.Exec(ctx, `UPDATE requests SET queued_behind = $2 WHERE id = $1`, request, behind)
	return err
}

func (s *pgStore) Requeue(ctx context.Context, stackID, behind uuid.UUID) error {
	_, err := s.tx.Exec(ctx, `UPDATE requests SET queued_behind = $2
		WHERE stack_id = $1 AND id <> $2 AND queued_behind IS NOT NULL AND completed_at IS NULL`, stackID, behind)
	return err
}

func (s *pgStore) AppendAudit(ctx context.Context, e audit.Event) error {
	_, err := s.audit.Append(ctx, s.tx, e)
	return err
}
