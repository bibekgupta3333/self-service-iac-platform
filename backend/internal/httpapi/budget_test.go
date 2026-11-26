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

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
)

type fakeBudgetContext struct{ paths []string }

func (f *fakeBudgetContext) Context(_ context.Context, path string) (budgets.Document, error) {
	ref, err := budgets.ParseStackPath(path)
	if err != nil {
		return budgets.Document{}, err
	}
	if ref.Environment == "production" {
		return budgets.Document{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "environment_not_found", Message: "no"}
	}
	f.paths = append(f.paths, path)
	return budgets.BuildContext(ref, budgets.Environment{AllowedRegions: []string{"us-east-1"}},
		&budgets.Budget{LimitUSD: 100, HardBlock: true, Scope: budgets.ScopeTeam},
		[]budgets.YankedVersion{{Module: "s3", Version: "1.1.0"}}, time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)), nil
}

func budgetRouter(svc BudgetContextService) http.Handler {
	return NewRouter(Deps{Policies: testPolicies(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal:         AnyAuthenticator{NewOIDCAuthenticator(fakeTokenVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:            okPinger{},
		InternalHandlers: []RouteRegistrar{NewBudgetContextHandler(svc)},
	})
}

func TestBudgetContextRouteClass(t *testing.T) {
	const url = "/v1/internal/budget-context?stack_path=environments/development/payments/orders"
	h := budgetRouter(&fakeBudgetContext{})
	for header, want := range map[string]int{
		"Bearer prvalidate.jwt.token":       200, // pr-validate.yml on the PR merge ref
		"Bearer apply.jwt.token":            200, // apply.yml on main
		"Bearer " + testInternalToken:       200, // shared token, not workflow-scoped
		"Bearer release.jwt.token":          403, // module-release.yml is not in the class
		"Bearer bad.jwt.token":              401,
		"":                                  401,
		"Bearer " + strings.Repeat("x", 40): 401,
	} {
		rec := do(t, h, "GET", url, "", "Authorization", header)
		if rec.Code != want {
			t.Errorf("%q: status = %d, want %d: %s", header, rec.Code, want, rec.Body)
		}
	}
	// The dev user authenticator never opens it.
	if rec := do(t, h, "GET", url, ""); rec.Code != 401 {
		t.Fatalf("anonymous = %d", rec.Code)
	}
}

func TestBudgetContextBody(t *testing.T) {
	svc := &fakeBudgetContext{}
	h := budgetRouter(svc)
	auth := []string{"Authorization", "Bearer " + testInternalToken}

	rec := do(t, h, "GET", "/v1/internal/budget-context?stack=environments/development/payments/orders", "", auth...)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("alias: %d %s", rec.Code, rec.Body)
	}
	var doc struct {
		IACP struct {
			Context struct {
				Environment string `json:"environment"`
				Budget      struct {
					LimitUSD float64 `json:"limit_usd"`
				} `json:"budget"`
				Yanked []struct {
					Tag string `json:"tag"`
				} `json:"yanked_module_versions"`
			} `json:"context"`
		} `json:"iacp"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	c := doc.IACP.Context
	if c.Environment != "development" || c.Budget.LimitUSD != 100 || len(c.Yanked) != 1 || c.Yanked[0].Tag != "modules/s3/v1.1.0" {
		t.Fatalf("context = %+v", c)
	}

	for url, want := range map[string]string{
		"/v1/internal/budget-context":                       "invalid_stack_path",
		"/v1/internal/budget-context?stack_path=modules/s3": "invalid_stack_path",
		"/v1/internal/budget-context?stack_path=environments/development/a1/b1&stack=environments/development/a1/b2": "invalid_stack_path",
		"/v1/internal/budget-context?stack_path=environments/production/payments/orders":                             "environment_not_found",
	} {
		rec := do(t, h, "GET", url, "", auth...)
		if !strings.Contains(rec.Body.String(), `"code":"`+want+`"`) {
			t.Errorf("%s: %d %s, want %s", url, rec.Code, rec.Body, want)
		}
	}
	if len(svc.paths) != 1 {
		t.Fatalf("service called for %v", svc.paths)
	}
}
