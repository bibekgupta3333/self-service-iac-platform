package locks_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

var worker = identity.Principal{Login: "iacp-worker", Type: identity.ActorSystem}

// fixture inserts a team, a stack and n draft requests created one second apart.
func fixture(t *testing.T, pool *pgxpool.Pool, n int) (stackID uuid.UUID, reqs []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var userID, teamID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments', 'Payments', 7, 'payments', 'cc-4102') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, created_by)
		SELECT $1, id, 'order-events', 'environments/development/payments/order-events',
		       'development/payments/order-events/terraform.tfstate', 'us-east-1', $2
		FROM environments WHERE name = 'development' RETURNING id`, teamID, userID).Scan(&stackID); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for i := range n {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by, created_at, updated_at)
			VALUES ($1, $2, 'update', '{}', $3, $4, $4) RETURNING id`,
			fmt.Sprintf("req-%010d", i), stackID, userID, base.Add(time.Duration(i)*time.Second)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, id)
	}
	return stackID, reqs
}

func queuedBehind(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (behind *uuid.UUID, token *int64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT queued_behind, fencing_token FROM requests WHERE id = $1`, id).
		Scan(&behind, &token); err != nil {
		t.Fatal(err)
	}
	return behind, token
}

// Two acquirers race for a free stack: exactly one wins, the other queues behind it.
func TestConcurrentAcquireHasOneWinner(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	svc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, time.Hour)

	for round := range 10 {
		t.Run(fmt.Sprint("round ", round), func(t *testing.T) {
			if _, err := pool.Exec(ctx, `DELETE FROM locks; DELETE FROM requests; DELETE FROM stacks; DELETE FROM teams; DELETE FROM users WHERE NOT is_system`); err != nil {
				t.Fatal(err)
			}
			stackID, reqs := fixture(t, pool, 2)
			results := make([]locks.Result, 2)
			errs := make([]error, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results[i], errs[i] = svc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: reqs[i], By: worker})
				}()
			}
			close(start)
			wg.Wait()

			winners := 0
			var winner, loser int
			for i := range 2 {
				if errs[i] != nil {
					t.Fatalf("acquirer %d: %v", i, errs[i])
				}
				if results[i].Outcome == locks.OutcomeGranted {
					winners++
					winner = i
				} else {
					loser = i
				}
			}
			if winners != 1 {
				t.Fatalf("winners = %d, want exactly 1: %+v", winners, results)
			}
			if results[loser].Outcome != locks.OutcomeQueued || *results[loser].QueuedBehind != reqs[winner] {
				t.Fatalf("loser = %+v", results[loser])
			}
			if behind, _ := queuedBehind(t, pool, reqs[loser]); behind == nil || *behind != reqs[winner] {
				t.Fatalf("loser queued_behind = %v", behind)
			}
			if _, tok := queuedBehind(t, pool, reqs[winner]); tok == nil || *tok != results[winner].Lock.FencingToken {
				t.Fatalf("winner fencing_token = %v", tok)
			}
		})
	}
}

func TestPostgresLockLifecycle(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	clock := &stepClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	start := clock.t
	svc := locks.NewService(locks.NewPostgresRepository(pool, writer), clock, time.Hour)
	stackID, reqs := fixture(t, pool, 3)
	a, b, c := reqs[0], reqs[1], reqs[2]

	ga, err := svc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: a, By: worker})
	if err != nil || ga.Outcome != locks.OutcomeGranted || ga.Lock.Holder != "request:req-0000000000" {
		t.Fatalf("acquire a = %+v, %v", ga, err)
	}
	for _, id := range []uuid.UUID{c, b} {
		res, err := svc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: id, By: worker})
		if err != nil || res.Outcome != locks.OutcomeQueued || *res.QueuedBehind != a {
			t.Fatalf("acquire %s = %+v, %v", id, res, err)
		}
	}
	got, err := svc.Get(ctx, stackID)
	if err != nil || got.RequestID != a || got.FencingToken != ga.Lock.FencingToken || !got.ExpiresAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("get = %+v, %v", got, err)
	}

	clock.t = start.Add(30 * time.Minute)
	hb, err := svc.Heartbeat(ctx, locks.TokenCommand{StackID: stackID, RequestID: a, FencingToken: ga.Lock.FencingToken})
	if err != nil || !hb.ExpiresAt.Equal(clock.t.Add(time.Hour)) || hb.FencingToken != ga.Lock.FencingToken {
		t.Fatalf("heartbeat = %+v, %v", hb, err)
	}

	// Release promotes b (created before c), with a larger token; c now waits behind b.
	rel, err := svc.Release(ctx, locks.TokenCommand{StackID: stackID, RequestID: a, FencingToken: ga.Lock.FencingToken, By: worker})
	if err != nil || rel.Promoted == nil || rel.Promoted.RequestID != b || rel.Promoted.FencingToken <= ga.Lock.FencingToken {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	if behind, tok := queuedBehind(t, pool, b); behind != nil || tok == nil || *tok != rel.Promoted.FencingToken {
		t.Fatalf("b after promotion: behind=%v token=%v", behind, tok)
	}
	if behind, _ := queuedBehind(t, pool, c); behind == nil || *behind != b {
		t.Fatalf("c after promotion: behind=%v", behind)
	}
	if _, err := svc.Release(ctx, locks.TokenCommand{StackID: stackID, RequestID: a, FencingToken: ga.Lock.FencingToken}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("stale release: %v", err)
	}

	// b's lease expires; c takes it over with a new token, and b's late heartbeat is stale.
	clock.t = rel.Promoted.ExpiresAt.Add(time.Second)
	gc, err := svc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: c, By: worker})
	if err != nil || gc.Outcome != locks.OutcomeGranted || gc.Lock.FencingToken <= rel.Promoted.FencingToken {
		t.Fatalf("takeover = %+v, %v", gc, err)
	}
	if _, err := svc.Heartbeat(ctx, locks.TokenCommand{StackID: stackID, RequestID: b, FencingToken: rel.Promoted.FencingToken}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("superseded heartbeat: %v", err)
	}

	if _, err := svc.Acquire(ctx, locks.AcquireCommand{StackID: uuid.New(), RequestID: a}); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("unknown stack: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE subject_type = 'stack' AND subject_id = $1`, stackID.String()).Scan(&n); err != nil || n != 6 {
		t.Fatalf("audit events = %d, %v (want acquired, 2x queued, released, acquired, acquired)", n, err)
	}
	if res, err := audit.NewVerifier(pool).Verify(ctx, 1, 0); err != nil || !res.Valid {
		t.Fatalf("audit chain = %+v, %v", res, err)
	}
}

type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}
