package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
)

func TestPostgresQueue(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	agg := uuid.New()

	ok, err := Enqueue(ctx, pool, Message{Topic: TopicRequestPublish, AggregateID: &agg, DedupeKey: "publish:" + agg.String(),
		Payload: json.RawMessage(`{"request_id":"x"}`)})
	if err != nil || !ok {
		t.Fatalf("enqueue: %v, %v", ok, err)
	}
	// Same dedupe key while pending: no second job.
	if ok, err := Enqueue(ctx, pool, Message{Topic: TopicRequestPublish, DedupeKey: "publish:" + agg.String(), Payload: json.RawMessage(`{}`)}); err != nil || ok {
		t.Fatalf("duplicate enqueue: %v, %v", ok, err)
	}
	if _, err := Enqueue(ctx, pool, Message{Topic: TopicGitHubEvent, Payload: json.RawMessage(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Millisecond)
	if _, err := Enqueue(ctx, pool, Message{Topic: TopicGitHubEvent, Payload: json.RawMessage(`{}`), AvailableAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	// Two concurrent claimers never get the same job.
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		claims []*Job
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := store.Claim(ctx, now, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			claims = append(claims, j)
			mu.Unlock()
		}()
	}
	wg.Wait()
	got := map[int64]bool{}
	nils := 0
	for _, j := range claims {
		if j == nil {
			nils++
			continue
		}
		if got[j.ID] {
			t.Fatalf("job %d claimed twice", j.ID)
		}
		got[j.ID] = true
		if j.Attempts != 1 {
			t.Fatalf("attempts = %d", j.Attempts)
		}
	}
	if len(got) != 2 || nils != 2 {
		t.Fatalf("claimed %v (nil %d); the future job must wait", got, nils)
	}

	// While leased, nothing else is claimable; once the lease expires the job comes back.
	if j, _ := store.Claim(ctx, now.Add(30*time.Second), time.Minute); j != nil {
		t.Fatalf("leased job reclaimed: %+v", j)
	}
	j, err := store.Claim(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil || j == nil || j.Attempts != 2 {
		t.Fatalf("expired lease: %+v, %v", j, err)
	}
	// The first claim of this job (attempt 1) lost its lease: its outcome is refused.
	if err := store.Complete(ctx, j.ID, 1, now); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale claim completed the job: %v", err)
	}
	if err := store.Retry(ctx, j.ID, j.Attempts, now.Add(10*time.Minute), "transient"); err != nil {
		t.Fatal(err)
	}
	var other int64
	for id := range got {
		if id != j.ID {
			other = id
		}
	}
	if err := store.Complete(ctx, other, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Dead(ctx, j.ID, j.Attempts, now, "gave up"); err != nil {
		t.Fatal(err)
	}
	// A processed or dead job frees its dedupe key.
	if ok, err := Enqueue(ctx, pool, Message{Topic: TopicRequestPublish, DedupeKey: "publish:" + agg.String(), Payload: json.RawMessage(`{}`)}); err != nil || !ok {
		t.Fatalf("re-enqueue after completion: %v, %v", ok, err)
	}
	var pending, processed, dead int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE processed_at IS NULL AND dead_at IS NULL),
		count(*) FILTER (WHERE processed_at IS NOT NULL), count(*) FILTER (WHERE dead_at IS NOT NULL) FROM outbox`).
		Scan(&pending, &processed, &dead); err != nil {
		t.Fatal(err)
	}
	if pending != 2 || processed != 1 || dead != 1 {
		t.Fatalf("pending %d processed %d dead %d", pending, processed, dead)
	}
}
