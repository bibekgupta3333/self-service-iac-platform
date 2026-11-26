package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

type fakeBudgetAdmin struct {
	sets    []budgets.SetInput
	months  []string
	actuals []budgets.ActualInput
}

func (f *fakeBudgetAdmin) Set(_ context.Context, _ identity.Principal, team, month string, in budgets.SetInput, _ string) (budgets.Setting, error) {
	if team == "ghost" {
		return budgets.Setting{}, apperr.NotFound("team %q not found", team)
	}
	f.sets = append(f.sets, in)
	m, _ := budgets.ParseMonth(month)
	spent := 100.0
	return budgets.Setting{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000bb"), Team: team, Month: m, Environment: "production",
		LimitUSD: *in.LimitUSD, SpentUSD: spent, ActualUSD: &spent, HardBlock: true, AlertThresholds: []int{50, 80, 100},
		Override:  &budgets.Override{GrantedBy: "ada", Reason: "launch week capacity", ExpiresAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		UpdatedBy: "ada", UpdatedAt: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeBudgetAdmin) List(_ context.Context, _ identity.Principal, team, month string) ([]budgets.Setting, error) {
	f.months = append(f.months, month)
	return []budgets.Setting{{ID: uuid.New(), Team: team, Month: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), LimitUSD: 200, SpentUSD: 50}}, nil
}

func (f *fakeBudgetAdmin) RecordActual(_ context.Context, _ identity.Principal, in budgets.ActualInput, _ string) (budgets.Setting, error) {
	f.actuals = append(f.actuals, in)
	return budgets.Setting{ID: uuid.New(), Team: in.Team, Month: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ActualUSD: in.ActualUSD}, nil
}

func budgetAdminRouter(svc BudgetAdminService, role rbac.Role, policies PolicyTable) http.Handler {
	h := NewBudgetAdminHandler(svc)
	return NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal:         AnyAuthenticator{NewOIDCAuthenticator(fakeTokenVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:            okPinger{},
		Public:           []RouteRegistrar{h},
		InternalHandlers: []RouteRegistrar{h.Internal()},
		Policies:         policies,
		Roles:            flowRoles{role},
	})
}

const putBody = `{"environment":"production","limit_usd":2500.5,"hard_block":true,"override":{"reason":"launch week capacity","expires_at":"2026-12-01T00:00:00Z"}}`

func TestBudgetAdminRequiresAdmin(t *testing.T) {
	for _, role := range []rbac.Role{rbac.RoleNone, rbac.RoleViewer, rbac.RoleDeveloper, rbac.RoleMaintainer} {
		svc := &fakeBudgetAdmin{}
		h := budgetAdminRouter(svc, role, DefaultPolicies())
		for _, c := range []struct{ method, path, body string }{
			{"PUT", "/v1/teams/payments/budgets/2026-11", putBody},
			{"GET", "/v1/teams/payments/budgets", ""},
			{"GET", "/v1/teams/payments/budgets/2026-11", ""},
		} {
			if rec := do(t, h, c.method, c.path, c.body); rec.Code != 403 {
				t.Errorf("%q %s %s = %d, want 403: %s", role, c.method, c.path, rec.Code, rec.Body)
			}
		}
		if len(svc.sets) != 0 || len(svc.months) != 0 {
			t.Errorf("%q reached the service", role)
		}
	}
}

func TestBudgetAdminRoutesFailClosedWithoutAPolicy(t *testing.T) {
	// A table that lacks the budget routes refuses them, even for an admin.
	h := budgetAdminRouter(&fakeBudgetAdmin{}, rbac.RoleAdmin, PolicyTable{"GET /v1/me": {Action: rbac.ActionRead, Scope: ScopeAuthenticated}})
	if rec := do(t, h, "PUT", "/v1/teams/payments/budgets/2026-11", putBody); rec.Code != 403 {
		t.Fatalf("PUT without a policy entry = %d, want 403", rec.Code)
	}
}

func TestBudgetAdminUnauthenticated(t *testing.T) {
	h := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DenyAuthenticator{}, Ready: okPinger{},
		Public: []RouteRegistrar{NewBudgetAdminHandler(&fakeBudgetAdmin{})}, Policies: DefaultPolicies(), Roles: flowRoles{rbac.RoleAdmin}})
	if rec := do(t, h, "PUT", "/v1/teams/payments/budgets/2026-11", putBody); rec.Code != 401 {
		t.Fatalf("anonymous PUT = %d", rec.Code)
	}
}

func TestBudgetAdminPutAndGet(t *testing.T) {
	svc := &fakeBudgetAdmin{}
	h := budgetAdminRouter(svc, rbac.RoleAdmin, DefaultPolicies())

	rec := do(t, h, "PUT", "/v1/teams/payments/budgets/2026-11", putBody)
	if rec.Code != 200 {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["team"] != "payments" || got["month"] != "2026-11" || got["environment"] != "production" || got["currency"] != "USD" ||
		got["limit_usd"] != 2500.5 || got["used_usd"] != 100.0 || got["updated_by"] != "ada" {
		t.Fatalf("body = %s", rec.Body)
	}
	if o, _ := got["override"].(map[string]any); o["granted_by"] != "ada" {
		t.Fatalf("override = %v", got["override"])
	}
	if len(svc.sets) != 1 || svc.sets[0].Override == nil || svc.sets[0].Override.Reason != "launch week capacity" {
		t.Fatalf("service saw %+v", svc.sets)
	}

	rec = do(t, h, "GET", "/v1/teams/payments/budgets", "")
	var list struct {
		Items []struct {
			Environment *string `json:"environment"`
			Override    any     `json:"override"`
			Utilisation float64 `json:"utilisation_pct"`
		} `json:"items"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Items) != 1 ||
		list.Items[0].Environment != nil || list.Items[0].Override != nil || list.Items[0].Utilisation != 25 {
		t.Fatalf("GET list = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/v1/teams/payments/budgets/2026-11", ""); rec.Code != 200 || svc.months[len(svc.months)-1] != "2026-11" {
		t.Fatalf("GET month = %d, months %v", rec.Code, svc.months)
	}
}

func TestBudgetAdminPutRejectsBadBodies(t *testing.T) {
	svc := &fakeBudgetAdmin{}
	h := budgetAdminRouter(svc, rbac.RoleAdmin, DefaultPolicies())
	for name, body := range map[string]string{
		"unknown field": `{"limit_usd":1,"owner":"x"}`,
		"trailing data": `{"limit_usd":1} {}`,
		"not json":      `limit`,
		"wrong type":    `{"limit_usd":"lots"}`,
	} {
		if rec := do(t, h, "PUT", "/v1/teams/payments/budgets/2026-11", body); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(svc.sets) != 0 {
		t.Fatal("a bad body reached the service")
	}
	if rec := do(t, h, "PUT", "/v1/teams/ghost/budgets/2026-11", `{"limit_usd":1}`); rec.Code != 404 {
		t.Errorf("unknown team = %d", rec.Code)
	}
}

func TestBudgetActualsRouteClass(t *testing.T) {
	svc := &fakeBudgetAdmin{}
	h := budgetAdminRouter(svc, rbac.RoleAdmin, DefaultPolicies())
	const body = `{"team":"payments","month":"2026-11","actual_usd":321.5}`
	for header, want := range map[string]int{
		"Bearer " + testInternalToken: 200, // the shared token: the future cost worker
		"Bearer apply.jwt.token":      403, // no workflow may write the spend a gate reads
		"Bearer prvalidate.jwt.token": 403,
		"Bearer release.jwt.token":    403,
		"Bearer bad.jwt.token":        401,
		"":                            401,
	} {
		if rec := do(t, h, "POST", "/v1/internal/budget-actuals", body, "Authorization", header); rec.Code != want {
			t.Errorf("%q: %d, want %d: %s", header, rec.Code, want, rec.Body)
		}
	}
	if len(svc.actuals) != 1 || svc.actuals[0].Team != "payments" || *svc.actuals[0].ActualUSD != 321.5 {
		t.Fatalf("service saw %+v", svc.actuals)
	}
	// A session user never reaches the internal route.
	if rec := do(t, h, "POST", "/v1/internal/budget-actuals", body); rec.Code != 401 {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/v1/internal/budget-actuals", `{"team":"payments","bogus":1}`,
		"Authorization", "Bearer "+testInternalToken); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid JSON") {
		t.Fatalf("unknown field = %d %s", rec.Code, rec.Body)
	}
}
