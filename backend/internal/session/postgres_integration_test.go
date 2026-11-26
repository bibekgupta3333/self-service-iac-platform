package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

func newUser(t *testing.T, pool *pgxpool.Pool, login string, admin bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (github_user_id, github_login, is_platform_admin) VALUES ($1, $2, $3) RETURNING id`,
		time.Now().UnixNano(), login, admin).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPostgresStoreLifecycle(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	clock := platform.FixedClock{T: now}
	pol := session.Policy{AbsoluteTTL: 8 * time.Hour, IdleTTL: 30 * time.Minute}
	svc := session.NewService(session.NewPostgresStore(pool), pol, clock)

	user := newUser(t, pool, "priya", true)
	id, err := svc.Issue(ctx, user)
	if err != nil {
		t.Fatal(err)
	}

	// Only the hash is stored.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id_hash = $1`, session.HashID(id)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("hash row count = %d, %v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE encode(id_hash, 'escape') LIKE '%' || $1 || '%'`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("raw id found in sessions: %d, %v", n, err)
	}

	rec, err := svc.Resolve(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p := rec.Principal(); p.UserID != user || p.Login != "priya" || !p.PlatformAdmin {
		t.Fatalf("principal = %+v", p)
	}

	// A later request slides the idle window in the database.
	later := session.NewService(session.NewPostgresStore(pool), pol, platform.FixedClock{T: now.Add(20 * time.Minute)})
	if _, err := later.Resolve(ctx, id); err != nil {
		t.Fatal(err)
	}
	almost := session.NewService(session.NewPostgresStore(pool), pol, platform.FixedClock{T: now.Add(45 * time.Minute)})
	if _, err := almost.Resolve(ctx, id); err != nil {
		t.Fatalf("idle window did not slide: %v", err)
	}
	idle := session.NewService(session.NewPostgresStore(pool), pol, platform.FixedClock{T: now.Add(80 * time.Minute)})
	if _, err := idle.Resolve(ctx, id); !errors.Is(err, session.ErrIdle) {
		t.Fatalf("idle: %v", err)
	}

	// Disabling the user invalidates the session immediately.
	if _, err := pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := later.Resolve(ctx, id); !errors.Is(err, session.ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET disabled_at = NULL WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}

	if err := svc.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, id); !errors.Is(err, session.ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}

	// Purge removes revoked and expired rows but keeps live ones.
	live, _ := svc.Issue(ctx, user)
	if cnt, err := svc.Purge(ctx); err != nil || cnt != 1 {
		t.Fatalf("Purge = %d, %v (want the revoked row only)", cnt, err)
	}
	if _, err := svc.Resolve(ctx, live); err != nil {
		t.Fatalf("live session purged: %v", err)
	}
	expired := session.NewService(session.NewPostgresStore(pool), pol, platform.FixedClock{T: now.Add(9 * time.Hour)})
	if cnt, err := expired.Purge(ctx); err != nil || cnt != 1 {
		t.Fatalf("Purge expired = %d, %v", cnt, err)
	}
}

func TestPostgresStoreSessionsDieWithTheUser(t *testing.T) {
	dbtest.Skip(t)
	pool, owner := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	svc := session.NewService(session.NewPostgresStore(pool), session.Policy{AbsoluteTTL: time.Hour, IdleTTL: time.Hour}, platform.SystemClock{})
	user := newUser(t, pool, "gone", false)
	id, _ := svc.Issue(ctx, user)
	if _, err := owner.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, id); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted user's session: %v", err)
	}
}
