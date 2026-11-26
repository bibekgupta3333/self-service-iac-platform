package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// Enqueue inserts m using q, normally the producer's transaction. It returns false when a
// pending job with the same dedupe key already exists. AvailableAt defaults to the
// application clock (not the database's), because Claim compares against it too.
func Enqueue(ctx context.Context, q db.DBTX, m Message) (bool, error) {
	avail := m.AvailableAt
	if avail.IsZero() {
		avail = time.Now().UTC()
	}
	var dedupe any
	if m.DedupeKey != "" {
		dedupe = m.DedupeKey
	}
	tag, err := q.Exec(ctx, `INSERT INTO outbox (topic, aggregate_id, dedupe_key, payload, available_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (dedupe_key) WHERE dedupe_key IS NOT NULL AND processed_at IS NULL AND dead_at IS NULL DO NOTHING`,
		m.Topic, m.AggregateID, dedupe, []byte(m.Payload), avail)
	if err != nil {
		return false, fmt.Errorf("enqueue %s: %w", m.Topic, err)
	}
	return tag.RowsAffected() == 1, nil
}

// PostgresStore implements Store on the outbox table.
type PostgresStore struct{ pool *pgxpool.Pool }

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a PostgresStore.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Claim implements Store. SKIP LOCKED lets concurrent workers claim different jobs; an
// expired lease (a worker that died mid-job) makes the job claimable again.
func (s *PostgresStore) Claim(ctx context.Context, now time.Time, lease time.Duration) (*Job, error) {
	var j Job
	err := s.pool.QueryRow(ctx, `
		UPDATE outbox SET locked_until = $2, attempts = attempts + 1
		WHERE id = (
			SELECT id FROM outbox
			WHERE processed_at IS NULL AND dead_at IS NULL AND available_at <= $1
			  AND (locked_until IS NULL OR locked_until < $1)
			ORDER BY available_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1)
		RETURNING id, topic, aggregate_id, payload, attempts`, now, now.Add(lease)).
		Scan(&j.ID, &j.Topic, &j.AggregateID, &j.Payload, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// claimGuard limits an outcome write to the claim that ran the job: same attempt, still
// pending.
const claimGuard = ` WHERE id = $1 AND attempts = $2 AND processed_at IS NULL AND dead_at IS NULL`

func (s *PostgresStore) finish(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql+claimGuard, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrClaimLost
	}
	return nil
}

// Complete implements Store.
func (s *PostgresStore) Complete(ctx context.Context, id int64, attempt int, at time.Time) error {
	return s.finish(ctx, `UPDATE outbox SET processed_at = $3, locked_until = NULL, last_error = NULL`, id, attempt, at)
}

// Retry implements Store.
func (s *PostgresStore) Retry(ctx context.Context, id int64, attempt int, availableAt time.Time, lastErr string) error {
	return s.finish(ctx, `UPDATE outbox SET available_at = $3, locked_until = NULL, last_error = $4`, id, attempt, availableAt, lastErr)
}

// Dead implements Store.
func (s *PostgresStore) Dead(ctx context.Context, id int64, attempt int, at time.Time, lastErr string) error {
	return s.finish(ctx, `UPDATE outbox SET dead_at = $3, locked_until = NULL, last_error = $4`, id, attempt, at, lastErr)
}

// PurgeProcessed removes up to limit jobs that were handled successfully before cutoff and
// returns how many it removed. It calls purge_processed_outbox (migration 0010), which runs
// with the owner's rights because iacp_app cannot delete from outbox, and which never
// removes pending or dead jobs.
func (s *PostgresStore) PurgeProcessed(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT purge_processed_outbox($1, $2)`, cutoff, limit).Scan(&n); err != nil {
		return 0, fmt.Errorf("purge processed outbox rows: %w", err)
	}
	return n, nil
}
