package ttl

import (
	"testing"
	"time"
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
