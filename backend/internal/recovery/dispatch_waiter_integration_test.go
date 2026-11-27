package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
)

// hookPublisher runs onDispatch (while GitHub is being called) and then fails the dispatch.
type hookPublisher struct {
	fakePublisher
	onDispatch func()
}

func (p *hookPublisher) DispatchWorkflow(ctx context.Context, d publish.Dispatch) error {
	p.onDispatch()
	_ = p.fakePublisher.DispatchWorkflow(ctx, d)
	return errors.New("github: 502 bad gateway")
}

// A dispatch fails after the stack lock went to a waiting request (an admin release and
// promotion during the GitHub call). Only the original request fails: the stack is left to
// the waiter, which holds the lock and may be applying, and its lock is untouched.
func TestPostgresDispatchFailureWhileAWaiterHoldsTheLock(t *testing.T) {
	pool, reqID, stackID, token := seedApplying(t)
	ctx := context.Background()
	w := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	repo := lifecycleRepo(pool, w)
	clock := platform.FixedClock{T: time.Now().UTC().Truncate(time.Microsecond)}
	if _, err := NewRetrier(repo, clock, platform.RandomIDs{}).OnApplyFailure(ctx, FailedApply{RequestShortID: "req-7k3p9x",
		StackPath: itStackPath, FencingToken: &token, Run: RunRef{RunID: 1, Attempt: 1}, Stage: "apply",
		Message: "Error: creating SQS Queue (q) [api error ThrottlingException]"}); err != nil {
		t.Fatal(err)
	}
	var payload json.RawMessage
	mustScan(t, pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE topic = $1`, TopicApplyRetry).Scan(&payload))

	var waiter uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
		SELECT 'req-waiter02', $1, 'update', '{}', created_by FROM requests WHERE id = $2 RETURNING id`, stackID, reqID).Scan(&waiter))
	pub := &hookPublisher{onDispatch: func() {
		if _, err := pool.Exec(ctx, `UPDATE locks SET request_id = $1, holder = 'request:req-waiter02' WHERE stack_id = $2`,
			waiter, stackID); err != nil {
			t.Error(err)
		}
	}}
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, w), platform.SystemClock{}, time.Hour)
	err := NewDispatcher(repo, pub, lockSvc, clock, "main").Handle(ctx, outbox.Job{ID: 3, Payload: payload, Attempts: 1, MaxAttempts: 6})
	if !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("err %v", err)
	}

	var status, reason, stackStatus string
	var holder uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT r.status::text, coalesce(r.failure_reason, ''), s.status::text, l.request_id
		FROM requests r JOIN stacks s ON s.id = r.stack_id JOIN locks l ON l.stack_id = s.id WHERE r.id = $1`, reqID).
		Scan(&status, &reason, &stackStatus, &holder))
	if status != "failed" || reason != FailureRetryNotRun {
		t.Fatalf("original request %s (%s)", status, reason)
	}
	if stackStatus != "updating" || holder != waiter {
		t.Fatalf("stack %s, lock held by %s: the waiter's stack and lock must be untouched", stackStatus, holder)
	}
	var state string
	mustScan(t, pool.QueryRow(ctx, `SELECT dispatch_state FROM apply_failures WHERE request_id = $1`, reqID).Scan(&state))
	if state != DispatchFailed {
		t.Fatalf("dispatch state %s", state)
	}
}
