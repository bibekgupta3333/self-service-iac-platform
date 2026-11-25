package db_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/migrations"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Several databases migrating at once all create the cluster-wide iacp_app role.
func TestConcurrentDatabasesMigrate(t *testing.T) {
	const n = 4
	pools := make([]*db.Migrator, n)
	for i := range n {
		pools[i] = db.NewMigrator(dbtest.NewEmptyDatabase(t), migrations.FS, quietLogger())
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = pools[i].Up(context.Background()) }()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("database %d: %v", i, err)
		}
	}
}

func TestNoPgcryptoExtension(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	var installed bool
	if err := pool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto')").Scan(&installed); err != nil || installed {
		t.Fatalf("pgcrypto installed = %v (%v); gen_random_uuid is built in", installed, err)
	}
}
