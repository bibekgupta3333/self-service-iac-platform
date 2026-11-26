package ttl

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestBackoff(t *testing.T) {
	p := Policy{MinHours: 1, MaxHours: 72, DefaultHours: 24, BackoffHours: 24, MaxAttempts: 3}
	s := stack("development", t0.Add(-10*time.Hour))
	at := func(createdAgo, endedAgo time.Duration, outcome string) Attempt {
		return Attempt{CreatedAt: t0.Add(-createdAgo), EndedAt: t0.Add(-endedAgo), Outcome: outcome}
	}
	h := time.Hour
	tests := []struct {
		name     string
		attempts []Attempt
		policy   Policy
		want     BackoffState
	}{
		{"no attempts", nil, p, BackoffClear},
		{"failed an hour ago waits", []Attempt{at(8*h, h, "failed")}, p, BackoffWait},
		{"cancelled within the window waits", []Attempt{at(9*h, 2*h, "cancelled")}, p, BackoffWait},
		{"window passed", []Attempt{at(9*h, 30*h, "rejected")}, p, BackoffClear},
		{"attempts before the current expiry belong to an earlier cycle",
			[]Attempt{at(20*h, 19*h, "failed"), at(15*h, 14*h, "failed"), at(12*h, 11*h, "failed")}, p, BackoffClear},
		{"max attempts blocks", []Attempt{at(9*h, 50*h, "failed"), at(9*h, 40*h, "rejected"), at(9*h, 30*h, "cancelled")}, p, BackoffBlocked},
		{"below the maximum and past the window clears", []Attempt{at(9*h, 50*h, "failed"), at(9*h, 40*h, "failed")}, p, BackoffClear},
		{"backoff disabled", []Attempt{at(8*h, h, "failed")}, Policy{MaxAttempts: 3}, BackoffClear},
		{"no attempt limit", []Attempt{at(9*h, 50*h, "failed"), at(9*h, 40*h, "failed"), at(9*h, 30*h, "failed"), at(9*h, 20*h, "failed")},
			Policy{BackoffHours: 1}, BackoffClear},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := s
			s.Attempts = tt.attempts
			got, until := tt.policy.Backoff(s, t0)
			if got != tt.want {
				t.Fatalf("Backoff = %s, want %s", got, tt.want)
			}
			if got == BackoffWait && !until.After(t0) {
				t.Fatalf("until %v must be in the future", until)
			}
		})
	}
}

func TestClassifyLeavesBackoffOutButKeepsBlocked(t *testing.T) {
	p := Policy{BackoffHours: 24, MaxAttempts: 2}
	mk := func(n byte, attempts ...Attempt) Stack {
		s := stack("development", t0.Add(-10*time.Hour))
		s.ID, s.Attempts = sid(n), attempts
		return s
	}
	recent := Attempt{CreatedAt: t0.Add(-9 * time.Hour), EndedAt: t0.Add(-time.Hour), Outcome: "failed"}
	old := Attempt{CreatedAt: t0.Add(-9 * time.Hour), EndedAt: t0.Add(-40 * time.Hour), Outcome: "failed"}
	got := p.Classify([]Stack{mk(1), mk(2, recent), mk(3, old, recent)}, t0)
	ids := map[string]bool{}
	for _, d := range got {
		ids[d.Stack.ID.String()] = true
	}
	if len(got) != 2 || !ids[sid(1).String()] || !ids[sid(3).String()] || ids[sid(2).String()] {
		t.Fatalf("want the fresh and the blocked stack, not the one in backoff: %+v", got)
	}
}

// Blocked stacks and stacks in backoff are the oldest expiries and never go away by
// themselves. They must neither fill the page that bounds the actionable stacks nor be
// listed among the teardowns: fresh expired stacks behind hundreds of them are still found.
func TestDueFindsFreshTeardownsBehindManyBlockedAndBackoffStacks(t *testing.T) {
	p := Policy{MinHours: 1, MaxHours: 72, DefaultHours: 24, BackoffHours: 24, MaxAttempts: 2}
	var all []Stack
	n := 0
	add := func(expiredAgo time.Duration, endedAgo ...time.Duration) uuid.UUID {
		s := stack("development", t0.Add(-expiredAgo))
		var attempts []Attempt
		for _, e := range endedAgo { // attempts opened after the expiry, so they count in this cycle
			attempts = append(attempts, Attempt{CreatedAt: s.ExpiresAt.Add(time.Hour), EndedAt: t0.Add(-e), Outcome: "failed"})
		}
		s.ID, s.Team, s.Attempts = uuid.UUID{14: byte(n >> 8), 15: byte(n)}, "payments", attempts
		n++
		all = append(all, s)
		return s.ID
	}
	const blockedCount, backoffCount = 450, 450 // far more than a page (200) and than the run cap
	for i := 0; i < blockedCount; i++ {
		add(1000*time.Hour+time.Duration(i)*time.Minute, 50*time.Hour, 40*time.Hour)
	}
	for i := 0; i < backoffCount; i++ {
		add(900*time.Hour+time.Duration(i)*time.Minute, time.Hour)
	}
	fresh := []uuid.UUID{add(3 * time.Hour), add(2 * time.Hour), add(time.Hour)}
	nearing := stack("development", t0.Add(5*time.Hour))
	nearing.ID, nearing.Team = uuid.UUID{13: 1}, "payments"
	all = append(all, nearing)

	repo := newMemRepo(all...)
	items, err := NewService(repo, members, nil, platform.FixedClock{T: t0}, p).Due(context.Background(), "corr-due")
	if err != nil {
		t.Fatal(err)
	}
	var down, warn, blocked []uuid.UUID
	for _, it := range items {
		switch it.Phase {
		case PhaseTeardown:
			down = append(down, it.Stack.ID)
		case PhaseWarn:
			warn = append(warn, it.Stack.ID)
		case PhaseBlocked:
			blocked = append(blocked, it.Stack.ID)
		}
	}
	if len(down) != 3 || !slices.Equal(down, fresh) || len(warn) != 1 {
		t.Fatalf("teardowns %v, warnings %v: the fresh stacks must all be listed", down, warn)
	}
	if len(blocked) != blockedCount {
		t.Fatalf("blocked = %d, want %d", len(blocked), blockedCount)
	}
	// Each blocked stack is audited once; a second call adds nothing.
	count := func() (c int) {
		for _, e := range repo.events {
			if e.Action == "ttl.blocked" {
				c++
			}
		}
		return c
	}
	if count() != blockedCount {
		t.Fatalf("ttl.blocked events = %d, want %d", count(), blockedCount)
	}
	if _, err := NewService(repo, members, nil, platform.FixedClock{T: t0}, p).Due(context.Background(), "corr-due"); err != nil || count() != blockedCount {
		t.Fatalf("second Due: %v, events %d", err, count())
	}
}

func TestRecordBlockedIsDecidedUnderLockOnTheReloadedStack(t *testing.T) {
	p := Policy{MinHours: 1, MaxHours: 72, DefaultHours: 24, BackoffHours: 24, MaxAttempts: 2}
	blockedStack := func(n int) Stack {
		s := stack("development", t0.Add(-100*time.Hour))
		s.ID, s.Team = sid(byte(n)), "payments"
		for i := 0; i < 2; i++ {
			s.Attempts = append(s.Attempts, Attempt{CreatedAt: s.ExpiresAt.Add(time.Hour), EndedAt: t0.Add(-50 * time.Hour), Outcome: "failed"})
		}
		return s
	}
	svcFor := func(repo *memRepo) *Service { return NewService(repo, members, nil, platform.FixedClock{T: t0}, p) }
	blockedEvents := func(repo *memRepo) (out []string) {
		for _, e := range repo.events {
			if e.Action == "ttl.blocked" {
				out = append(out, e.SubjectID+"/"+e.CorrelationID)
			}
		}
		return out
	}

	t.Run("uses the request's correlation id", func(t *testing.T) {
		s := blockedStack(1)
		repo := newMemRepo(s)
		if _, err := svcFor(repo).Due(context.Background(), "corr-run-7"); err != nil {
			t.Fatal(err)
		}
		if got := blockedEvents(repo); !slices.Equal(got, []string{s.ID.String() + "/corr-run-7"}) {
			t.Fatalf("events = %v", got)
		}
	})
	t.Run("a stack extended since the candidate read is not recorded", func(t *testing.T) {
		stale := blockedStack(2) // what the candidate read saw
		fresh := stale           // what is stored now: extended by a day, so no longer expired
		later := t0.Add(24 * time.Hour)
		fresh.ExpiresAt = &later
		repo := newMemRepo(fresh)
		repo.candidateView = map[uuid.UUID]Stack{stale.ID: stale}
		if _, err := svcFor(repo).Due(context.Background(), "c"); err != nil {
			t.Fatal(err)
		}
		if got := blockedEvents(repo); len(got) != 0 {
			t.Fatalf("ttl.blocked recorded for an expiry that no longer applies: %v", got)
		}
	})
	t.Run("a failing audit append does not fail Due or hide the other stacks", func(t *testing.T) {
		down := stack("development", t0.Add(-time.Hour))
		down.ID, down.Team = sid(9), "payments"
		repo := newMemRepo(blockedStack(3), down)
		repo.auditErr = errors.New("audit store down")
		items, err := svcFor(repo).Due(context.Background(), "c")
		if err != nil {
			t.Fatalf("Due must survive one bad audit append: %v", err)
		}
		phases := map[Phase]int{}
		for _, it := range items {
			phases[it.Phase]++
		}
		if phases[PhaseBlocked] != 1 || phases[PhaseTeardown] != 1 {
			t.Fatalf("phases = %v", phases)
		}
		if len(blockedEvents(repo)) != 0 {
			t.Fatal("nothing was appended")
		}
	})
}

func TestExtensionClearsTheWarningOnlyBeyondTheWindow(t *testing.T) {
	p := Policy{MinHours: 1, MaxHours: 72, DefaultHours: 24, MaxExtensionHours: 100}
	s := stack("development", t0.Add(5*time.Hour))
	short, err := p.Extend(s, 10, t0) // 15 h from now: still inside the window
	if err != nil || short.ClearWarning {
		t.Fatalf("short extension = %+v, %v", short, err)
	}
	long, err := p.Extend(s, 48, t0)
	if err != nil || !long.ClearWarning {
		t.Fatalf("long extension = %+v, %v", long, err)
	}
}

func TestPolicyCeilingAndBackoffValidation(t *testing.T) {
	if (Policy{MinHours: 1, MaxHours: MaxTTLCeilingHours + 1, DefaultHours: 1}).Validate() == nil {
		t.Error("a maximum above the ceiling must be invalid")
	}
	if (Policy{MinHours: 1, MaxHours: 10, DefaultHours: 1, BackoffHours: -1}).Validate() == nil {
		t.Error("a negative backoff must be invalid")
	}
}
