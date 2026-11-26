package webhooks

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestPostgresIngestDedupe(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	repo := NewPostgresRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := NewIngester(repo, platform.FixedClock{T: now}, "acme/infra")
	d := Delivery{ID: uuid.New(), Event: "pull_request", Body: prBody("closed", true, "iacp/req-7k3p9x")}

	if res, err := in.Ingest(ctx, d); err != nil || !res.Enqueued {
		t.Fatalf("first: %+v, %v", res, err)
	}
	if res, err := in.Ingest(ctx, d); err != nil || !res.Duplicate || res.Enqueued {
		t.Fatalf("duplicate: %+v, %v", res, err)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'github.event'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs = %d, %v", jobs, err)
	}
	// The 7-day purge runs as iacp_app.
	if n, err := repo.PurgeDeliveries(ctx, now.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
}
