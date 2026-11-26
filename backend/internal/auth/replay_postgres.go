package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// PostgresReplayStore is the ReplayStore shared by every API replica (table oidc_jti).
// The primary key makes the claim atomic: of two replicas presented the same token,
// exactly one inserts the row.
type PostgresReplayStore struct {
	db    db.DBTX
	clock platform.Clock
}

// NewPostgresReplayStore returns a store over conn.
func NewPostgresReplayStore(conn db.DBTX, clock platform.Clock) *PostgresReplayStore {
	return &PostgresReplayStore{db: conn, clock: clock}
}

// Claim implements ReplayStore. Only an expired row for the same jti is removed inline,
// so an old id never blocks a claim and the hot path touches one key; other expired
// rows are left to PurgeExpired (run by RunReplaySweeper).
func (s *PostgresReplayStore) Claim(ctx context.Context, jti string, expiresAt time.Time) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM oidc_jti WHERE jti = $1 AND expires_at <= $2`, jti, s.clock.Now()); err != nil {
		return fmt.Errorf("purge expired jti: %w", err)
	}
	tag, err := s.db.Exec(ctx,
		`INSERT INTO oidc_jti (jti, expires_at) VALUES ($1, $2) ON CONFLICT (jti) DO NOTHING`, jti, expiresAt)
	if err != nil {
		return fmt.Errorf("claim jti: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenReplayed
	}
	return nil
}

// PurgeExpired deletes at most limit expired rows and returns how many it removed.
// The ctid subquery bounds each statement, so a large backlog is cleared in small
// batches that never hold many row locks.
func (s *PostgresReplayStore) PurgeExpired(ctx context.Context, limit int) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM oidc_jti WHERE ctid IN (
			SELECT ctid FROM oidc_jti WHERE expires_at <= $1 ORDER BY expires_at LIMIT $2)`,
		s.clock.Now(), limit)
	if err != nil {
		return 0, fmt.Errorf("purge expired jti: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReplaySweepBatch bounds one PurgeExpired statement.
const ReplaySweepBatch = 1000

// RunReplaySweeper calls PurgeExpired every interval until ctx ends, draining the
// backlog batch by batch. Errors are logged and retried on the next tick.
func RunReplaySweeper(ctx context.Context, s *PostgresReplayStore, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for {
			n, err := s.PurgeExpired(ctx, ReplaySweepBatch)
			if err != nil {
				if ctx.Err() == nil {
					logger.WarnContext(ctx, "oidc_jti sweep failed", "error", err)
				}
				break
			}
			if n < ReplaySweepBatch {
				break
			}
		}
	}
}
