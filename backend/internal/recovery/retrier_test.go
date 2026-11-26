package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// memStore is an in-memory RetryStore. InTx works on a copy and keeps it only when fn
// succeeds, so a failed transaction leaves no trace, like Postgres.
type memStore struct {
	target   Target
	lock     *locks.Lock
	failures map[string]FailureRecord // key: run id/attempt
	retries  map[int]string           // retry number -> dispatch state
	jobs     []outbox.Message
	events   []audit.Event
}

func newMem(t Target) *memStore {
	return &memStore{target: t, failures: map[string]FailureRecord{}, retries: map[int]string{}}
}

func (m *memStore) clone() *memStore {
	c := *m
	c.failures, c.retries = map[string]FailureRecord{}, map[int]string{}
	for k, v := range m.failures {
		c.failures[k] = v
	}
	for k, v := range m.retries {
		c.retries[k] = v
	}
	c.jobs, c.events = append([]outbox.Message(nil), m.jobs...), append([]audit.Event(nil), m.events...)
	return &c
}

type memRepo struct{ s *memStore }

func (r *memRepo) InTx(_ context.Context, fn func(RetryStore) error) error {
	c := r.s.clone()
	if err := fn(c); err != nil {
		return err
	}
	r.s = c
	return nil
}

func (m *memStore) TargetByShortID(_ context.Context, shortID string) (Target, error) {
	if shortID != m.target.ShortID {
		return Target{}, apperr.NotFound("request %s not found", shortID)
	}
	return m.target, nil
}

func (m *memStore) TargetByID(_ context.Context, id uuid.UUID) (Target, error) {
	if id != m.target.ID {
		return Target{}, apperr.NotFound("request %s not found", id)
	}
	return m.target, nil
}

func (m *memStore) CurrentLock(context.Context, uuid.UUID) (*locks.Lock, error) { return m.lock, nil }

func runKey(r RunRef) string { return fmt.Sprintf("%d/%d", r.RunID, r.Attempt) }

func (m *memStore) InsertFailure(_ context.Context, f FailureRecord) (bool, error) {
	if _, ok := m.failures[runKey(f.Run)]; ok {
		return false, nil
	}
	m.failures[runKey(f.Run)] = f
	if f.RetryNumber > 0 {
		m.retries[f.RetryNumber] = DispatchScheduled
	}
	return true, nil
}

func (m *memStore) IncrementRetry(_ context.Context, _ uuid.UUID, from int, _ time.Time) error {
	if m.target.Status != requests.StatusApplying || m.target.RetryCount != from {
		return apperr.Conflict("concurrent_update", "moved on")
	}
	m.target.RetryCount++
	return nil
}

func (m *memStore) ClaimDispatch(_ context.Context, _ uuid.UUID, n int, _ time.Time) (bool, error) {
	if m.retries[n] != DispatchScheduled {
		return false, nil
	}
	m.retries[n] = DispatchDispatching
	return true, nil
}

func (m *memStore) FinishDispatch(_ context.Context, _ uuid.UUID, n int, state, _ string, _ time.Time) error {
	if m.retries[n] != DispatchDispatching {
		return apperr.Conflict("concurrent_update", "not dispatching")
	}
	m.retries[n] = state
	return nil
}

func (m *memStore) Enqueue(_ context.Context, msg outbox.Message) (bool, error) {
	m.jobs = append(m.jobs, msg)
	return true, nil
}

func (m *memStore) AppendAudit(_ context.Context, e audit.Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) actions() []string {
	out := make([]string, 0, len(m.events))
	for _, e := range m.events {
		out = append(out, e.Action)
	}
	return out
}

var now = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

func applyingTarget() Target {
	tok := int64(7)
	return Target{ID: uuid.New(), ShortID: "req-7k3p9x", Kind: requests.KindUpdate, Status: requests.StatusApplying,
		FencingToken: &tok, ApprovedPlanHash: strings.Repeat("b", 64), StackID: uuid.New(),
		StackPath: "environments/development/payments/orders", TeamID: uuid.New()}
}

func throttled(t Target, runID int64) FailedApply {
	tok := int64(7)
	return FailedApply{RequestShortID: t.ShortID, StackPath: t.StackPath, FencingToken: &tok, Run: RunRef{RunID: runID, Attempt: 1},
		Stage: "apply", ExitCode: ptr(1), Message: "api error ThrottlingException: Rate exceeded"}
}

func TestRetrierSchedulesATransientRetry(t *testing.T) {
	tgt := applyingTarget()
	repo := &memRepo{s: newMem(tgt)}
	repo.s.lock = &locks.Lock{RequestID: tgt.ID, FencingToken: 7}
	r := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{})

	out, err := r.OnApplyFailure(context.Background(), throttled(tgt, 100))
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != DecisionRetryScheduled || out.RetryNumber != 1 || !out.RetryAt.Equal(now.Add(2*time.Minute)) || out.Class != ClassTransient {
		t.Fatalf("outcome %+v", out)
	}
	if repo.s.target.RetryCount != 1 {
		t.Fatalf("retry_count = %d", repo.s.target.RetryCount)
	}
	want := []string{"request.apply_failure_classified", "request.transitioned", "request.apply_retry_scheduled"}
	if got := repo.s.actions(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	var tr map[string]any
	_ = json.Unmarshal(repo.s.events[1].Payload, &tr)
	if tr["event"] != "apply_retry" || tr["from"] != "applying" || tr["to"] != "applying" {
		t.Fatalf("transition payload %v", tr)
	}
	if len(repo.s.jobs) != 1 || repo.s.jobs[0].Topic != TopicApplyRetry || !repo.s.jobs[0].AvailableAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("jobs %+v", repo.s.jobs)
	}
	for _, e := range repo.s.events {
		if e.ActorType != audit.ActorSystem || e.ActorID != "recovery" || e.SubjectID != tgt.ID.String() || *e.TeamID != tgt.TeamID {
			t.Fatalf("audit event %+v", e)
		}
	}

	// The same run reported again (a replay, or a later failed stage) changes nothing.
	again, err := r.OnApplyFailure(context.Background(), throttled(tgt, 100))
	if err != nil || !again.Duplicate {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if repo.s.target.RetryCount != 1 || len(repo.s.jobs) != 1 || len(repo.s.events) != 3 {
		t.Fatal("a replayed failure must not schedule another retry")
	}
}

func TestRetrierStopsAtTheRetryBudget(t *testing.T) {
	tgt := applyingTarget()
	repo := &memRepo{s: newMem(tgt)}
	r := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{})
	ctx := context.Background()
	for i := int64(1); i <= 3; i++ {
		out, err := r.OnApplyFailure(ctx, throttled(tgt, 100+i))
		if err != nil {
			t.Fatal(err)
		}
		want := DecisionRetryScheduled
		if i == 3 {
			want = DecisionBudgetExhausted
		}
		if out.Decision != want {
			t.Fatalf("failure %d: decision %s, want %s", i, out.Decision, want)
		}
	}
	if repo.s.target.RetryCount != requests.MaxApplyRetries || len(repo.s.jobs) != requests.MaxApplyRetries {
		t.Fatalf("retry_count %d, jobs %d", repo.s.target.RetryCount, len(repo.s.jobs))
	}
	if !repo.s.jobs[1].AvailableAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("second retry at %s", repo.s.jobs[1].AvailableAt)
	}
}

func TestRetrierRecordsWithoutRetrying(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*memStore, *FailedApply)
		decision string
	}{
		{"config error", func(_ *memStore, f *FailedApply) {
			f.Message = "Error: Invalid value for variable"
		}, DecisionNotTransient},
		{"partial apply", func(_ *memStore, f *FailedApply) { f.Message = "OperationAbortedException" }, DecisionNotTransient},
		{"smoke", func(_ *memStore, f *FailedApply) { f.Stage = "smoke" }, DecisionStageNotRetryable},
		{"lock taken by another request", func(m *memStore, _ *FailedApply) {
			m.lock = &locks.Lock{RequestID: uuid.New(), FencingToken: 9}
		}, DecisionLockLost},
		{"stale token", func(m *memStore, f *FailedApply) {
			m.lock = &locks.Lock{RequestID: m.target.ID, FencingToken: 9}
		}, DecisionLockLost},
		{"request already failed", func(m *memStore, _ *FailedApply) { m.target.Status = requests.StatusFailed }, DecisionRequestNotApplying},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt := applyingTarget()
			repo := &memRepo{s: newMem(tgt)}
			f := throttled(tgt, 5)
			tc.mutate(repo.s, &f)
			out, err := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{}).OnApplyFailure(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			if out.Decision != tc.decision || out.RetryNumber != 0 || out.RetryAt != nil {
				t.Fatalf("outcome %+v", out)
			}
			if len(repo.s.jobs) != 0 || repo.s.target.RetryCount != 0 {
				t.Fatal("no retry may be scheduled")
			}
			if got := repo.s.actions(); len(got) != 1 || got[0] != "request.apply_failure_classified" {
				t.Fatalf("audit %v", got)
			}
		})
	}
}

func TestRetrierWithRetriesDisabledOnlyRecords(t *testing.T) {
	tgt := applyingTarget()
	repo := &memRepo{s: newMem(tgt)}
	out, err := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{}).DisableRetries().
		OnApplyFailure(context.Background(), throttled(tgt, 1))
	if err != nil || out.Decision != DecisionRetriesDisabled || out.Class != ClassTransient {
		t.Fatalf("%+v, %v", out, err)
	}
	if len(repo.s.jobs) != 0 || repo.s.target.RetryCount != 0 || len(repo.s.events) != 1 {
		t.Fatal("no retry without a dispatcher")
	}
}

func TestRetrierIgnoresUnknownRequestsAndOtherStacks(t *testing.T) {
	tgt := applyingTarget()
	repo := &memRepo{s: newMem(tgt)}
	r := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{})
	f := throttled(tgt, 1)
	f.RequestShortID = "req-unknown1"
	if out, err := r.OnApplyFailure(context.Background(), f); err != nil || out.Ignored != "unknown_request" {
		t.Fatalf("unknown: %+v, %v", out, err)
	}
	f = throttled(tgt, 1)
	f.StackPath = "environments/development/payments/other"
	if out, err := r.OnApplyFailure(context.Background(), f); err != nil || out.Ignored != "stack_mismatch" {
		t.Fatalf("mismatch: %+v, %v", out, err)
	}
	if len(repo.s.events) != 0 {
		t.Fatal("ignored reports are not audited")
	}
}

// fakePublisher records dispatches.
type fakePublisher struct {
	dispatches []publish.Dispatch
	err        error
}

func (p *fakePublisher) Name() string { return "fake" }
func (p *fakePublisher) CommitFiles(context.Context, publish.Commit) (publish.CommitRef, error) {
	return publish.CommitRef{}, errors.New("not used")
}
func (p *fakePublisher) OpenPullRequest(context.Context, publish.PullRequestSpec) (publish.PullRequest, error) {
	return publish.PullRequest{}, errors.New("not used")
}
func (p *fakePublisher) AddLabels(context.Context, int, []string) error {
	return errors.New("not used")
}
func (p *fakePublisher) DispatchWorkflow(_ context.Context, d publish.Dispatch) error {
	p.dispatches = append(p.dispatches, d)
	return p.err
}
func (p *fakePublisher) ClosePullRequest(context.Context, int) error { return errors.New("not used") }
func (p *fakePublisher) DeleteBranch(context.Context, string) error  { return errors.New("not used") }
func (p *fakePublisher) PullRequestHead(context.Context, int) (string, error) {
	return "", errors.New("not used")
}

func scheduled(t *testing.T) (*memRepo, Target, outbox.Job) {
	t.Helper()
	tgt := applyingTarget()
	repo := &memRepo{s: newMem(tgt)}
	if _, err := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{}).OnApplyFailure(context.Background(), throttled(tgt, 1)); err != nil {
		t.Fatal(err)
	}
	m := repo.s.jobs[0]
	return repo, tgt, outbox.Job{ID: 41, Topic: m.Topic, AggregateID: m.AggregateID, Payload: m.Payload, Attempts: 1, MaxAttempts: 6}
}

func TestDispatcherDispatchesOnce(t *testing.T) {
	repo, tgt, job := scheduled(t)
	pub := &fakePublisher{}
	d := NewDispatcher(repo, pub, platform.FixedClock{T: now.Add(2 * time.Minute)}, "main")
	if err := d.Handle(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(pub.dispatches) != 1 {
		t.Fatalf("dispatches %d", len(pub.dispatches))
	}
	got := pub.dispatches[0]
	if got.Workflow != "apply.yml" || got.Ref != "main" || got.Inputs["request_id"] != tgt.ID.String() ||
		got.Inputs["stack_path"] != tgt.StackPath || got.Inputs["approved_plan_hash"] != tgt.ApprovedPlanHash {
		t.Fatalf("dispatch %+v", got)
	}
	if repo.s.retries[1] != DispatchDispatched {
		t.Fatalf("state %s", repo.s.retries[1])
	}
	if a := repo.s.actions(); a[len(a)-1] != "request.apply_retry_dispatched" {
		t.Fatalf("audit %v", a)
	}
	// The job running again (a lost completion, a crashed worker) dispatches nothing.
	if err := d.Handle(context.Background(), job); err != nil || len(pub.dispatches) != 1 {
		t.Fatalf("second run: %v, %d dispatches", err, len(pub.dispatches))
	}
}

func TestDispatcherNeverRetriesAFailedDispatch(t *testing.T) {
	repo, _, job := scheduled(t)
	pub := &fakePublisher{err: errors.New("github: 502 bad gateway")}
	d := NewDispatcher(repo, pub, platform.FixedClock{T: now}, "main")
	err := d.Handle(context.Background(), job)
	if !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("a failed dispatch must end the job: %v", err)
	}
	if repo.s.retries[1] != DispatchFailed {
		t.Fatalf("state %s", repo.s.retries[1])
	}
	if a := repo.s.actions(); a[len(a)-1] != "request.apply_retry_dispatch_failed" {
		t.Fatalf("audit %v", a)
	}
	if err := d.Handle(context.Background(), job); err != nil || len(pub.dispatches) != 1 {
		t.Fatalf("a failed dispatch is never sent again: %v, %d", err, len(pub.dispatches))
	}
}

func TestDispatcherSkipsWhenTheRequestMovedOn(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*memStore)
		reason string
	}{
		{"request failed", func(m *memStore) { m.target.Status = requests.StatusFailed }, DecisionRequestNotApplying},
		{"lock taken", func(m *memStore) { m.lock = &locks.Lock{RequestID: uuid.New()} }, DecisionLockLost},
		{"superseded", func(m *memStore) { m.target.RetryCount = 2 }, "superseded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, job := scheduled(t)
			tc.mutate(repo.s)
			pub := &fakePublisher{}
			if err := NewDispatcher(repo, pub, platform.FixedClock{T: now}, "").Handle(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if len(pub.dispatches) != 0 || repo.s.retries[1] != DispatchSkipped {
				t.Fatalf("dispatches %d, state %s", len(pub.dispatches), repo.s.retries[1])
			}
			last := repo.s.events[len(repo.s.events)-1]
			var p map[string]any
			_ = json.Unmarshal(last.Payload, &p)
			if last.Action != "request.apply_retry_skipped" || p["reason"] != tc.reason {
				t.Fatalf("audit %s %v", last.Action, p)
			}
		})
	}
}

func TestDispatcherRejectsBadJobs(t *testing.T) {
	d := NewDispatcher(&memRepo{s: newMem(applyingTarget())}, &fakePublisher{}, platform.FixedClock{T: now}, "main")
	for _, payload := range []string{`{`, `{"request_id":"00000000-0000-0000-0000-000000000000","retry_number":1}`,
		`{"request_id":"` + uuid.NewString() + `","retry_number":0}`} {
		if err := d.Handle(context.Background(), outbox.Job{ID: 1, Payload: json.RawMessage(payload)}); !errors.Is(err, outbox.ErrPermanent) {
			t.Errorf("payload %s: %v", payload, err)
		}
	}
	// A request that no longer exists cannot be retried either.
	job := outbox.Job{ID: 2, Payload: json.RawMessage(`{"request_id":"` + uuid.NewString() + `","retry_number":1}`)}
	if err := d.Handle(context.Background(), job); !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("unknown request: %v", err)
	}
}
