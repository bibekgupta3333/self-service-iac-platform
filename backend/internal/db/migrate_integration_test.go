package db_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/migrations"
)

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	m := db.NewMigrator(pool, migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := m.Up(ctx); err != nil { // second run is a no-op
		t.Fatalf("re-run migrations: %v", err)
	}
	v, err := m.Version(ctx)
	if err != nil || v != 3 {
		t.Fatalf("version = %d, %v; want 3", v, err)
	}

	for _, table := range []string{
		"teams", "users", "memberships", "modules", "module_versions", "environments", "stacks",
		"stack_resources", "requests", "locks", "idempotency_keys", "audit_events",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass('public.'||$1) IS NOT NULL", table).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s missing (err=%v)", table, err)
		}
	}

	var env string
	if err := pool.QueryRow(ctx, "SELECT aws_account_id FROM environments WHERE name='development'").Scan(&env); err != nil || env != "000000000000" {
		t.Fatalf("seeded development env = %q, %v", env, err)
	}
}

func TestRequestCompletedAtCheck(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		WITH u AS (SELECT id FROM users WHERE github_login='iacp-bot'),
		     tm AS (INSERT INTO teams (slug,name,github_team_id,github_team_slug,cost_center)
		            VALUES ('payments','Payments',1,'payments','cc-1') RETURNING id),
		     s AS (INSERT INTO stacks (team_id,environment_id,name,path,state_key,region,created_by)
		           SELECT tm.id, e.id, 'orders', 'p', 'k', 'us-east-1', u.id
		           FROM tm, u, environments e WHERE e.name='development' RETURNING id)
		INSERT INTO requests (short_id, stack_id, kind, status, spec, created_by)
		SELECT 'req-x', s.id, 'create', 'applied', '{}', u.id FROM s, u`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("expected CHECK violation for applied request without completed_at, got %v", err)
	}
}
