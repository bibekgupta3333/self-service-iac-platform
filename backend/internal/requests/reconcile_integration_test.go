package requests

import (
	"context"
	"testing"
)

// TestReconcileStackAgainstPostgres opens the reconcile update through the real repository:
// the recorded spec, the stack lock, the audit chain and the idempotency key.
func TestReconcileStackAgainstPostgres(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	created, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "recon-create-001"))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Request.Stack.ID
	if _, err := f.pool.Exec(ctx, `UPDATE requests SET status = 'applied', approved_plan_hash = repeat('a', 64),
		completed_at = now() WHERE id = $1`, created.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE stacks SET status = 'active', health = 'drifted' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	req, err := f.svc.ReconcileStack(ctx, f.user, id, "Reconcile drift on the stack", "drift-reconcile-abcd")
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind != KindUpdate || req.Stack.ID != id || req.FencingToken == nil || req.Stack.Status != "active" {
		t.Fatalf("reconcile request = %+v", req)
	}
	again, err := f.svc.ReconcileStack(ctx, f.user, id, "Reconcile drift on the stack", "drift-reconcile-abcd")
	if err != nil || again.ID != req.ID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if acts := f.actions(t, req.ID); len(acts) == 0 || acts[0] != "request.created" {
		t.Fatalf("audit = %v", acts)
	}
	// Another reconcile while the first holds the lock queues behind it like any update.
	next, err := f.svc.ReconcileStack(ctx, f.user, id, "again", "drift-reconcile-efgh")
	if err != nil || next.QueuedBehind == nil || *next.QueuedBehind != req.ID {
		t.Fatalf("second reconcile = %+v, %v", next, err)
	}
}
