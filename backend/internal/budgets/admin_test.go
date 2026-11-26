package budgets

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

func ptr[T any](v T) *T { return &v }

func fieldPointers(t *testing.T, err error) []string {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Kind != apperr.KindInvalid {
		t.Fatalf("err = %v, want an invalid-input error", err)
	}
	var out []string
	for _, f := range e.Fields {
		out = append(out, f.Pointer)
	}
	return out
}

func TestParseMonth(t *testing.T) {
	m, err := ParseMonth("2026-11")
	if err != nil || !m.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ParseMonth = %v, %v", m, err)
	}
	for _, bad := range []string{"", "2026-1", "2026-13", "2026-11-01", "11-2026", "202611", " 2026-11"} {
		if _, err := ParseMonth(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCents(t *testing.T) {
	for _, tc := range []struct {
		in    float64
		cents int64
		ok    bool
	}{
		{0, 0, true}, {2000, 200000, true}, {19.99, 1999, true}, {1000000, 100000000, true},
		{1000000.01, 0, false}, {-0.01, 0, false}, {1.005, 0, false}, {math.NaN(), 0, false}, {math.Inf(1), 0, false},
	} {
		got, ok := Cents(tc.in, MaxLimitUSD)
		if ok != tc.ok || (ok && got != tc.cents) {
			t.Errorf("Cents(%v) = %d, %v; want %d, %v", tc.in, got, ok, tc.cents, tc.ok)
		}
	}
	if FormatCents(200005) != "2000.05" || FormatCents(0) != "0.00" {
		t.Fatal("FormatCents")
	}
}

func TestNewWriteDefaults(t *testing.T) {
	w, err := NewWrite("payments", "2026-11", SetInput{LimitUSD: ptr(2000.0)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.LimitCents != 200000 || !w.HardBlock || w.Environment != "" || w.Override != nil ||
		len(w.Thresholds) != 3 || w.Thresholds[1] != 80 {
		t.Fatalf("write = %+v", w)
	}
	w.Thresholds[0] = 1 // a caller's edit must not reach the shared defaults
	if DefaultThresholds[0] != 50 {
		t.Fatal("DefaultThresholds was aliased")
	}
}

func TestNewWriteFull(t *testing.T) {
	exp := now.Add(48 * time.Hour)
	w, err := NewWrite("payments", "2026-12", SetInput{
		Environment: ptr("production"), LimitUSD: ptr(1234.56), Currency: "USD", HardBlock: ptr(false),
		AlertThresholds: []int{100, 50, 50, 120},
		Override:        &OverrideInput{Reason: "  launch week capacity ok  ", ExpiresAt: exp},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.Environment != "production" || w.LimitCents != 123456 || w.HardBlock ||
		len(w.Thresholds) != 3 || w.Thresholds[0] != 50 || w.Thresholds[2] != 120 ||
		w.Override == nil || w.Override.Reason != "launch week capacity ok" || !w.Override.ExpiresAt.Equal(exp) {
		t.Fatalf("write = %+v", w)
	}
}

func TestNewWriteRejects(t *testing.T) {
	valid := func() SetInput { return SetInput{LimitUSD: ptr(100.0)} }
	for name, tc := range map[string]struct {
		mutate func(*SetInput)
		want   string
	}{
		"missing limit":      {func(i *SetInput) { i.LimitUSD = nil }, "/limit_usd"},
		"negative":           {func(i *SetInput) { i.LimitUSD = ptr(-1.0) }, "/limit_usd"},
		"too large":          {func(i *SetInput) { i.LimitUSD = ptr(1000000.01) }, "/limit_usd"},
		"fraction of a cent": {func(i *SetInput) { i.LimitUSD = ptr(1.005) }, "/limit_usd"},
		"currency":           {func(i *SetInput) { i.Currency = "EUR" }, "/currency"},
		"environment":        {func(i *SetInput) { i.Environment = ptr("qa") }, "/environment"},
		"no thresholds":      {func(i *SetInput) { i.AlertThresholds = []int{} }, "/alert_thresholds"},
		"threshold zero":     {func(i *SetInput) { i.AlertThresholds = []int{0, 50} }, "/alert_thresholds"},
		"threshold huge":     {func(i *SetInput) { i.AlertThresholds = []int{201} }, "/alert_thresholds"},
		"too many":           {func(i *SetInput) { i.AlertThresholds = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} }, "/alert_thresholds"},
		"short reason": {func(i *SetInput) {
			i.Override = &OverrideInput{Reason: "because", ExpiresAt: now.Add(time.Hour)}
		}, "/override/reason"},
		"long reason": {func(i *SetInput) {
			i.Override = &OverrideInput{Reason: strings.Repeat("x", 501), ExpiresAt: now.Add(time.Hour)}
		}, "/override/reason"},
		"expired override": {func(i *SetInput) {
			i.Override = &OverrideInput{Reason: strings.Repeat("x", 20), ExpiresAt: now.Add(-time.Hour)}
		}, "/override/expires_at"},
		"no expiry": {func(i *SetInput) { i.Override = &OverrideInput{Reason: strings.Repeat("x", 20)} }, "/override/expires_at"},
		"override too long": {func(i *SetInput) {
			i.Override = &OverrideInput{Reason: strings.Repeat("x", 20), ExpiresAt: now.Add(MaxOverrideTTL + time.Hour)}
		}, "/override/expires_at"},
	} {
		in := valid()
		tc.mutate(&in)
		_, err := NewWrite("payments", "2026-11", in, now)
		if got := fieldPointers(t, err); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: field errors %v, want [%s]", name, got, tc.want)
		}
	}
}

func TestNewWriteReportsEveryProblem(t *testing.T) {
	_, err := NewWrite("payments", "2026-11", SetInput{Currency: "EUR", Environment: ptr("qa")}, now)
	got := fieldPointers(t, err)
	if len(got) != 3 {
		t.Fatalf("field errors = %v, want limit, currency and environment", got)
	}
}

func TestNewWriteRejectsTeamAndMonth(t *testing.T) {
	in := SetInput{LimitUSD: ptr(1.0)}
	if _, err := NewWrite("Bad Team", "2026-11", in, now); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("team: %v", err)
	}
	for _, month := range []string{"nope", "2020-01", "2030-01"} {
		if _, err := NewWrite("payments", month, in, now); !apperr.IsKind(err, apperr.KindInvalid) {
			t.Errorf("month %s: %v", month, err)
		}
	}
	if _, err := NewWrite("payments", "2025-12", in, now); err != nil {
		t.Errorf("a year back is allowed: %v", err)
	}
}

func TestNewActualWrite(t *testing.T) {
	a, err := NewActualWrite(ActualInput{Team: "payments", Environment: "staging", Month: "2026-11", ActualUSD: ptr(88.5)}, now)
	if err != nil || a.ActualCents != 8850 || a.Environment != "staging" {
		t.Fatalf("actual = %+v, %v", a, err)
	}
	for name, in := range map[string]ActualInput{
		"team":     {Team: "X", Month: "2026-11", ActualUSD: ptr(1.0)},
		"env":      {Team: "payments", Environment: "qa", Month: "2026-11", ActualUSD: ptr(1.0)},
		"month":    {Team: "payments", Month: "soon", ActualUSD: ptr(1.0)},
		"missing":  {Team: "payments", Month: "2026-11"},
		"negative": {Team: "payments", Month: "2026-11", ActualUSD: ptr(-5.0)},
	} {
		if _, err := NewActualWrite(in, now); !apperr.IsKind(err, apperr.KindInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSettingUsageAndUtilisation(t *testing.T) {
	s := Setting{LimitUSD: 2000, SpentUSD: 900}
	if s.UsedUSD() != 900 || s.Utilisation() != 45 {
		t.Fatalf("estimate only: %v %v", s.UsedUSD(), s.Utilisation())
	}
	s.ActualUSD = ptr(1500.0)
	if s.UsedUSD() != 1500 || s.Utilisation() != 75 {
		t.Fatalf("actual above estimate: %v %v", s.UsedUSD(), s.Utilisation())
	}
	s.ActualUSD = ptr(100.0)
	if s.UsedUSD() != 900 {
		t.Fatalf("actual below estimate: %v", s.UsedUSD())
	}
	if (Setting{}).Utilisation() != 0 {
		t.Fatal("zero limit")
	}
}

func TestAuditEvents(t *testing.T) {
	by := identity.Principal{UserID: uuid.MustParse("00000000-0000-0000-0000-0000000000aa"), Type: identity.ActorUser, Login: "ada"}
	id := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
	exp := now.Add(24 * time.Hour)

	create := Write{Team: "payments", Month: MonthOf(now), LimitCents: 200000, HardBlock: true, Thresholds: []int{50, 80, 100}}
	evs := AuditEvents(create, Change{After: Setting{ID: id}}, by, "req-1", now)
	if len(evs) != 1 || evs[0].Action != ActionSet || evs[0].SubjectType != "budget" || evs[0].SubjectID != id.String() ||
		evs[0].ActorID != by.UserID.String() || evs[0].CorrelationID != "req-1" {
		t.Fatalf("create events = %+v", evs)
	}
	var p map[string]any
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p["limit_usd"] != "2000.00" || p["created"] != true ||
		p["environment"] != "all" || p["month"] != "2026-11" {
		t.Fatalf("payload = %s (%v)", evs[0].Payload, err)
	}

	grant := create
	grant.Override = &OverrideWrite{Reason: "launch week capacity ok", ExpiresAt: exp}
	evs = AuditEvents(grant, Change{Before: &Setting{LimitUSD: 1000, HardBlock: false}, After: Setting{ID: id}}, by, "", now)
	if len(evs) != 2 || evs[1].Action != ActionOverrideGranted || !strings.Contains(string(evs[1].Payload), "launch week capacity ok") {
		t.Fatalf("grant events = %+v", evs)
	}
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p["previous_limit_usd"] != "1000.00" || p["created"] != false {
		t.Fatalf("update payload = %s", evs[0].Payload)
	}

	evs = AuditEvents(create, Change{Before: &Setting{Override: &Override{GrantedBy: "x"}}, After: Setting{ID: id}}, by, "", now)
	if len(evs) != 2 || evs[1].Action != ActionOverrideRevoked {
		t.Fatalf("revoke events = %+v", evs)
	}

	actual := ActualAuditEvent(ActualWrite{Team: "payments", Month: MonthOf(now), ActualCents: 5}, Setting{ID: id},
		identity.Principal{Type: identity.ActorSystem, Login: "iacp-bot"}, "", now)
	if actual.Action != ActionActualRecorded || actual.ActorType != "system" || actual.ActorID != "iacp-bot" ||
		!strings.Contains(string(actual.Payload), `"actual_usd":"0.05"`) {
		t.Fatalf("actual event = %+v", actual)
	}
}

type fakeAdminRepo struct {
	role   rbac.Role
	roleEr error
	set    []Write
	listed []string
}

func (f *fakeAdminRepo) RoleInTeam(context.Context, string, uuid.UUID) (rbac.Role, error) {
	return f.role, f.roleEr
}

func (f *fakeAdminRepo) Set(_ context.Context, w Write, _ identity.Principal, _ string) (Change, error) {
	f.set = append(f.set, w)
	return Change{After: Setting{Team: w.Team, LimitUSD: float64(w.LimitCents) / 100}}, nil
}

func (f *fakeAdminRepo) List(_ context.Context, team string, _ *time.Time) ([]Setting, error) {
	f.listed = append(f.listed, team)
	return []Setting{{Team: team}}, nil
}

func (f *fakeAdminRepo) RecordActual(_ context.Context, a ActualWrite, _ identity.Principal, _ string) (Setting, error) {
	return Setting{Team: a.Team, ActualUSD: ptr(float64(a.ActualCents) / 100)}, nil
}

func TestAdminServiceRequiresTeamAdmin(t *testing.T) {
	user := identity.Principal{UserID: uuid.New(), Type: identity.ActorUser}
	in := SetInput{LimitUSD: ptr(10.0)}
	for _, role := range []rbac.Role{rbac.RoleNone, rbac.RoleViewer, rbac.RoleDeveloper, rbac.RoleMaintainer} {
		repo := &fakeAdminRepo{role: role}
		svc := NewAdminService(repo, platform.FixedClock{T: now})
		if _, err := svc.Set(context.Background(), user, "payments", "2026-11", in, ""); !apperr.IsKind(err, apperr.KindForbidden) {
			t.Errorf("%q set: %v", role, err)
		}
		if _, err := svc.List(context.Background(), user, "payments", ""); !apperr.IsKind(err, apperr.KindForbidden) {
			t.Errorf("%q list: %v", role, err)
		}
		if len(repo.set) != 0 || len(repo.listed) != 0 {
			t.Errorf("%q reached the repository", role)
		}
	}
	repo := &fakeAdminRepo{role: rbac.RoleAdmin}
	svc := NewAdminService(repo, platform.FixedClock{T: now})
	got, err := svc.Set(context.Background(), user, "payments", "2026-11", in, "")
	if err != nil || got.LimitUSD != 10 || len(repo.set) != 1 {
		t.Fatalf("admin set = %+v, %v", got, err)
	}
	if _, err := svc.List(context.Background(), user, "payments", "bad"); !apperr.IsKind(err, apperr.KindInvalid) {
		t.Errorf("bad month: %v", err)
	}
	if rows, err := svc.List(context.Background(), user, "payments", "2026-11"); err != nil || len(rows) != 1 {
		t.Errorf("list = %v, %v", rows, err)
	}
	// Validation runs after authorisation and before the repository.
	if _, err := svc.Set(context.Background(), user, "payments", "2026-11", SetInput{}, ""); !apperr.IsKind(err, apperr.KindInvalid) || len(repo.set) != 1 {
		t.Errorf("invalid input: %v", err)
	}
	// An unknown team surfaces as not found.
	repo.roleEr = apperr.NotFound("team")
	if _, err := svc.Set(context.Background(), user, "ghost", "2026-11", in, ""); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("unknown team: %v", err)
	}
}

func TestAdminServiceRecordActualNeedsNoRole(t *testing.T) {
	svc := NewAdminService(&fakeAdminRepo{}, platform.FixedClock{T: now})
	s, err := svc.RecordActual(context.Background(), identity.Principal{Type: identity.ActorSystem}, ActualInput{
		Team: "payments", Month: "2026-11", ActualUSD: ptr(12.34)}, "")
	if err != nil || s.ActualUSD == nil || *s.ActualUSD != 12.34 {
		t.Fatalf("actual = %+v, %v", s, err)
	}
}
