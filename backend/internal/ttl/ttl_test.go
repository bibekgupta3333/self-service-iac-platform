package ttl

import (
	"strings"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func stack(env string, expires time.Time) Stack {
	return Stack{ID: "s1", Environment: env, Path: "environments/" + env + "/team/app", Status: "active",
		IsEphemeral: true, ExpiresAt: &expires, CreatedAt: t0.Add(-48 * time.Hour)}
}

func TestPolicyValidate(t *testing.T) {
	if err := DefaultPolicy().Validate(); err != nil {
		t.Fatalf("default policy: %v", err)
	}
	bad := []Policy{
		{MinHours: 0, MaxHours: 10, DefaultHours: 5},
		{MinHours: 5, MaxHours: 4, DefaultHours: 5},
		{MinHours: 2, MaxHours: 10, DefaultHours: 11},
		{MinHours: 2, MaxHours: 10, DefaultHours: 1},
		{MinHours: 1, MaxHours: 10, DefaultHours: 5, MaxExtensionHours: -1},
	}
	for i, p := range bad {
		if p.Validate() == nil {
			t.Errorf("policy %d %+v should be invalid", i, p)
		}
	}
}

func TestResolve(t *testing.T) {
	p := Policy{MinHours: 2, MaxHours: 100, DefaultHours: 24, MaxExtensionHours: 10}
	dev := EnvRule{Name: "development", AllowEphemeral: true}
	tests := []struct {
		name      string
		req       int
		env       EnvRule
		wantHours int
		wantCode  string
	}{
		{"default when omitted", 0, dev, 24, ""},
		{"at the minimum", 2, dev, 2, ""},
		{"at the maximum", 100, dev, 100, ""},
		{"below the minimum", 1, dev, 0, "minimum"},
		{"negative", -5, dev, 0, "minimum"},
		{"above the maximum", 101, dev, 0, "maximum"},
		{"environment ceiling wins", 50, EnvRule{Name: "staging", AllowEphemeral: true, MaxTTLHours: ptr(48)}, 0, "maximum"},
		{"environment ceiling above the policy is ignored", 100, EnvRule{Name: "staging", AllowEphemeral: true, MaxTTLHours: ptr(500)}, 100, ""},
		{"environment forbids", 24, EnvRule{Name: "staging"}, 0, "ephemeral_not_allowed"},
		{"production is never ephemeral, even if the row says so", 24, EnvRule{Name: "production", AllowEphemeral: true}, 0, "ephemeral_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hours, errs := p.Resolve(tt.req, tt.env)
			if tt.wantCode == "" {
				if len(errs) != 0 || hours != tt.wantHours {
					t.Fatalf("Resolve = %d, %v; want %d", hours, errs, tt.wantHours)
				}
				return
			}
			if len(errs) != 1 || errs[0].Code != tt.wantCode {
				t.Fatalf("Resolve errors = %v, want code %s", errs, tt.wantCode)
			}
		})
	}
}

func TestExpiresAtAndHoursLeft(t *testing.T) {
	if got := ExpiresAt(t0, 72); !got.Equal(t0.Add(72 * time.Hour)) {
		t.Fatalf("ExpiresAt = %v", got)
	}
	exp := t0.Add(90 * time.Minute)
	for _, tc := range []struct {
		now  time.Time
		want int
	}{{t0, 2}, {t0.Add(30 * time.Minute), 1}, {exp, 0}, {exp.Add(time.Hour), 0}} {
		if got := HoursLeft(exp, tc.now); got != tc.want {
			t.Errorf("HoursLeft at %v = %d, want %d", tc.now, got, tc.want)
		}
	}
}

func TestPhaseAt(t *testing.T) {
	exp := t0.Add(48 * time.Hour)
	warnedAt := t0
	tests := []struct {
		name  string
		s     Stack
		now   time.Time
		phase Phase
	}{
		{"far from expiry", stack("development", exp), t0, PhaseNone},
		{"one second before the window", stack("development", exp), exp.Add(-WarnBefore - time.Second), PhaseNone},
		{"window opens exactly 24 h before", stack("development", exp), exp.Add(-WarnBefore), PhaseWarn},
		{"inside the window", stack("development", exp), exp.Add(-time.Hour), PhaseWarn},
		{"already warned", func() Stack { s := stack("development", exp); s.WarnedAt = &warnedAt; return s }(), exp.Add(-time.Hour), PhaseNone},
		{"expires exactly now", stack("development", exp), exp, PhaseTeardown},
		{"long expired and warned", func() Stack { s := stack("staging", exp); s.WarnedAt = &warnedAt; return s }(), exp.Add(72 * time.Hour), PhaseTeardown},
		{"production is never due", stack("production", exp), exp.Add(time.Hour), PhaseNone},
		{"permanent stack", Stack{Environment: "development", Status: "active"}, exp, PhaseNone},
		{"ephemeral without expiry", Stack{Environment: "development", Status: "active", IsEphemeral: true}, exp, PhaseNone},
		{"already destroying", func() Stack { s := stack("development", exp); s.Status = "destroying"; return s }(), exp.Add(time.Hour), PhaseNone},
		{"already destroyed", func() Stack { s := stack("development", exp); s.Status = "destroyed"; return s }(), exp.Add(time.Hour), PhaseNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PhaseAt(tt.s, tt.now); got != tt.phase {
				t.Fatalf("PhaseAt = %s, want %s", got, tt.phase)
			}
		})
	}
}

func TestEligibleCodes(t *testing.T) {
	exp := t0.Add(time.Hour)
	for _, tc := range []struct {
		s    Stack
		code string
	}{
		{Stack{Environment: "development", Status: "active"}, "not_ephemeral"},
		{stack("production", exp), "production_not_ephemeral"},
		{func() Stack { s := stack("development", exp); s.Status = "destroyed"; return s }(), "stack_destroyed"},
	} {
		e, ok := apperr.As(Eligible(tc.s))
		if !ok || e.Code != tc.code || e.Kind != apperr.KindConflict {
			t.Errorf("Eligible(%+v) = %v, want conflict %s", tc.s, e, tc.code)
		}
	}
	if err := Eligible(stack("staging", exp)); err != nil {
		t.Errorf("a live ephemeral staging stack is eligible: %v", err)
	}
}

func TestExtend(t *testing.T) {
	p := Policy{MinHours: 1, MaxHours: 72, DefaultHours: 24, MaxExtensionHours: 100}
	exp := t0.Add(10 * time.Hour)
	base := stack("development", exp)

	t.Run("adds to the current expiry", func(t *testing.T) {
		ext, err := p.Extend(base, 24, t0)
		if err != nil || !ext.ExpiresAt.Equal(exp.Add(24*time.Hour)) || ext.ExtendedHours != 24 {
			t.Fatalf("Extend = %+v, %v", ext, err)
		}
	})
	t.Run("an expired but live stack restarts from now", func(t *testing.T) {
		s := stack("development", t0.Add(-5*time.Hour))
		ext, err := p.Extend(s, 12, t0)
		if err != nil || !ext.ExpiresAt.Equal(t0.Add(12*time.Hour)) {
			t.Fatalf("Extend = %+v, %v", ext, err)
		}
	})
	t.Run("extensions accumulate against the cap", func(t *testing.T) {
		s := base
		s.TTLExtendedHours = 80
		if _, err := p.Extend(s, 20, t0); err != nil {
			t.Fatalf("exactly at the cap must pass: %v", err)
		}
		_, err := p.Extend(s, 21, t0)
		if e, ok := apperr.As(err); !ok || e.Code != "ttl_extension_cap" || !strings.Contains(e.Message, "20 h left") {
			t.Fatalf("over the cap = %v", err)
		}
	})
	t.Run("bounds on a single extension", func(t *testing.T) {
		for _, h := range []int{0, -1, 73} {
			_, err := p.Extend(base, h, t0)
			if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindInvalid {
				t.Errorf("hours=%d: %v, want invalid", h, err)
			}
		}
	})
	t.Run("environment lifetime ceiling", func(t *testing.T) {
		s := base
		s.MaxTTLHours = ptr(168) // created 48 h before t0, expires t0+10h = 58 h lifetime
		if _, err := p.Extend(s, 72, t0); err != nil {
			t.Fatalf("58+72 = 130 <= 168: %v", err)
		}
		s.MaxTTLHours = ptr(100)
		_, err := p.Extend(s, 72, t0)
		if e, ok := apperr.As(err); !ok || e.Code != "ttl_environment_max" {
			t.Fatalf("ceiling = %v", err)
		}
	})
	t.Run("never for production or permanent stacks", func(t *testing.T) {
		if _, err := p.Extend(stack("production", exp), 1, t0); err == nil {
			t.Fatal("production extension must fail")
		}
		if _, err := p.Extend(Stack{Environment: "development", Status: "active"}, 1, t0); err == nil {
			t.Fatal("permanent stack extension must fail")
		}
	})
}

func TestClassifyOrdersTeardownFirstAndIsDeterministic(t *testing.T) {
	mk := func(id, env string, expires time.Time) Stack {
		s := stack(env, expires)
		s.ID = id
		return s
	}
	in := []Stack{
		mk("w2", "development", t0.Add(20*time.Hour)),
		mk("none", "development", t0.Add(100*time.Hour)),
		mk("d2", "staging", t0.Add(-time.Hour)),
		mk("prod", "production", t0.Add(-time.Hour)),
		mk("w1", "development", t0.Add(2*time.Hour)),
		mk("d1", "development", t0.Add(-5*time.Hour)),
		mk("d1b", "development", t0.Add(-5*time.Hour)),
	}
	got := Classify(in, t0)
	var ids []string
	for _, d := range got {
		ids = append(ids, string(d.Phase)+":"+d.Stack.ID)
	}
	want := "teardown:d1,teardown:d1b,teardown:d2,warn:w1,warn:w2"
	if strings.Join(ids, ",") != want {
		t.Fatalf("Classify = %v, want %s", ids, want)
	}
}

func TestWarningComment(t *testing.T) {
	s := stack("development", t0.Add(5*time.Hour+time.Minute))
	c := WarningComment(s, t0, "https://portal.example/stacks/s1")
	for _, want := range []string{s.Path, "2026-03-01T17:01:00Z", "6 h", "https://portal.example/stacks/s1"} {
		if !strings.Contains(c, want) {
			t.Errorf("comment lacks %q:\n%s", want, c)
		}
	}
	if !strings.Contains(WarningComment(s, t0, ""), "ttl/extend") {
		t.Error("without a portal URL the comment names the API")
	}
}
