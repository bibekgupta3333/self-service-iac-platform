package db_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/migrations"
)

func TestMigrationsApplyAndAreIdempotent(t *testing.T) {
	_, pool := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	m := db.NewMigrator(pool, migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := m.Up(ctx); err != nil { // second run is a no-op
		t.Fatalf("re-run migrations: %v", err)
	}
	v, err := m.Version(ctx)
	if want := latestMigration(t); err != nil || v != want {
		t.Fatalf("version = %d, %v; want %d", v, err, want)
	}

	for _, table := range []string{
		"teams", "users", "memberships", "modules", "module_versions", "environments", "stacks",
		"stack_resources", "requests", "locks", "idempotency_keys", "audit_events", "pipeline_runs",
		"oidc_jti", "budgets", "webhook_deliveries", "outbox", "apply_failures",
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

// latestMigration is the highest NNNN prefix among the embedded migrations.
func latestMigration(t *testing.T) int64 {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("list migrations: %v", err)
	}
	var latest int64
	for _, n := range names {
		v, err := strconv.ParseInt(strings.SplitN(n, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("migration %s has no numeric prefix", n)
		}
		latest = max(latest, v)
	}
	return latest
}

func TestRequestCompletedAtCheck(t *testing.T) {
	_, pool := dbtest.NewDatabaseWithOwner(t)
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

func TestRuntimeRoleIsLeastPrivileged(t *testing.T) {
	app, owner := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()

	if p, err := db.Privileged(ctx, app); err != nil || p {
		t.Fatalf("app pool privileged = %v, %v; want false", p, err)
	}
	if p, err := db.Privileged(ctx, owner); err != nil || !p {
		t.Fatalf("owner pool privileged = %v, %v; want true", p, err)
	}
	for _, stmt := range []string{
		"CREATE TABLE sneaky (n int)",
		"SELECT * FROM goose_db_version",
		"DROP TABLE requests",
		"DELETE FROM pipeline_runs",                 // run reports are insert/update only (0005)
		"UPDATE oidc_jti SET expires_at = now()",    // replay ids are insert/purge only (0006)
		"DELETE FROM outbox",                        // jobs are completed, never deleted (0008)
		"UPDATE webhook_deliveries SET event = 'x'", // deliveries are insert-only, purged by DELETE (0008)
		"DELETE FROM apply_failures",                // classified failures are insert/update only (0015)
		"TRUNCATE locks",
	} {
		if _, err := app.Exec(ctx, stmt); err == nil {
			t.Errorf("iacp_app could run %q", stmt)
		}
	}
	// Ordinary DML on platform tables is allowed.
	if _, err := app.Exec(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('ops', 'Ops', 99, 'ops', 'cc-9')`); err != nil {
		t.Fatalf("iacp_app cannot insert teams: %v", err)
	}
	if _, err := app.Exec(ctx, `DELETE FROM teams WHERE slug = 'ops'`); err != nil {
		t.Fatalf("iacp_app cannot delete teams: %v", err)
	}
	// Provisioning is idempotent.
	if err := db.ProvisionAppRole(ctx, owner, "iacp-app-test-only"); err != nil {
		t.Fatal(err)
	}
}
