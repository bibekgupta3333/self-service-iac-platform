package requests

import (
	"context"
	"strings"
	"testing"
)

func TestApplyTargetByPath(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	res, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "lookup-key-0001"))
	if err != nil {
		t.Fatal(err)
	}
	req := res.Request
	path := req.Stack.Path

	if _, err := f.svc.ApplyTargetByPath(ctx, "environments/development/payments/unknown", ""); !hasCode(err, "stack_not_found") {
		t.Fatalf("unknown stack: %v", err)
	}
	if _, err := f.svc.ApplyTargetByPath(ctx, path+"/", ""); !hasCode(err, "invalid_stack_path") {
		t.Fatalf("trailing slash: %v", err)
	}
	if _, err := f.svc.ApplyTargetByPath(ctx, path, "nothex"); !hasCode(err, "invalid_head_sha") {
		t.Fatalf("bad head: %v", err)
	}
	// The creator holds the lock but its change is not approved: fail closed.
	if _, err := f.svc.ApplyTargetByPath(ctx, path, ""); !hasCode(err, "request_not_approved") {
		t.Fatalf("draft holder: %v", err)
	}

	hash, merge := strings.Repeat("ab", 32), strings.Repeat("c", 40)
	if _, err := f.pool.Exec(ctx, `UPDATE requests SET status = 'approved', approved_plan_hash = $2, approved_by = created_by,
		approved_at = now(), merge_commit_sha = $3 WHERE id = $1`, req.ID, hash, merge); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.ApplyTargetByPath(ctx, path, merge)
	if err != nil {
		t.Fatal(err)
	}
	if got.StackID != req.Stack.ID || got.RequestID != req.ID || got.RequestShortID != req.ShortID || got.ApprovedPlanHash != hash ||
		got.FencingToken != *req.FencingToken || got.Status != StatusApproved || got.Kind != KindCreate {
		t.Fatalf("target = %+v", got)
	}
	if _, err := f.svc.ApplyTargetByPath(ctx, path, strings.Repeat("d", 40)); !hasCode(err, "merge_commit_mismatch") {
		t.Fatalf("other head: %v", err)
	}

	// Once the lock is gone there is nothing to apply.
	if _, err := f.pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, req.Stack.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApplyTargetByPath(ctx, path, ""); !hasCode(err, "no_active_request") {
		t.Fatalf("unlocked: %v", err)
	}
}
