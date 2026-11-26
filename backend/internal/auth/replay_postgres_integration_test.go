package auth_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestPostgresReplayStore(t *testing.T) {
	pool := dbtest.NewDatabase(t) // connected as iacp_app, like the API
	ctx := context.Background()
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	store := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0})

	if err := store.Claim(ctx, "jti-a", t0.Add(5*time.Minute)); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := store.Claim(ctx, "jti-a", t0.Add(5*time.Minute)); !errors.Is(err, auth.ErrTokenReplayed) {
		t.Fatalf("replay: err = %v", err)
	}

	// After expiry the row is purged and the id can be claimed again.
	later := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0.Add(6 * time.Minute)})
	if err := later.Claim(ctx, "jti-b", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM oidc_jti WHERE jti = 'jti-a'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired row not purged: n=%d err=%v", n, err)
	}
	if err := later.Claim(ctx, "jti-a", t0.Add(time.Hour)); err != nil {
		t.Fatalf("expired id must be claimable again: %v", err)
	}
}

func TestPostgresReplayStoreConcurrentReplicas(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 { // eight "replicas" presented the same token at once
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0})
			err := s.Claim(context.Background(), "shared", t0.Add(time.Minute))
			switch {
			case err == nil:
				admitted.Add(1)
			case !errors.Is(err, auth.ErrTokenReplayed):
				t.Errorf("replica %d: %v", i, fmt.Errorf("unexpected: %w", err))
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted %d times, want exactly 1", admitted.Load())
	}
}
