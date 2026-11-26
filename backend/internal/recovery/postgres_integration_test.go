package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

const itStackPath = "environments/development/payments/order-events"

// seedApplying inserts a user, team, stack and an applying update request that holds the
// stack lock, and returns the pool, the request id, the stack id and the lock's token.
func seedApplying(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID, int64) {
	t.Helper()
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	var userID, teamID, stackID, reqID uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID))
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments', 'Payments', 7, 'payments', 'cc-1') RETURNING id`).Scan(&teamID))
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, created_by, status)
		SELECT $1, id, 'order-events', $3, 'development/payments/order-events/terraform.tfstate', 'us-east-1', $2, 'updating'
		FROM environments WHERE name = 'development' RETURNING id`, teamID, userID, itStackPath).Scan(&stackID))
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, status, spec, created_by, approved_plan_hash)
		VALUES ('req-7k3p9x', $1, 'update', 'applying', '{}', $2, $3) RETURNING id`, stackID, userID, strings.Repeat("c", 64)).Scan(&reqID))
	w := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	grant, err := locks.NewService(locks.NewPostgresRepository(pool, w), platform.SystemClock{}, time.Hour).
		Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: reqID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE requests SET fencing_token = $2 WHERE id = $1`, reqID, grant.Lock.FencingToken); err != nil {
		t.Fatal(err)
	}
	return pool, reqID, stackID, grant.Lock.FencingToken
}

func mustScan(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRetryLifecycle(t *testing.T) {
	pool, reqID, _, token := seedApplying(t)
	ctx := context.Background()
	w := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	repo := lifecycleRepo(pool, w)
	clock := platform.FixedClock{T: time.Now().UTC().Truncate(time.Microsecond)}
	retrier := NewRetrier(repo, clock, platform.RandomIDs{})
	failure := FailedApply{RequestShortID: "req-7k3p9x", StackPath: itStackPath, FencingToken: &token,
		Run: RunRef{RunID: 9001, Attempt: 1, HeadSHA: strings.Repeat("e4", 20)}, Stage: "apply", ExitCode: ptr(1),
		Message: "Error: creating SQS Queue: https response error StatusCode: 503, api error ServiceUnavailable", CorrelationID: "it-1"}

	out, err := retrier.OnApplyFailure(ctx, failure)
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != DecisionRetryScheduled || out.RetryNumber != 1 || out.Reason != "provider_5xx" {
		t.Fatalf("outcome %+v", out)
	}
	var retryCount int
	mustScan(t, pool.QueryRow(ctx, `SELECT retry_count FROM requests WHERE id = $1`, reqID).Scan(&retryCount))
	if retryCount != 1 {
		t.Fatalf("retry_count %d", retryCount)
	}
	var class, decision, state string
	mustScan(t, pool.QueryRow(ctx, `SELECT failure_class::text, decision, dispatch_state FROM apply_failures WHERE request_id = $1`, reqID).
		Scan(&class, &decision, &state))
	if class != "transient" || decision != DecisionRetryScheduled || state != DispatchScheduled {
		t.Fatalf("row %s %s %s", class, decision, state)
	}

	// A replayed report is recorded once.
	if again, err := retrier.OnApplyFailure(ctx, failure); err != nil || !again.Duplicate {
		t.Fatalf("replay %+v, %v", again, err)
	}

	// The job is in the outbox, not available before the backoff.
	var (
		jobID   int64
		payload []byte
		avail   time.Time
	)
	mustScan(t, pool.QueryRow(ctx, `SELECT id, payload, available_at FROM outbox WHERE topic = $1`, TopicApplyRetry).Scan(&jobID, &payload, &avail))
	if !avail.Equal(clock.T.Add(2 * time.Minute)) {
		t.Fatalf("available_at %s, want %s", avail, clock.T.Add(2*time.Minute))
	}

	pub := &fakePublisher{}
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, w), platform.SystemClock{}, time.Hour)
	// apply.yml released the lock when the failed run ended.
	if _, err := pool.Exec(ctx, `DELETE FROM locks WHERE request_id = $1`, reqID); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(repo, pub, lockSvc, clock, "main")
	job := outbox.Job{ID: jobID, Topic: TopicApplyRetry, Payload: payload, Attempts: 1, MaxAttempts: 6}
	if err := d.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if len(pub.dispatches) != 1 || pub.dispatches[0].Inputs["request_id"] != reqID.String() {
		t.Fatalf("dispatches %+v", pub.dispatches)
	}
	// The free lock was taken back for the request, with a new fencing token.
	var newToken int64
	mustScan(t, pool.QueryRow(ctx, `SELECT fencing_token FROM locks WHERE request_id = $1`, reqID).Scan(&newToken))
	if newToken <= token {
		t.Fatalf("token %d after relock, was %d", newToken, token)
	}
	var dispatchedAt *time.Time
	mustScan(t, pool.QueryRow(ctx, `SELECT dispatch_state, dispatched_at FROM apply_failures WHERE request_id = $1`, reqID).Scan(&state, &dispatchedAt))
	if state != DispatchDispatched || dispatchedAt == nil {
		t.Fatalf("state %s at %v", state, dispatchedAt)
	}

	// The second failure (another run) schedules retry 2; the third exhausts the budget.
	for i, want := range []string{DecisionRetryScheduled, DecisionBudgetExhausted} {
		f := failure
		f.Run.RunID = int64(9002 + i)
		f.FencingToken = &newToken // the retried run reports with the lock it claimed
		got, err := retrier.OnApplyFailure(ctx, f)
		if err != nil || got.Decision != want {
			t.Fatalf("failure %d: %+v, %v", i+2, got, err)
		}
	}
	mustScan(t, pool.QueryRow(ctx, `SELECT retry_count FROM requests WHERE id = $1`, reqID).Scan(&retryCount))
	if retryCount != 2 {
		t.Fatalf("retry_count %d after the budget, want 2", retryCount)
	}

	var actions []string
	rows, err := pool.Query(ctx, `SELECT action FROM audit_events WHERE subject_id = $1 ORDER BY seq`, reqID.String())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		mustScan(t, rows.Scan(&a))
		actions = append(actions, a)
	}
	want := []string{
		"request.apply_failure_classified", "request.transitioned", "request.apply_retry_scheduled", "request.apply_retry_dispatched",
		"request.apply_failure_classified", "request.transitioned", "request.apply_retry_scheduled",
		"request.apply_failure_classified",
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audit\n got %v\nwant %v", actions, want)
	}
}

func TestPostgresDispatchFailureIsRecordedAndFinal(t *testing.T) {
	pool, reqID, _, token := seedApplying(t)
	ctx := context.Background()
	repo := lifecycleRepo(pool, audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{}))
	clock := platform.FixedClock{T: time.Now().UTC().Truncate(time.Microsecond)}
	f := FailedApply{RequestShortID: "req-7k3p9x", StackPath: itStackPath, FencingToken: &token, Run: RunRef{RunID: 1, Attempt: 1},
		Stage: "apply", Message: "dial tcp: i/o timeout"}
	if _, err := NewRetrier(repo, clock, platform.RandomIDs{}).OnApplyFailure(ctx, f); err != nil {
		t.Fatal(err)
	}
	var payload json.RawMessage
	mustScan(t, pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE topic = $1`, TopicApplyRetry).Scan(&payload))
	pub := &fakePublisher{err: errors.New("github is down")}
	err := NewDispatcher(repo, pub, locks.NewService(locks.NewPostgresRepository(pool, audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})),
		platform.SystemClock{}, time.Hour), clock, "main").Handle(ctx, outbox.Job{ID: 1, Payload: payload, Attempts: 1, MaxAttempts: 6})
	if !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("err %v", err)
	}
	var state, detail string
	mustScan(t, pool.QueryRow(ctx, `SELECT dispatch_state, dispatch_detail FROM apply_failures WHERE request_id = $1`, reqID).Scan(&state, &detail))
	if state != DispatchFailed || !strings.Contains(detail, "github is down") {
		t.Fatalf("state %s detail %q", state, detail)
	}
}

func TestPostgresRetryRefusedWhenAnotherRequestHoldsTheLock(t *testing.T) {
	pool, reqID, stackID, token := seedApplying(t)
	ctx := context.Background()
	// The lock moves to another request (released, then granted to a newer one).
	var other uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
		SELECT 'req-other01', $1, 'update', '{}', created_by FROM requests WHERE id = $2 RETURNING id`, stackID, reqID).Scan(&other))
	if _, err := pool.Exec(ctx, `UPDATE locks SET request_id = $1, holder = 'request:req-other01' WHERE stack_id = $2`, other, stackID); err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{}))
	out, err := NewRetrier(repo, platform.SystemClock{}, platform.RandomIDs{}).OnApplyFailure(ctx, FailedApply{
		RequestShortID: "req-7k3p9x", StackPath: itStackPath, FencingToken: &token, Run: RunRef{RunID: 3, Attempt: 1},
		Stage: "apply", Message: "ThrottlingException"})
	if err != nil || out.Decision != DecisionLockLost {
		t.Fatalf("%+v, %v", out, err)
	}
	var jobs int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = $1`, TopicApplyRetry).Scan(&jobs))
	if jobs != 0 {
		t.Fatalf("%d retry jobs", jobs)
	}
}
