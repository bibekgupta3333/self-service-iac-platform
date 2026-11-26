package requests

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestUpdateAndDestroyAgainstPostgres runs the existing-stack kinds through the real
// repository: the stack row lock, the open-request query, queueing and the audit chain.
func TestUpdateAndDestroyAgainstPostgres(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	created, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "kinds-create-001"))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Request.Stack.ID

	// The stack is still provisioning: mid-flight stacks take no update or destroy.
	if _, err := f.svc.Create(ctx, f.user, createCmd(stackInput(updateInput(), id), "kinds-early-0001")); !hasCode(err, "stack_busy") {
		t.Fatalf("update while provisioning: %v", err)
	}

	// The create finished (what WBS 5.3 does): the stack is active and the lock is free.
	if _, err := f.pool.Exec(ctx, `UPDATE requests SET status = 'applied', approved_plan_hash = repeat('a', 64),
		completed_at = now() WHERE id = $1`, created.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE stacks SET status = 'active' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	up, err := f.svc.Create(ctx, f.user, createCmd(stackInput(updateInput(), id), "kinds-update-001"))
	if err != nil {
		t.Fatal(err)
	}
	if up.Request.Kind != KindUpdate || up.Request.FencingToken == nil || up.Request.QueuedBehind != nil || up.Request.Stack.Status != "active" {
		t.Fatalf("update = %+v", up.Request)
	}
	if f.pendingPublishJobs(t, up.Request.ID) != 1 {
		t.Fatal("an update that holds the lock must enqueue a publish job")
	}

	// A destroy submitted meanwhile queues behind the update; nothing may follow it.
	down, err := f.svc.Create(ctx, f.user, createCmd(stackInput(destroyInput(), id), "kinds-destroy-01"))
	if err != nil {
		t.Fatal(err)
	}
	if down.Request.QueuedBehind == nil || *down.Request.QueuedBehind != up.Request.ID || down.Request.FencingToken != nil {
		t.Fatalf("destroy must queue behind the update: %+v", down.Request)
	}
	if f.pendingPublishJobs(t, down.Request.ID) != 0 {
		t.Fatal("a queued destroy must not be published yet")
	}
	if _, err := f.svc.Create(ctx, f.user, createCmd(stackInput(updateInput(), id), "kinds-late-00001")); !hasCode(err, "destroy_pending") {
		t.Fatalf("update after an open destroy: %v", err)
	}

	got, err := f.svc.Get(ctx, down.Request.ID)
	if err != nil || got.Kind != KindDestroy || got.Stack.ID != id {
		t.Fatalf("stored destroy = %+v, %v", got, err)
	}
	if acts := f.actions(t, down.Request.ID); len(acts) == 0 || acts[0] != "request.created" {
		t.Fatalf("audit = %v", acts)
	}
	if _, err := f.svc.Create(ctx, f.user, createCmd(stackInput(updateInput(), uuid.New()), "kinds-unknown-01")); !hasCode(err, "validation_failed") {
		t.Fatalf("unknown stack: %v", err)
	}
}
