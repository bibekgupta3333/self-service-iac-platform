package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
)

// The sweeper can remove processed jobs older than the cutoff and nothing else, although
// iacp_app itself has no DELETE on outbox.
func TestPurgeProcessedRemovesOnlyOldFinishedJobs(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	store := NewPostgresStore(pool)
	base := time.Now().UTC().Truncate(time.Microsecond)

	var ids []int64
	for i := range 5 {
		if _, err := Enqueue(ctx, pool, Message{Topic: TopicGitHubEvent, Payload: json.RawMessage(`{}`), AvailableAt: base.Add(time.Duration(i) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		j, err := store.Claim(ctx, base.Add(time.Hour), time.Minute)
		if err != nil || j == nil {
			t.Fatalf("claim: %v %v", j, err)
		}
		ids = append(ids, j.ID)
	}
	// Two old processed jobs, one recent processed job, one dead, one left claimed (pending).
	if err := store.Complete(ctx, ids[0], 1, base.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, ids[1], 1, base.Add(-47*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, ids[2], 1, base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Dead(ctx, ids[3], 1, base.Add(-72*time.Hour), "gave up"); err != nil {
		t.Fatal(err)
	}

	cutoff := base.Add(-24 * time.Hour)
	if n, err := store.PurgeProcessed(ctx, cutoff, 1); err != nil || n != 1 {
		t.Fatalf("first batch of 1: removed %d, %v", n, err)
	}
	if n, err := store.PurgeProcessed(ctx, cutoff, 100); err != nil || n != 1 {
		t.Fatalf("second batch: removed %d, %v", n, err)
	}
	if n, err := store.PurgeProcessed(ctx, cutoff, 100); err != nil || n != 0 {
		t.Fatalf("nothing old is left: removed %d, %v", n, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&left); err != nil || left != 3 {
		t.Fatalf("recent processed, dead and pending rows must survive: %d left, %v", left, err)
	}
	if n, err := store.PurgeProcessed(ctx, cutoff, 0); err != nil || n != 0 {
		t.Fatalf("a zero limit removes nothing: %d, %v", n, err)
	}
	// The grant is the function only: the runtime role still cannot delete rows itself.
	if _, err := pool.Exec(ctx, `DELETE FROM outbox`); err == nil {
		t.Fatal("iacp_app must not be able to DELETE FROM outbox")
	}
}
