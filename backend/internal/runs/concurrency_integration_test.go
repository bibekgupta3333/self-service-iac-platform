package runs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

// seedStack inserts a team, the order-events stack and one draft request per short id.
func seedStack(t *testing.T, pool *pgxpool.Pool, shortIDs ...string) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var userID, teamID, stackID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (43, 'marco-p') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments', 'Payments', 7, 'payments', 'cc-1') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, created_by)
		SELECT $1, id, 'order-events', $3, 'development/payments/order-events/terraform.tfstate', 'us-east-1', $2
		FROM environments WHERE name = 'development' RETURNING id`, teamID, userID, stackPath).Scan(&stackID); err != nil {
		t.Fatal(err)
	}
	ids := make([]uuid.UUID, len(shortIDs))
	for i, s := range shortIDs {
		if err := pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by, created_at, updated_at)
			VALUES ($1, $2, 'update', '{}', $3, now() + $4 * interval '1 ms', now() + $4 * interval '1 ms') RETURNING id`,
			s, stackID, userID, i).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	return stackID, ids
}

// Run reports and lock release/promotion on the same stack must never deadlock: both
// paths lock the stacks row before the locks row.
func TestRecordAndReleaseDoNotDeadlock(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	stackID, reqs := seedStack(t, pool, "req-aaaaaaaaaa", "req-bbbbbbbbbb")
	short := map[uuid.UUID]string{reqs[0]: "req-aaaaaaaaaa", reqs[1]: "req-bbbbbbbbbb"}

	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, time.Hour)
	runSvc := runs.NewService(runs.NewPostgresRepository(pool), platform.SystemClock{})
	worker := identity.Principal{Login: "internal-token", Type: identity.ActorGitHubActions}
	for _, id := range reqs {
		if _, err := lockSvc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: id, By: worker}); err != nil {
			t.Fatal(err)
		}
	}

	const iterations = 150
	var wg sync.WaitGroup
	errs := make(chan error, 2*iterations)
	wg.Add(2)
	go func() { // apply job reporting under whatever lease is current
		defer wg.Done()
		for i := range iterations {
			l, err := lockSvc.Get(ctx, stackID)
			if err != nil {
				errs <- err
				continue
			}
			tok := l.FencingToken
			_, err = runSvc.Record(ctx, worker, runs.Report{Pipeline: runs.KindApply, StackPath: stackPath,
				RequestShortID: short[l.RequestID], FencingToken: &tok, Stage: "apply", Status: runs.StageInProgress,
				GitHub: runs.GitHubRun{RunID: int64(1000 + i), RunAttempt: 1, HeadSHA: strings.Repeat("cd", 20)}})
			if err != nil && !hasCode(err, "stale_fencing_token") {
				errs <- fmt.Errorf("record %d: %w", i, err)
			}
		}
	}()
	go func() { // worker releasing the holder, promoting the waiter, re-queueing the old holder
		defer wg.Done()
		for i := range iterations {
			l, err := lockSvc.Get(ctx, stackID)
			if err != nil {
				errs <- err
				continue
			}
			if _, err := lockSvc.Release(ctx, locks.TokenCommand{StackID: stackID, RequestID: l.RequestID, FencingToken: l.FencingToken, By: worker}); err != nil {
				errs <- fmt.Errorf("release %d: %w", i, err)
				continue
			}
			if _, err := lockSvc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: l.RequestID, By: worker}); err != nil {
				errs <- fmt.Errorf("re-acquire %d: %w", i, err)
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("deadlock: %v", err)
		}
		t.Errorf("unexpected error: %v", err)
	}
}
