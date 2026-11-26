package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// PostgresStore is the PostgreSQL adapter for sessions.
type PostgresStore struct{ q db.DBTX }

// NewPostgresStore returns a PostgresStore reading and writing through q.
func NewPostgresStore(q db.DBTX) *PostgresStore { return &PostgresStore{q: q} }

var _ Store = (*PostgresStore)(nil)

// Insert implements Store.
func (s *PostgresStore) Insert(ctx context.Context, r Record) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO sessions (id_hash, user_id, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, r.IDHash, r.UserID, r.CreatedAt, r.LastSeenAt, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, idHash []byte) (Record, error) {
	var r Record
	err := s.q.QueryRow(ctx, `
		SELECT s.id_hash, s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.revoked_at,
		       u.github_login::text, u.is_platform_admin, u.disabled_at IS NOT NULL
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = $1`, idHash).
		Scan(&r.IDHash, &r.UserID, &r.CreatedAt, &r.LastSeenAt, &r.ExpiresAt, &r.RevokedAt,
			&r.Login, &r.PlatformAdmin, &r.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("load session: %w", err)
	}
	return r, nil
}

// Touch implements Store. It never moves last_seen_at backwards.
func (s *PostgresStore) Touch(ctx context.Context, idHash []byte, now time.Time) error {
	if _, err := s.q.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id_hash = $1 AND last_seen_at < $2`, idHash, now); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Revoke implements Store.
func (s *PostgresStore) Revoke(ctx context.Context, idHash []byte, now time.Time) error {
	if _, err := s.q.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id_hash = $1 AND revoked_at IS NULL`, idHash, now); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeUser implements Store.
func (s *PostgresStore) RevokeUser(ctx context.Context, userID uuid.UUID, now time.Time) error {
	if _, err := s.q.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, now); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

// PurgeExpired implements Store.
func (s *PostgresStore) PurgeExpired(ctx context.Context, now time.Time, idleTTL time.Duration) (int64, error) {
	tag, err := s.q.Exec(ctx, `
		DELETE FROM sessions
		WHERE expires_at <= $1 OR last_seen_at <= $1 - make_interval(secs => $2) OR revoked_at IS NOT NULL`,
		now, idleTTL.Seconds())
	if err != nil {
		return 0, fmt.Errorf("purge sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
