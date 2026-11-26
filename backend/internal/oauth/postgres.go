package oauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// PostgresStateStore is the PostgreSQL adapter for pending logins.
type PostgresStateStore struct{ q db.DBTX }

// NewPostgresStateStore returns a PostgresStateStore.
func NewPostgresStateStore(q db.DBTX) *PostgresStateStore { return &PostgresStateStore{q: q} }

var _ StateStore = (*PostgresStateStore)(nil)

// Save implements StateStore.
func (s *PostgresStateStore) Save(ctx context.Context, r StateRecord) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO oauth_login_states (state_hash, code_verifier, return_to, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, r.StateHash, r.Verifier, r.ReturnTo, r.CreatedAt, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert login state: %w", err)
	}
	return nil
}

// Consume implements StateStore. DELETE ... RETURNING makes concurrent callbacks with the
// same state race for the row: exactly one wins.
func (s *PostgresStateStore) Consume(ctx context.Context, stateHash []byte, now time.Time) (StateRecord, error) {
	r := StateRecord{StateHash: stateHash}
	err := s.q.QueryRow(ctx, `
		DELETE FROM oauth_login_states WHERE state_hash = $1 AND expires_at > $2
		RETURNING code_verifier, return_to, created_at, expires_at`, stateHash, now).
		Scan(&r.Verifier, &r.ReturnTo, &r.CreatedAt, &r.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return StateRecord{}, ErrInvalidState
	}
	if err != nil {
		return StateRecord{}, fmt.Errorf("consume login state: %w", err)
	}
	return r, nil
}

// Purge implements StateStore.
func (s *PostgresStateStore) Purge(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.q.Exec(ctx, `DELETE FROM oauth_login_states WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("purge login states: %w", err)
	}
	return tag.RowsAffected(), nil
}
