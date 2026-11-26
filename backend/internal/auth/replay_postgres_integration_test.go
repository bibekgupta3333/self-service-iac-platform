package auth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

	// Claiming another id leaves unrelated expired rows to the sweeper (no global purge
	// on the hot path); the expired id itself can be claimed again.
	later := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0.Add(6 * time.Minute)})
	if err := later.Claim(ctx, "jti-b", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM oidc_jti WHERE jti = 'jti-a'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("an unrelated claim must not purge jti-a: n=%d err=%v", n, err)
	}
	if err := later.Claim(ctx, "jti-a", t0.Add(time.Hour)); err != nil {
		t.Fatalf("expired id must be claimable again: %v", err)
	}
}

func TestPostgresReplayStorePurgeIsBounded(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	s := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0})
	for i := range 5 {
		if err := s.Claim(ctx, fmt.Sprintf("old-%d", i), t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Claim(ctx, "live", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sweeper := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0.Add(2 * time.Minute)})
	for _, want := range []int64{2, 2, 1, 0} {
		got, err := sweeper.PurgeExpired(ctx, 2)
		if err != nil || got != want {
			t.Fatalf("PurgeExpired = %d, %v; want %d", got, err, want)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM oidc_jti`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("live row must survive: n=%d err=%v", n, err)
	}
}

func TestRunReplaySweeperStopsWithContext(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	s := auth.NewPostgresReplayStore(pool, platform.FixedClock{T: t0})
	if err := s.Claim(context.Background(), "old", t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		auth.RunReplaySweeper(ctx, s, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM oidc_jti`).Scan(&n); err == nil && n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sweeper did not purge the expired row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
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
