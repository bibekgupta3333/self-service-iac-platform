// Package dbtest provisions throwaway, fully migrated PostgreSQL databases for
// integration tests. Tests are skipped unless IACP_INTEGRATION=1.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/config"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/migrations"
)

// appPassword is the iacp_app password set on the test cluster.
const appPassword = "iacp-app-test-only"

// Skip skips t unless integration tests are enabled.
func Skip(t testing.TB) {
	t.Helper()
	if os.Getenv("IACP_INTEGRATION") != "1" {
		t.Skip("integration test: set IACP_INTEGRATION=1 and run Postgres (docker compose up -d)")
	}
}

// AdminURL returns a connection URL with rights to create databases and roles:
// IACP_TEST_ADMIN_DATABASE_URL, else IACP_MIGRATE_DATABASE_URL, else IACP_DATABASE_URL
// (CI sets this to the superuser), else the local compose default.
func AdminURL() string {
	for _, k := range []string{"IACP_TEST_ADMIN_DATABASE_URL", "IACP_MIGRATE_DATABASE_URL", "IACP_DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return config.DefaultDatabaseURL
}

// NewDatabase creates an isolated, migrated database and returns a pool connected as
// the runtime role iacp_app, exactly like the API. The database is dropped afterwards.
func NewDatabase(t testing.TB) *pgxpool.Pool {
	t.Helper()
	app, _ := NewDatabaseWithOwner(t)
	return app
}

// NewDatabaseWithOwner is NewDatabase plus an owner (superuser) pool on the same database,
// for tests that need DDL or must simulate a privileged attacker.
func NewDatabaseWithOwner(t testing.TB) (app, owner *pgxpool.Pool) {
	t.Helper()
	owner, name := newEmpty(t)
	ctx := context.Background()
	if err := db.NewMigrator(owner, migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil))).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.ProvisionAppRole(ctx, owner, appPassword); err != nil {
		t.Fatalf("provision app role: %v", err)
	}
	u := mustURL(t, name)
	u.User = url.UserPassword(db.AppRole, appPassword)
	app, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(app.Close) // registered last, so it runs before the database is dropped
	return app, owner
}

// NewEmptyDatabase creates an isolated, unmigrated database and returns an owner pool.
func NewEmptyDatabase(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, _ := newEmpty(t)
	return pool
}

func newEmpty(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	Skip(t)
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, AdminURL())
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "iacp_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	pool, err := db.Open(ctx, mustURL(t, name).String())
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool, name
}

func mustURL(t testing.TB, dbName string) *url.URL {
	t.Helper()
	u, err := url.Parse(AdminURL())
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Path = "/" + dbName
	return u
}
