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

// Skip skips t unless integration tests are enabled.
func Skip(t testing.TB) {
	t.Helper()
	if os.Getenv("IACP_INTEGRATION") != "1" {
		t.Skip("integration test: set IACP_INTEGRATION=1 and run Postgres (docker compose up -d)")
	}
}

// BaseURL returns the admin connection URL (IACP_DATABASE_URL or the local default).
func BaseURL() string {
	if v := os.Getenv("IACP_DATABASE_URL"); v != "" {
		return v
	}
	return config.DefaultDatabaseURL
}

// NewDatabase creates an isolated database, applies all migrations and returns a pool.
// The database is dropped when the test finishes.
func NewDatabase(t testing.TB) *pgxpool.Pool {
	t.Helper()
	Skip(t)
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, BaseURL())
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

	u, err := url.Parse(BaseURL())
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	m := db.NewMigrator(pool, migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := m.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}
