package requests

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
)

var runRef = RunRef{RunID: 77, Attempt: 1, HeadSHA: "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4", Stage: "summary"}

func succeeded(health StackHealth, outputs string) ApplyProgress {
	return ApplyProgress{Phase: PhaseSucceeded, Run: runRef, Health: health, Outputs: json.RawMessage(outputs)}
}

func failed(reason string) ApplyProgress {
	return ApplyProgress{Phase: PhaseFailed, Run: runRef, FailureReason: reason, FailureDetail: json.RawMessage(`{"stage":"apply"}`)}
}

func TestDecideApply(t *testing.T) {
	started := ApplyProgress{Phase: PhaseStarted, Run: runRef}
	tests := []struct {
		name       string
		status     Status
		kind       Kind
		stack      StackStatus
		p          ApplyProgress
		wantEvents []Event
		wantStack  *StackChange // nil: the stack stays as it is
		wantIgnore string
		wantErr    string
	}{
		{name: "first report of an approved create starts the apply", status: StatusApproved, kind: KindCreate, stack: StackProvisioning,
			p: started, wantEvents: []Event{EventApplyStarted}},
		{name: "first report of an approved update moves the stack to updating", status: StatusApproved, kind: KindUpdate, stack: StackActive,
			p: started, wantEvents: []Event{EventApplyStarted}, wantStack: &StackChange{To: StackUpdating}},
		{name: "destroy moves the stack to destroying", status: StatusApproved, kind: KindDestroy, stack: StackFailed,
			p: started, wantEvents: []Event{EventApplyStarted}, wantStack: &StackChange{To: StackDestroying}},
		{name: "a replayed started report changes nothing", status: StatusApplying, kind: KindUpdate, stack: StackUpdating, p: started},
		{name: "a started report repairs a stack the merge webhook left alone", status: StatusApplying, kind: KindUpdate, stack: StackActive,
			p: started, wantStack: &StackChange{To: StackUpdating}},
		{name: "success of a create", status: StatusApplying, kind: KindCreate, stack: StackProvisioning,
			p: succeeded(HealthHealthy, `{"queue_url":"q"}`), wantEvents: []Event{EventApplySucceeded},
			wantStack: &StackChange{To: StackActive, Health: HealthHealthy, Outputs: json.RawMessage(`{"queue_url":"q"}`), Applied: true}},
		{name: "success without outputs stores an empty object", status: StatusApplying, kind: KindUpdate, stack: StackUpdating,
			p: succeeded("", ""), wantEvents: []Event{EventApplySucceeded},
			wantStack: &StackChange{To: StackActive, Health: HealthUnknown, Outputs: json.RawMessage(`{}`), Applied: true}},
		{name: "a degraded smoke test keeps the stack active", status: StatusApplying, kind: KindUpdate, stack: StackUpdating,
			p: succeeded(HealthDegraded, `{}`), wantEvents: []Event{EventApplySucceeded},
			wantStack: &StackChange{To: StackActive, Health: HealthDegraded, Outputs: json.RawMessage(`{}`), Applied: true}},
		{name: "a destroy that succeeded destroys the stack and clears outputs", status: StatusApplying, kind: KindDestroy, stack: StackDestroying,
			p: succeeded(HealthHealthy, `{"x":1}`), wantEvents: []Event{EventApplySucceeded},
			wantStack: &StackChange{To: StackDestroyed, Health: HealthUnknown, Outputs: json.RawMessage(`{}`), Applied: true, Destroyed: true}},
		{name: "a report that finishes an approved request starts and finishes it", status: StatusApproved, kind: KindCreate, stack: StackProvisioning,
			p: succeeded(HealthHealthy, `{}`), wantEvents: []Event{EventApplyStarted, EventApplySucceeded},
			wantStack: &StackChange{To: StackActive, Health: HealthHealthy, Outputs: json.RawMessage(`{}`), Applied: true}},
		{name: "failure fails the request and the stack", status: StatusApplying, kind: KindUpdate, stack: StackUpdating,
			p: failed(FailureApplyFailed), wantEvents: []Event{EventApplyFailed}, wantStack: &StackChange{To: StackFailed}},
		{name: "a failed create leaves a failed stack", status: StatusApplying, kind: KindCreate, stack: StackProvisioning,
			p: failed(FailurePlanFailed), wantEvents: []Event{EventApplyFailed}, wantStack: &StackChange{To: StackFailed}},
		{name: "a failed destroy leaves a failed stack", status: StatusApplying, kind: KindDestroy, stack: StackDestroying,
			p: failed(FailureApplyFailed), wantEvents: []Event{EventApplyFailed}, wantStack: &StackChange{To: StackFailed}},
		{name: "a destroyed stack is never changed", status: StatusApplying, kind: KindDestroy, stack: StackDestroyed,
			p: succeeded(HealthHealthy, `{}`), wantEvents: []Event{EventApplySucceeded}},
		{name: "a release report never moves anything", status: StatusApplying, kind: KindUpdate, stack: StackUpdating,
			p: ApplyProgress{Phase: PhaseObserved, Run: runRef, ReleaseFailure: "boom"}},
		{name: "a late success for an applied request is ignored", status: StatusApplied, kind: KindUpdate, stack: StackActive,
			p: succeeded(HealthHealthy, `{}`), wantIgnore: "request_terminal"},
		{name: "a late failure for an applied request is ignored", status: StatusApplied, kind: KindUpdate, stack: StackActive,
			p: failed(FailureApplyFailed), wantIgnore: "request_terminal"},
		{name: "a late success for a failed request is ignored", status: StatusFailed, kind: KindUpdate, stack: StackFailed,
			p: succeeded(HealthHealthy, `{}`), wantIgnore: "request_terminal"},
		{name: "a late report for a rejected request is ignored", status: StatusRejected, kind: KindCreate, stack: StackProvisioning,
			p: started, wantIgnore: "request_terminal"},
		{name: "plan_ready is not applying", status: StatusPlanReady, kind: KindCreate, stack: StackProvisioning, p: started, wantErr: "request_not_applying"},
		{name: "validating is not applying", status: StatusValidating, kind: KindUpdate, stack: StackActive, p: succeeded(HealthHealthy, `{}`), wantErr: "request_not_applying"},
		{name: "draft is not applying", status: StatusDraft, kind: KindCreate, stack: StackProvisioning, p: failed(FailureApplyFailed), wantErr: "request_not_applying"},
		{name: "pr_open is not applying", status: StatusPROpen, kind: KindCreate, stack: StackProvisioning,
			p: ApplyProgress{Phase: PhaseObserved, ReleaseFailure: "x"}, wantErr: "request_not_applying"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := DecideApply(tc.status, tc.kind, tc.stack, tc.p)
			if tc.wantErr != "" {
				if !hasCode(err, tc.wantErr) {
					t.Fatalf("err = %v, want %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(d.Events, tc.wantEvents) || d.Ignore != tc.wantIgnore {
				t.Fatalf("events %v ignore %q, want %v %q", d.Events, d.Ignore, tc.wantEvents, tc.wantIgnore)
			}
			if (d.Stack == nil) != (tc.wantStack == nil) {
				t.Fatalf("stack change = %+v, want %+v", d.Stack, tc.wantStack)
			}
			if tc.wantStack != nil {
				got, want := *d.Stack, *tc.wantStack
				if got.To != want.To || got.Health != want.Health || got.Applied != want.Applied || got.Destroyed != want.Destroyed ||
					string(got.Outputs) != string(want.Outputs) {
					t.Fatalf("stack change = %+v, want %+v", got, want)
				}
			}
			if tc.p.Phase == PhaseObserved && (len(d.Events) != 0 || d.Stack != nil) {
				t.Fatal("an observed report must not move anything")
			}
			if wantRelease := tc.p.Phase == PhaseObserved && tc.p.ReleaseFailure != ""; d.ReleaseFailed != wantRelease {
				t.Fatalf("ReleaseFailed = %v", d.ReleaseFailed)
			}
		})
	}
}

// Every event DecideApply can emit is a legal edge, and replaying a decision against the
// state it produced yields nothing.
func TestDecideApplyIsIdempotentAndLegal(t *testing.T) {
	for _, kind := range []Kind{KindCreate, KindUpdate, KindDestroy} {
		for _, p := range []ApplyProgress{
			{Phase: PhaseStarted}, succeeded(HealthHealthy, `{}`), failed(FailureApplyFailed),
		} {
			status, stack := StatusApproved, StackStatus(StackActive)
			if kind == KindCreate {
				stack = StackProvisioning
			}
			d, err := DecideApply(status, kind, stack, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range d.Events {
				next, err := Transition(status, e)
				if err != nil {
					t.Fatalf("%s/%s: event %s from %s is not an edge: %v", kind, p.Phase, e, status, err)
				}
				status = next
			}
			if d.Stack != nil {
				stack = d.Stack.To
			}
			again, err := DecideApply(status, kind, stack, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(again.Events) != 0 {
				t.Fatalf("%s/%s: replay fires %v against %s", kind, p.Phase, again.Events, status)
			}
			if p.Phase == PhaseStarted && again.Stack != nil {
				t.Fatalf("%s: a replayed started report rewrites the stack: %+v", kind, again.Stack)
			}
		}
	}
}

// applying drives a request from approved to the given phase through ApplyIn.
func applyFixture(t *testing.T, kind Kind, stack StackStatus, status Status) (*memRepo, *Service, *Request) {
	t.Helper()
	repo, svc, created := existingStackFixture(t, stack)
	var r *Request
	if kind == KindCreate {
		r = created // the create request of the fixture
		r.Status, r.CompletedAt = status, nil
	} else {
		in := stackInput(updateInput(), created.Stack.ID)
		if kind == KindDestroy {
			in = stackInput(destroyInput(), created.Stack.ID)
		}
		res, err := svc.Create(context.Background(), dev, createCmd(in, "key-apply-"+string(kind)))
		if err != nil {
			t.Fatal(err)
		}
		r = res.Request
		r.Status = status
	}
	repo.setStackStatus(r.Stack.ID, stack)
	return repo, svc, r
}

func applyReport(t *testing.T, repo *memRepo, svc *Service, id uuid.UUID, p ApplyProgress) (ApplyResult, error) {
	t.Helper()
	var res ApplyResult
	err := repo.InTx(context.Background(), func(st Store) error {
		var err error
		res, err = svc.ApplyIn(context.Background(), st, ApplyInput{RequestID: id, Progress: p, By: dev, CorrelationID: "run-77"})
		return err
	})
	return res, err
}

func actions(repo *memRepo) []string {
	var out []string
	for _, e := range repo.state.events {
		out = append(out, e.Action)
	}
	return out
}

func TestApplyInDrivesAnUpdateToApplied(t *testing.T) {
	repo, svc, r := applyFixture(t, KindUpdate, StackActive, StatusApproved)
	before := len(repo.state.events)

	res, err := applyReport(t, repo, svc, r.ID, ApplyProgress{Phase: PhaseStarted, Run: runRef})
	if err != nil || r.Status != StatusApplying || res.Status != StatusApplying || !res.Changed {
		t.Fatalf("started: %+v %v, request %s", res, err, r.Status)
	}
	if repo.state.status[r.Stack.ID] != StackUpdating {
		t.Fatalf("stack = %s", repo.state.status[r.Stack.ID])
	}

	res, err = applyReport(t, repo, svc, r.ID, ApplyProgress{Phase: PhaseStarted, Run: runRef})
	if err != nil || res.Changed {
		t.Fatalf("replayed started report changed state: %+v %v", res, err)
	}

	res, err = applyReport(t, repo, svc, r.ID, succeeded(HealthDegraded, `{"queue_url":"q"}`))
	if err != nil || r.Status != StatusApplied || r.CompletedAt == nil || res.Status != StatusApplied {
		t.Fatalf("succeeded: %+v %v, request %s", res, err, r.Status)
	}
	last := repo.state.applied[len(repo.state.applied)-1]
	if last.To != StackActive || last.Health != HealthDegraded || string(last.Outputs) != `{"queue_url":"q"}` ||
		last.CommitSHA != runRef.HeadSHA || last.RequestID != r.ID || last.Destroyed {
		t.Fatalf("stack write = %+v", last)
	}

	// A replay and a late failure of the finished request are recorded and change nothing.
	nApplied, nEvents := len(repo.state.applied), len(repo.state.events)
	for _, p := range []ApplyProgress{succeeded(HealthHealthy, `{}`), failed(FailureApplyFailed)} {
		res, err := applyReport(t, repo, svc, r.ID, p)
		if err != nil || res.Changed || res.Ignored != "request_terminal" || r.Status != StatusApplied {
			t.Fatalf("late report: %+v %v, request %s", res, err, r.Status)
		}
	}
	if len(repo.state.applied) != nApplied || repo.state.status[r.Stack.ID] != StackActive {
		t.Fatal("a late report rewrote the stack")
	}
	if len(repo.state.events) != nEvents+2 {
		t.Fatalf("late reports must be audited as ignored: %v", actions(repo)[before:])
	}
	for _, a := range actions(repo)[nEvents:] {
		if a != "request.run_report_ignored" {
			t.Fatalf("late report wrote %s", a)
		}
	}
}

func TestApplyInAuditsEveryTransition(t *testing.T) {
	repo, svc, r := applyFixture(t, KindCreate, StackProvisioning, StatusApproved)
	before := len(repo.state.events)
	if _, err := applyReport(t, repo, svc, r.ID, succeeded(HealthHealthy, `{}`)); err != nil {
		t.Fatal(err)
	}
	got := actions(repo)[before:]
	want := []string{"request.transitioned", "stack.status_changed", "request.transitioned"}
	if !slices.Equal(got, want) {
		t.Fatalf("audit actions = %v, want %v", got, want)
	}
	for _, e := range repo.state.events[before:] {
		if e.ActorType != string(dev.Type) || e.CorrelationID != "run-77" {
			t.Fatalf("audit event actor/correlation: %+v", e)
		}
	}
}

func TestApplyInFailure(t *testing.T) {
	repo, svc, r := applyFixture(t, KindUpdate, StackActive, StatusApplying)
	repo.setStackStatus(r.Stack.ID, StackUpdating)
	if _, err := applyReport(t, repo, svc, r.ID, failed(FailurePlanFailed)); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusFailed || r.FailureReason != FailurePlanFailed || string(r.FailureDetail) != `{"stage":"apply"}` || r.CompletedAt == nil {
		t.Fatalf("request = %+v", r)
	}
	if repo.state.status[r.Stack.ID] != StackFailed {
		t.Fatalf("stack = %s", repo.state.status[r.Stack.ID])
	}
}

func TestApplyInDestroyCompletes(t *testing.T) {
	repo, svc, r := applyFixture(t, KindDestroy, StackActive, StatusApproved)
	if _, err := applyReport(t, repo, svc, r.ID, ApplyProgress{Phase: PhaseStarted, Run: runRef}); err != nil {
		t.Fatal(err)
	}
	if repo.state.status[r.Stack.ID] != StackDestroying {
		t.Fatalf("stack = %s", repo.state.status[r.Stack.ID])
	}
	if _, err := applyReport(t, repo, svc, r.ID, succeeded(HealthHealthy, `{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	last := repo.state.applied[len(repo.state.applied)-1]
	if r.Status != StatusApplied || repo.state.status[r.Stack.ID] != StackDestroyed || !last.Destroyed || string(last.Outputs) != `{}` {
		t.Fatalf("request %s, stack %s, write %+v", r.Status, repo.state.status[r.Stack.ID], last)
	}
	if !slices.Contains(actions(repo), "stack.destroyed") {
		t.Fatalf("audit = %v", actions(repo))
	}
	// Nothing may target the destroyed stack any more.
	for i, in := range []CreateInput{updateInput(), destroyInput()} {
		_, err := svc.Create(context.Background(), dev, createCmd(stackInput(in, r.Stack.ID), "key-after-destroyed-"+string(rune('a'+i))))
		if !hasCode(err, "stack_destroyed") {
			t.Fatalf("%s after destroy: %v", in.Kind, err)
		}
	}
}

func TestApplyInRefusesARequestThatWasNeverApproved(t *testing.T) {
	for _, status := range []Status{StatusDraft, StatusPROpen, StatusValidating, StatusPlanReady} {
		repo, svc, r := applyFixture(t, KindUpdate, StackActive, status)
		_, err := applyReport(t, repo, svc, r.ID, ApplyProgress{Phase: PhaseStarted, Run: runRef})
		if !hasCode(err, "request_not_applying") || r.Status != status {
			t.Fatalf("%s: err %v, status %s", status, err, r.Status)
		}
	}
}
