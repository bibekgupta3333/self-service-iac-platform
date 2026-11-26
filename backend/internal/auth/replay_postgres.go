package auth

import (
	"context"
	"fmt"
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

// Claim implements ReplayStore. Expired rows are purged first, so the table holds only
// ids still inside their validity window and an expired id never blocks a claim.
func (s *PostgresReplayStore) Claim(ctx context.Context, jti string, expiresAt time.Time) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM oidc_jti WHERE expires_at <= $1`, s.clock.Now()); err != nil {
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
