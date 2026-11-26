package requests

import (
	"context"
	"encoding/json"
	"testing"
)

// TestReconcileStackAgainstPostgres opens the reconcile update through the real repository:
// the last applied spec, the open-request refusal, the stack lock, audit and idempotency.
func TestReconcileStackAgainstPostgres(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	created, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "recon-create-001"))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Request.Stack.ID

	// Nothing has applied yet.
	if _, err := f.svc.ReconcileStack(ctx, f.user, id, "x", "drift-reconcile-none1"); !hasCode(err, "no_recorded_spec") {
		t.Fatalf("reconcile before anything applied: %v", err)
	}
	// The create applied: it is the stack's last applied request, the lock is free.
	if _, err := f.pool.Exec(ctx, `UPDATE requests SET status = 'applied', approved_plan_hash = repeat('a', 64),
		completed_at = now() WHERE id = $1`, created.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE stacks SET status = 'active', health = 'drifted', last_applied_request_id = $2 WHERE id = $1`,
		id, created.Request.ID); err != nil {
		t.Fatal(err)
	}

	// An update with a different spec is under review: a reconcile is refused, not queued
	// behind it, and its spec cannot leak into a later reconcile.
	shrunk := stackInput(updateInput(), id)
	shrunk.Resources, shrunk.AllowRemovals = shrunk.Resources[:1], true
	upd, err := f.svc.Create(ctx, f.user, createCmd(shrunk, "recon-update-0001"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReconcileStack(ctx, f.user, id, "x", "drift-reconcile-open1"); !hasCode(err, "stack_busy") {
		t.Fatalf("reconcile while an update is open: %v", err)
	}
	// The update is rejected and over; the stack's applied request is still the create.
	if _, err := f.pool.Exec(ctx, `UPDATE requests SET status = 'rejected', completed_at = now() WHERE id = $1`, upd.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, id); err != nil {
		t.Fatal(err)
	}

	req, err := f.svc.ReconcileStack(ctx, f.user, id, "Reconcile drift on the stack", "drift-reconcile-abcd")
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind != KindUpdate || req.Stack.ID != id || req.FencingToken == nil {
		t.Fatalf("reconcile request = %+v", req)
	}
	var spec, want NormalizedSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(created.Request.Spec, &want); err != nil {
		t.Fatal(err)
	}
	if len(spec.Resources) != len(want.Resources) || len(spec.Resources) != 2 {
		t.Fatalf("reconcile spec has %d resources, the applied create had %d", len(spec.Resources), len(want.Resources))
	}
	again, err := f.svc.ReconcileStack(ctx, f.user, id, "Reconcile drift on the stack", "drift-reconcile-abcd")
	if err != nil || again.ID != req.ID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if acts := f.actions(t, req.ID); len(acts) == 0 || acts[0] != "request.created" {
		t.Fatalf("audit = %v", acts)
	}
	// A second reconcile with another key meets the open one.
	if _, err := f.svc.ReconcileStack(ctx, f.user, id, "again", "drift-reconcile-efgh"); !hasCode(err, "stack_busy") {
		t.Fatalf("second reconcile: %v", err)
	}
}
