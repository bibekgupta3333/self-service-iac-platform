package requests

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestReconcileInput(t *testing.T) {
	id := uuid.New()
	spec := NormalizedSpec{Team: "payments", Environment: "development", Stack: "order-events", Region: "us-east-1",
		Resources: validInput().Resources}
	in := ReconcileInput(id, spec, "Reconcile drift")
	if in.Kind != KindUpdate || in.StackID == nil || *in.StackID != id || len(in.Resources) != 2 || in.Justification != "Reconcile drift" {
		t.Fatalf("input = %+v", in)
	}
	// It is an ordinary update: the stack is named by id only, so the shape check passes.
	if errs := ValidateShape(in); len(errs) > 0 {
		t.Fatalf("a reconcile input must be a valid update: %+v", errs)
	}
}

func TestReconcileStackOpensAnUpdateOfTheRecordedSpec(t *testing.T) {
	repo, svc, created := existingStackFixture(t, StackActive)
	ctx := context.Background()

	req, err := svc.ReconcileStack(ctx, dev, created.Stack.ID, "Reconcile drift on the stack", "drift-reconcile-0001")
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind != KindUpdate || req.Stack.ID != created.Stack.ID || req.Justification != "Reconcile drift on the stack" {
		t.Fatalf("request = %+v", req)
	}
	var got, want NormalizedSpec
	if err := json.Unmarshal(req.Spec, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(created.Spec, &want); err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != len(want.Resources) || got.Resources[0].Name != want.Resources[0].Name ||
		got.Resources[1].Version != want.Resources[1].Version {
		t.Fatalf("reconcile spec = %+v, want the recorded %+v", got.Resources, want.Resources)
	}

	// A retry with the same key returns the same request.
	again, err := svc.ReconcileStack(ctx, dev, created.Stack.ID, "Reconcile drift on the stack", "drift-reconcile-0001")
	if err != nil || again.ID != req.ID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if n := len(repo.state.requests); n != 2 {
		t.Fatalf("%d requests stored, want the create plus one reconcile", n)
	}
}

func TestReconcileStackRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("destroyed stack", func(t *testing.T) {
		_, svc, created := existingStackFixture(t, StackDestroyed)
		if _, err := svc.ReconcileStack(ctx, dev, created.Stack.ID, "x", "drift-reconcile-0002"); !hasCode(err, "stack_destroyed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("stack mid-change", func(t *testing.T) {
		_, svc, created := existingStackFixture(t, StackUpdating)
		if _, err := svc.ReconcileStack(ctx, dev, created.Stack.ID, "x", "drift-reconcile-0003"); !hasCode(err, "stack_busy") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no recorded spec", func(t *testing.T) {
		repo := newMemRepo()
		svc := newSvc(repo, fakeVersions{})
		if _, err := svc.ReconcileStack(ctx, dev, uuid.New(), "x", "drift-reconcile-0004"); !hasCode(err, "no_recorded_spec") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("short key", func(t *testing.T) {
		_, svc, created := existingStackFixture(t, StackActive)
		if _, err := svc.ReconcileStack(ctx, dev, created.Stack.ID, "x", "short"); err == nil {
			t.Fatal("a key under 8 characters must be refused")
		}
	})
}
