package requests

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

func TestTransition(t *testing.T) {
	tests := []struct {
		from    Status
		event   Event
		want    Status
		wantErr bool
	}{
		{StatusDraft, EventPROpened, StatusPROpen, false},
		{StatusDraft, EventCancel, StatusCancelled, false},
		{StatusPROpen, EventValidationStarted, StatusValidating, false},
		{StatusPROpen, EventPRClosed, StatusRejected, false},
		{StatusValidating, EventChecksPassed, StatusPlanReady, false},
		{StatusValidating, EventChecksFailed, StatusFailed, false},
		{StatusPlanReady, EventApproved, StatusApproved, false},
		{StatusPlanReady, EventNewCommit, StatusValidating, false},
		{StatusPlanReady, EventPRClosed, StatusRejected, false},
		{StatusApproved, EventNewCommit, StatusValidating, false},
		{StatusApproved, EventApplyStarted, StatusApplying, false},
		{StatusApplying, EventApplySucceeded, StatusApplied, false},
		{StatusApplying, EventApplyFailed, StatusFailed, false},
		{StatusApplying, EventApplyRetry, StatusApplying, false},
		{StatusApproved, EventReviewDismissed, StatusPlanReady, false},
		{StatusPlanReady, EventReviewDismissed, StatusPlanReady, true},
		// invalid
		{StatusDraft, EventApplyStarted, StatusDraft, true},
		{StatusApplying, EventCancel, StatusApplying, true},
		{StatusPROpen, EventApproved, StatusPROpen, true},
		{StatusValidating, EventApproved, StatusValidating, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"/"+string(tt.event), func(t *testing.T) {
			got, err := Transition(tt.from, tt.event)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
			if err != nil {
				if e, ok := apperr.As(err); !ok || e.Code != "invalid_transition" || e.Kind != apperr.KindConflict {
					t.Fatalf("unexpected error type %v", err)
				}
			}
		})
	}
}

func TestTerminalStatusesAcceptNoEvents(t *testing.T) {
	events := []Event{EventReviewDismissed, EventPROpened, EventValidationStarted, EventChecksPassed, EventChecksFailed, EventApproved,
		EventNewCommit, EventPRClosed, EventCancel, EventApplyStarted, EventApplySucceeded, EventApplyFailed, EventApplyRetry}
	for _, s := range AllStatuses {
		if !s.IsTerminal() {
			if len(AllowedEvents(s)) == 0 {
				t.Errorf("non-terminal status %s has no outgoing events (dead end)", s)
			}
			continue
		}
		for _, e := range events {
			if _, err := Transition(s, e); err == nil {
				t.Errorf("terminal status %s accepted %s", s, e)
			}
		}
	}
}

func TestEveryStatusIsReachable(t *testing.T) {
	reached := map[Status]bool{StatusDraft: true}
	for to := range maps(transitions) {
		reached[to] = true
	}
	for _, s := range AllStatuses {
		if !reached[s] {
			t.Errorf("status %s is unreachable", s)
		}
	}
}

func maps(m map[edge]Status) map[Status]struct{} {
	out := map[Status]struct{}{}
	for _, v := range m {
		out[v] = struct{}{}
	}
	return out
}

func TestAllowedEventsSorted(t *testing.T) {
	got := AllowedEvents(StatusPlanReady)
	want := []Event{EventApproved, EventCancel, EventNewCommit, EventPRClosed}
	if !slices.Equal(got, want) {
		t.Fatalf("AllowedEvents = %v, want %v", got, want)
	}
}

func TestRequestLifecycle(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	r := NewDraft(uuid.New(), "req-abc123", StackRef{}, KindCreate, nil, "", uuid.New(), t0)
	hash := strings.Repeat("ab", 32)
	approver := uuid.New()

	steps := []func() error{
		func() error { return r.Fire(EventPROpened, t0.Add(1*time.Second)) },
		func() error { return r.Fire(EventValidationStarted, t0.Add(2*time.Second)) },
		func() error { return r.Fire(EventChecksPassed, t0.Add(3*time.Second)) },
		func() error { return r.Approve(hash, approver, t0.Add(4*time.Second)) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if r.Status != StatusApproved || r.ApprovedPlanHash != hash || *r.ApprovedBy != approver {
		t.Fatalf("not approved: %+v", r)
	}

	// A new commit clears the pinned plan.
	if err := r.Fire(EventNewCommit, t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusValidating || r.ApprovedPlanHash != "" || r.ApprovedBy != nil {
		t.Fatalf("approval not cleared: %+v", r)
	}

	if err := r.Fire(EventChecksFailed, t0.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if r.CompletedAt == nil || !r.CompletedAt.Equal(t0.Add(6*time.Second)) {
		t.Fatalf("completed_at not set for terminal status: %+v", r.CompletedAt)
	}
}

func TestApproveGuards(t *testing.T) {
	r := NewDraft(uuid.New(), "req-abc123", StackRef{}, KindCreate, nil, "", uuid.New(), time.Now())
	if err := r.Fire(EventApproved, time.Now()); err == nil {
		t.Fatal("Fire(EventApproved) must be rejected; use Approve")
	}
	r.Status = StatusPlanReady
	if err := r.Approve("not-a-hash", uuid.New(), time.Now()); err == nil {
		t.Fatal("invalid plan hash accepted")
	}
	if r.Status != StatusPlanReady {
		t.Fatalf("status changed on failed approve: %s", r.Status)
	}
}

func TestQueuedRequestCannotOpenPR(t *testing.T) {
	holder := uuid.New()
	tests := []struct {
		name   string
		behind *uuid.UUID
		event  Event
		want   Status
		code   string
	}{
		{name: "holder opens its PR", event: EventPROpened, want: StatusPROpen},
		{name: "queued request stays draft", behind: &holder, event: EventPROpened, want: StatusDraft, code: "stack_locked"},
		{name: "queued request can still be cancelled", behind: &holder, event: EventCancel, want: StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewDraft(uuid.New(), "req-abc123", StackRef{}, KindUpdate, nil, "", uuid.New(), t0)
			r.QueuedBehind = tt.behind
			err := r.Fire(tt.event, t0.Add(time.Second))
			if r.Status != tt.want || r.IsQueued() != (tt.behind != nil) {
				t.Fatalf("status = %s, queued = %v", r.Status, r.IsQueued())
			}
			if tt.code == "" && err != nil {
				t.Fatal(err)
			}
			if tt.code != "" {
				if e, ok := apperr.As(err); !ok || e.Code != tt.code || e.Kind != apperr.KindConflict {
					t.Fatalf("want %s conflict, got %v", tt.code, err)
				}
			}
		})
	}
}

func TestNextCapsApplyRetries(t *testing.T) {
	s := Snapshot{Status: StatusApplying}
	for i := 1; i <= MaxApplyRetries; i++ {
		var err error
		if s, err = Next(s, EventApplyRetry); err != nil || s.RetryCount != i || s.Status != StatusApplying {
			t.Fatalf("retry %d: %+v, %v", i, s, err)
		}
	}
	if _, err := Next(s, EventApplyRetry); err == nil {
		t.Fatalf("retry %d must exceed the budget of %d", MaxApplyRetries+1, MaxApplyRetries)
	}
	// After the budget, the only ways out are success or failure.
	if got, err := Next(s, EventApplyFailed); err != nil || got.Status != StatusFailed || got.RetryCount != MaxApplyRetries {
		t.Fatalf("fail after retries = %+v, %v", got, err)
	}
}

func TestReviewDismissedClearsApproval(t *testing.T) {
	r := NewDraft(uuid.New(), "req-abc123", StackRef{}, KindCreate, nil, "", uuid.New(), time.Now())
	r.Status = StatusPlanReady
	if err := r.Approve(strings.Repeat("cd", 32), uuid.New(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.Fire(EventReviewDismissed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusPlanReady || r.ApprovedPlanHash != "" || r.ApprovedBy != nil || r.ApprovedAt != nil {
		t.Fatalf("dismissal must return to plan_ready and clear the pinned plan: %+v", r)
	}
}

func TestRequestRetryCountTracked(t *testing.T) {
	r := NewDraft(uuid.New(), "req-abc123", StackRef{}, KindCreate, nil, "", uuid.New(), time.Now())
	r.Status = StatusApplying
	for range MaxApplyRetries {
		if err := r.Fire(EventApplyRetry, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if r.RetryCount != MaxApplyRetries || r.Fire(EventApplyRetry, time.Now()) == nil {
		t.Fatalf("entity must enforce the retry budget: %+v", r)
	}
}
