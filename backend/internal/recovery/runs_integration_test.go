package recovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

// TestRunReportsRetryATransientApplyFailure drives the whole path against Postgres: a
// failed apply stage reported on /v1/runs (runs.Service with the request lifecycle and the
// recovery policy) leaves the request applying and schedules a retry; when the stack lock
// then goes to another request, the dispatcher fails the request instead of retrying.
func TestRunReportsRetryATransientApplyFailure(t *testing.T) {
	pool, reqID, stackID, token := seedApplying(t)
	ctx := context.Background()
	clock := platform.SystemClock{}
	w := audit.NewWriter(clock, platform.RandomIDs{})
	reqRepo := requests.NewPostgresRepository(pool, w)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, w), clock, time.Hour)
	reqSvc := requests.NewService(reqRepo, nil, reqRepo, lockSvc, clock, platform.RandomIDs{})
	driver := requests.NewApplyDriver(reqSvc, w)
	recRepo := NewPostgresRepository(pool, w).WithLifecycle(driver.ForTx)
	retrier := NewRetrier(recRepo, clock, platform.RandomIDs{})
	driver.WithRetry(retrier.Policy(recRepo))
	runSvc := runs.NewService(runs.NewPostgresRepository(pool, driver), clock)
	if _, err := pool.Exec(ctx, `UPDATE requests SET merge_commit_sha = $2 WHERE id = $1`, reqID, strings.Repeat("e4", 20)); err != nil {
		t.Fatal(err)
	}

	bot := identity.Principal{Login: "internal", Type: identity.ActorGitHubActions}
	report := func(stage string, status runs.StageStatus, errMsg string) runs.Report {
		r := runs.Report{Pipeline: runs.KindApply, StackPath: itStackPath, RequestShortID: "req-7k3p9x", FencingToken: &token,
			GitHub: runs.GitHubRun{RunID: 5001, RunAttempt: 1, HeadSHA: strings.Repeat("e4", 20)}, Stage: stage, Status: status,
			TerraformExitCode: ptr(1)}
		if errMsg != "" {
			r.Error, _ = json.Marshal(map[string]string{"class": "permanent", "message": errMsg})
		}
		return r
	}
	if _, err := runSvc.Record(ctx, bot, report("apply", runs.StageFailed, "│ Error: creating SQS Queue: api error ThrottlingException: Rate exceeded")); err != nil {
		t.Fatal(err)
	}
	if _, err := runSvc.Record(ctx, bot, report("summary", runs.StageFailed, "apply.yml did not finish (failure)")); err != nil {
		t.Fatal(err)
	}
	var (
		status, stackStatus string
		retryCount          int
	)
	mustScan(t, pool.QueryRow(ctx, `SELECT r.status::text, r.retry_count, s.status::text FROM requests r JOIN stacks s ON s.id = r.stack_id
		WHERE r.id = $1`, reqID).Scan(&status, &retryCount, &stackStatus))
	if status != "applying" || retryCount != 1 || stackStatus != "updating" {
		t.Fatalf("request %s retry_count %d stack %s", status, retryCount, stackStatus)
	}
	var jobs int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = $1`, TopicApplyRetry).Scan(&jobs))
	if jobs != 1 {
		t.Fatalf("%d retry jobs", jobs)
	}

	// apply.yml releases the lock; meanwhile another request was waiting and gets it.
	var other uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
		SELECT 'req-waiter01', $1, 'update', '{}', created_by FROM requests WHERE id = $2 RETURNING id`, stackID, reqID).Scan(&other))
	if _, err := pool.Exec(ctx, `UPDATE locks SET request_id = $1, holder = 'request:req-waiter01' WHERE stack_id = $2`, other, stackID); err != nil {
		t.Fatal(err)
	}
	var payload json.RawMessage
	mustScan(t, pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE topic = $1`, TopicApplyRetry).Scan(&payload))
	pub := &fakePublisher{}
	if err := NewDispatcher(recRepo, pub, lockSvc, clock, "main").Handle(ctx, outbox.Job{ID: 9, Payload: payload, Attempts: 1, MaxAttempts: 6}); err != nil {
		t.Fatal(err)
	}
	if len(pub.dispatches) != 0 {
		t.Fatal("no retry may run while another request holds the lock")
	}
	var reason string
	mustScan(t, pool.QueryRow(ctx, `SELECT r.status::text, coalesce(r.failure_reason, ''), s.status::text FROM requests r
		JOIN stacks s ON s.id = r.stack_id WHERE r.id = $1`, reqID).Scan(&status, &reason, &stackStatus))
	// The waiter holds the lock and may be applying: the stack is left to it.
	if status != "failed" || reason != FailureRetryNotRun || stackStatus != "updating" {
		t.Fatalf("request %s (%s), stack %s", status, reason, stackStatus)
	}
}
