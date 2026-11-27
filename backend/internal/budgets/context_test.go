package budgets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

var now = time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC)

func TestParseStackPath(t *testing.T) {
	ref, err := ParseStackPath("environments/production/payments/order-events/")
	if err != nil || ref != (StackRef{Environment: "production", Team: "payments", Stack: "order-events"}) {
		t.Fatalf("got %+v %v", ref, err)
	}
	if ref.Path() != "environments/production/payments/order-events" {
		t.Fatalf("path = %q", ref.Path())
	}
	for _, bad := range []string{
		"", "environments/production/payments", "modules/s3/x/y", "environments/qa/payments/orders",
		"environments/production/Payments/orders", "environments/production/payments/orders/extra",
		"environments/production/../orders", "environments/production/payments/o",
	} {
		_, err := ParseStackPath(bad)
		if e, ok := apperr.As(err); !ok || e.Code != "invalid_stack_path" {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestMonthOf(t *testing.T) {
	got := MonthOf(time.Date(2026, 12, 31, 23, 30, 0, 0, time.FixedZone("x", -3600)))
	if !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("MonthOf = %v (must use UTC)", got)
	}
}

func TestBuildContextShape(t *testing.T) {
	ref := StackRef{Environment: "production", Team: "payments", Stack: "order-events"}
	capUSD := 5000.0
	budget := &Budget{Month: MonthOf(now), LimitUSD: 2000, SpentUSD: 0, HardBlock: true, Scope: ScopeEnvironment,
		SingleChangeCapUSD: &capUSD, CommittedUSD: 1200.5}
	yanked := []YankedVersion{{Module: "sqs", Version: "1.1.0"}, {Module: "s3", Version: "1.2.0"}, {Module: "s3", Version: "1.2.0"}}
	doc := BuildContext(ref, Environment{Name: "production", AllowedRegions: []string{"us-east-1"}}, budget, yanked, now)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"iacp":{"context":{"environment":"production","team":"payments","stack":"order-events",` +
		`"stack_path":"environments/production/payments/order-events","allowed_regions":["us-east-1"],` +
		`"budget":{"month":"2026-11","limit_usd":2000,"spent_usd":0,"actual_usd":null,"hard_block":true,"scope":"environment","single_change_cap_usd":5000},` +
		`"budget_override":null,"current_committed_usd":1200.5,"exceptions":[],` +
		`"yanked_module_versions":[{"module":"s3","version":"1.2.0","tag":"modules/s3/v1.2.0"},` +
		`{"module":"sqs","version":"1.1.0","tag":"modules/sqs/v1.1.0"}],"now":"2026-11-24T10:05:00Z"}}}`
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
}

func TestBuildContextActualAndOverride(t *testing.T) {
	actual := 1450.1
	budget := &Budget{Month: MonthOf(now), LimitUSD: 2000, SpentUSD: 900, ActualUSD: &actual, HardBlock: true, Scope: ScopeTeam,
		Override: &Override{GrantedBy: "ada", Reason: "launch week capacity", MaxDeltaUSD: 750,
			ExpiresAt: time.Date(2026, 12, 1, 3, 0, 0, 0, time.FixedZone("x", 3600))}}
	doc := BuildContext(StackRef{Environment: "staging", Team: "payments", Stack: "orders"}, Environment{Name: "staging"}, budget, nil, now)
	raw, _ := json.Marshal(doc)
	for _, want := range []string{`"actual_usd":1450.1`, `"single_change_cap_usd":null`,
		`"budget_override":{"granted_by":"ada","reason":"launch week capacity","expires_at":"2026-12-01T02:00:00Z","max_delta_usd":750}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("context lacks %s: %s", want, raw)
		}
	}
}

func TestBuildContextWithoutBudgetOrYanked(t *testing.T) {
	doc := BuildContext(StackRef{Environment: "development", Team: "platform", Stack: "smoke"},
		Environment{Name: "development"}, nil, nil, now)
	raw, _ := json.Marshal(doc)
	var got struct {
		IACP struct {
			Context map[string]any `json:"context"`
		} `json:"iacp"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	c := got.IACP.Context
	if c["budget"] != nil {
		t.Fatalf("budget = %v, want null", c["budget"])
	}
	for _, k := range []string{"allowed_regions", "exceptions", "yanked_module_versions"} {
		if arr, ok := c[k].([]any); !ok || len(arr) != 0 {
			t.Errorf("%s = %#v, want [] (never null, so policies can iterate it)", k, c[k])
		}
	}
}

type fakeRepo struct {
	envErr    error
	budget    *Budget
	budgetErr error
	yanked    []YankedVersion
	gotMonth  time.Time
	committed float64
	asked     []string // team|env|excludePath of each CommittedUSD call
}

func (f *fakeRepo) Environment(_ context.Context, name string) (Environment, error) {
	return Environment{Name: name, AllowedRegions: []string{"us-east-1"}}, f.envErr
}

func (f *fakeRepo) MonthlyBudget(_ context.Context, _, _ string, month time.Time) (*Budget, error) {
	f.gotMonth = month
	return f.budget, f.budgetErr
}

func (f *fakeRepo) CommittedUSD(_ context.Context, team, env, exclude string) (float64, error) {
	f.asked = append(f.asked, team+"|"+env+"|"+exclude)
	return f.committed, nil
}

func (f *fakeRepo) YankedVersions(context.Context) ([]YankedVersion, error) { return f.yanked, nil }

func TestServiceContext(t *testing.T) {
	repo := &fakeRepo{budget: &Budget{LimitUSD: 50, Scope: ScopeTeam}, yanked: []YankedVersion{{Module: "s3", Version: "1.0.1"}}}
	svc := NewService(repo, platform.FixedClock{T: now})
	doc, err := svc.Context(context.Background(), "environments/development/platform/smoke")
	if err != nil {
		t.Fatal(err)
	}
	c := doc.IACP.Context
	if c.Team != "platform" || c.Budget == nil || c.Budget.LimitUSD != 50 || len(c.YankedModuleVersions) != 1 {
		t.Fatalf("context = %+v", c)
	}
	if !repo.gotMonth.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("budget month = %v", repo.gotMonth)
	}
	// A team-wide budget sums every environment and leaves out the stack being changed.
	if len(repo.asked) != 1 || repo.asked[0] != "platform||environments/development/platform/smoke" {
		t.Fatalf("committed query = %v", repo.asked)
	}
	// An environment budget sums only that environment; the total is served as current_committed_usd.
	envRepo := &fakeRepo{budget: &Budget{LimitUSD: 50, Scope: ScopeEnvironment}, committed: 321.25}
	doc, err = NewService(envRepo, platform.FixedClock{T: now}).Context(context.Background(), "environments/staging/payments/orders")
	if err != nil || envRepo.asked[0] != "payments|staging|environments/staging/payments/orders" ||
		doc.IACP.Context.CurrentCommittedUSD == nil || *doc.IACP.Context.CurrentCommittedUSD != 321.25 {
		t.Fatalf("environment budget: %+v, %v, %v", doc.IACP.Context.CurrentCommittedUSD, envRepo.asked, err)
	}
	// Without a budget there is nothing to sum.
	none := &fakeRepo{}
	doc, _ = NewService(none, platform.FixedClock{T: now}).Context(context.Background(), "environments/staging/payments/orders")
	if len(none.asked) != 0 || doc.IACP.Context.CurrentCommittedUSD != nil {
		t.Fatalf("no budget: asked %v, committed %v", none.asked, doc.IACP.Context.CurrentCommittedUSD)
	}

	if _, err := svc.Context(context.Background(), "nope"); !apperr.IsKind(err, apperr.KindInvalid) {
		t.Fatalf("bad path: %v", err)
	}
	missing := apperr.NotFound("no env")
	if _, err := NewService(&fakeRepo{envErr: missing}, platform.FixedClock{T: now}).
		Context(context.Background(), "environments/staging/a1/b1"); !errors.Is(err, missing) {
		t.Fatalf("missing env: %v", err)
	}
	boom := errors.New("db down")
	if _, err := NewService(&fakeRepo{budgetErr: boom}, platform.FixedClock{T: now}).
		Context(context.Background(), "environments/staging/a1/b1"); !errors.Is(err, boom) {
		t.Fatalf("budget error: %v", err)
	}
}
