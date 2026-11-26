package oauth_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
)

func TestPostgresStateStore(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	store := oauth.NewPostgresStateStore(pool)
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	rec := oauth.StateRecord{StateHash: oauth.HashState("s1"), Verifier: "v1", ReturnTo: "/x", CreatedAt: now, ExpiresAt: now.Add(oauth.StateTTL)}
	if err := store.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := store.Consume(ctx, rec.StateHash, now.Add(time.Minute))
	if err != nil || got.Verifier != "v1" || got.ReturnTo != "/x" {
		t.Fatalf("Consume = %+v, %v", got, err)
	}
	if _, err := store.Consume(ctx, rec.StateHash, now.Add(time.Minute)); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("second Consume: %v", err)
	}

	// Expired rows cannot be consumed and are purged.
	old := oauth.StateRecord{StateHash: oauth.HashState("s2"), Verifier: "v2", ReturnTo: "/", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	_ = store.Save(ctx, old)
	if _, err := store.Consume(ctx, old.StateHash, now.Add(time.Minute)); !errors.Is(err, oauth.ErrInvalidState) {
		t.Fatalf("expired Consume: %v", err)
	}
	if n, err := store.Purge(ctx, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
}

func TestPostgresStateStoreConsumeIsAtomic(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	store := oauth.NewPostgresStateStore(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	h := oauth.HashState("race")
	if err := store.Save(ctx, oauth.StateRecord{StateHash: h, Verifier: "v", ReturnTo: "/", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Consume(ctx, h, now); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d concurrent callbacks consumed the same state, want exactly 1", wins.Load())
	}
}
