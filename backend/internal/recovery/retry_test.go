package recovery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func TestBackoff(t *testing.T) {
	cases := []struct {
		n      int
		reason string
		want   time.Duration
	}{
		{0, "throttling", 2 * time.Minute},
		{1, "throttling", 2 * time.Minute},
		{2, "throttling", 10 * time.Minute},
		{3, "throttling", 30 * time.Minute}, // 50 min, capped
		{9, "timeout", 30 * time.Minute},
		{1, "eventual_consistency", time.Minute},
		{2, "eventual_consistency", 10 * time.Minute},
	}
	for _, tc := range cases {
		if got := Backoff(tc.n, tc.reason); got != tc.want {
			t.Errorf("Backoff(%d, %s) = %s, want %s", tc.n, tc.reason, got, tc.want)
		}
	}
}

func TestPlanRetry(t *testing.T) {
	transient := Classification{Class: ClassTransient, Reason: "throttling"}
	applying := func(n int) requests.Snapshot {
		return requests.Snapshot{Status: requests.StatusApplying, RetryCount: n}
	}
	cases := []struct {
		name  string
		c     Classification
		stage string
		s     requests.Snapshot
		want  RetryPlan
	}{
		{"first retry", transient, "apply", applying(0), RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: 1, Delay: 2 * time.Minute}},
		{"second retry", transient, "apply", applying(1), RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: 2, Delay: 10 * time.Minute}},
		{"budget exhausted", transient, "apply", applying(requests.MaxApplyRetries), RetryPlan{Decision: DecisionBudgetExhausted}},
		{"plan stage", transient, "plan", applying(0), RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: 1, Delay: 2 * time.Minute}},
		{"lock stage", Classification{Class: ClassTransient, Reason: "lock_contention"}, "lock", applying(0),
			RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: 1, Delay: 2 * time.Minute}},
		{"destroy", transient, "destroy", applying(0), RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: 1, Delay: 2 * time.Minute}},
		{"eventual consistency", Classification{Class: ClassTransient, Reason: "eventual_consistency"}, "apply", applying(0),
			RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: 1, Delay: time.Minute}},
		{"smoke is never retried", transient, "smoke", applying(0), RetryPlan{Decision: DecisionStageNotRetryable}},
		{"summary is never retried", transient, "summary", applying(0), RetryPlan{Decision: DecisionStageNotRetryable}},
		{"config", Classification{Class: ClassConfig, Reason: "validation"}, "apply", applying(0), RetryPlan{Decision: DecisionNotTransient}},
		{"infra", Classification{Class: ClassInfra, Reason: "partial_apply"}, "apply", applying(0), RetryPlan{Decision: DecisionNotTransient}},
		{"failed request", transient, "apply", requests.Snapshot{Status: requests.StatusFailed}, RetryPlan{Decision: DecisionRequestNotApplying}},
		{"approved request", transient, "apply", requests.Snapshot{Status: requests.StatusApproved}, RetryPlan{Decision: DecisionRequestNotApplying}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanRetry(tc.c, tc.stage, tc.s); got != tc.want {
				t.Fatalf("PlanRetry = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The retry cap is the state machine's: PlanRetry never schedules more than MaxApplyRetries.
func TestPlanRetryFollowsTheStateMachineCap(t *testing.T) {
	s := requests.Snapshot{Status: requests.StatusApplying}
	scheduled := 0
	for range 10 {
		p := PlanRetry(Classification{Class: ClassTransient, Reason: "timeout"}, "apply", s)
		if !p.Retry {
			break
		}
		scheduled++
		s.RetryCount = p.Number
	}
	if scheduled != requests.MaxApplyRetries {
		t.Fatalf("scheduled %d retries, want %d", scheduled, requests.MaxApplyRetries)
	}
}

func TestRetryMessage(t *testing.T) {
	id := uuid.MustParse("3f2c1b7a-1d2e-4f5a-8b9c-0d1e2f3a4b5c")
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	m, err := RetryMessage(id, 2, at)
	if err != nil {
		t.Fatal(err)
	}
	var p RetryPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil || p.RequestID != id || p.RetryNumber != 2 {
		t.Fatalf("payload %s: %v", m.Payload, err)
	}
	if m.Topic != TopicApplyRetry || m.DedupeKey != "apply-retry:"+id.String()+":2" || !m.AvailableAt.Equal(at) || *m.AggregateID != id {
		t.Fatalf("message %+v", m)
	}
}

func TestDispatchInputs(t *testing.T) {
	tgt := Target{ID: uuid.New(), StackID: uuid.New(), StackPath: "environments/development/payments/orders", Kind: requests.KindUpdate,
		ApprovedPlanHash: strings.Repeat("a", 64)}
	in := DispatchInputs(tgt)
	if in["stack_path"] != tgt.StackPath || in["stack_id"] != tgt.StackID.String() || in["request_id"] != tgt.ID.String() ||
		in["approved_plan_hash"] != tgt.ApprovedPlanHash || in["destroy"] != "" {
		t.Fatalf("inputs %v", in)
	}
	tgt.Kind, tgt.ApprovedPlanHash = requests.KindDestroy, "not-a-hash"
	in = DispatchInputs(tgt)
	if in["destroy"] != "true" {
		t.Fatalf("destroy input missing: %v", in)
	}
	if _, ok := in["approved_plan_hash"]; ok {
		t.Fatalf("malformed hash must be left out: %v", in)
	}
}

func TestLockAllowsRetry(t *testing.T) {
	tgt := Target{ID: uuid.New()}
	own := &locks.Lock{RequestID: tgt.ID, FencingToken: 7}
	other := &locks.Lock{RequestID: uuid.New(), FencingToken: 8}
	tok := func(v int64) *int64 { return &v }
	cases := []struct {
		name  string
		lock  *locks.Lock
		token *int64
		want  bool
	}{
		{"free", nil, tok(7), true},
		{"held, same token", own, tok(7), true},
		{"held, no token", own, nil, true},
		{"held, stale token", own, tok(6), false},
		{"another request", other, tok(7), false},
		{"another request, no token", other, nil, false},
	}
	for _, tc := range cases {
		if got := LockAllowsRetry(tc.lock, tgt, tc.token); got != tc.want {
			t.Errorf("%s: LockAllowsRetry = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSkipReason(t *testing.T) {
	tgt := Target{ID: uuid.New(), Status: requests.StatusApplying, RetryCount: 1}
	cases := []struct {
		name string
		t    Target
		n    int
		lock *locks.Lock
		want string
	}{
		{"due, lock free", tgt, 1, nil, ""},
		{"due, own lock", tgt, 1, &locks.Lock{RequestID: tgt.ID}, ""},
		{"lock taken", tgt, 1, &locks.Lock{RequestID: uuid.New()}, DecisionLockLost},
		{"superseded", tgt, 2, nil, "superseded"},
		{"request failed", Target{ID: tgt.ID, Status: requests.StatusFailed, RetryCount: 1}, 1, nil, DecisionRequestNotApplying},
	}
	for _, tc := range cases {
		if got := SkipReason(tc.t, tc.n, tc.lock); got != tc.want {
			t.Errorf("%s: SkipReason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
