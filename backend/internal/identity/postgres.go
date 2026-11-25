package identity

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserStore is the PostgreSQL adapter for users.
type UserStore struct{ pool *pgxpool.Pool }

// NewUserStore returns a UserStore.
func NewUserStore(pool *pgxpool.Pool) *UserStore { return &UserStore{pool: pool} }

// DevGitHubUserID derives a stable, negative github_user_id for a local-only user so it
// can never collide with a real GitHub account id (which are positive). Pure.
func DevGitHubUserID(login string) int64 {
	h := fnv.New64a()
	h.Write([]byte(login))
	return -int64(h.Sum64()>>1) - 1
}

// EnsureDevUser upserts the local development user and returns its principal.
func (s *UserStore) EnsureDevUser(ctx context.Context, login string) (Principal, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (github_user_id, github_login, display_name)
		VALUES ($1, $2, $3)
		ON CONFLICT (github_login) DO UPDATE SET last_login_at = now()
		RETURNING id`, DevGitHubUserID(login), login, login).Scan(&id)
	if err != nil {
		return Principal{}, fmt.Errorf("ensure dev user %q: %w", login, err)
	}
	return Principal{UserID: id, Login: login, Type: ActorUser}, nil
}
