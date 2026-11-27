package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

type fakeLookup struct{ calls [][2]string }

func (f *fakeLookup) ApplyTargetByPath(_ context.Context, path, head string) (requests.ApplyTarget, error) {
	f.calls = append(f.calls, [2]string{path, head})
	env := ""
	if parts := strings.Split(path, "/"); len(parts) == 4 {
		env = parts[1]
	}
	return requests.ApplyTarget{StackID: lockStack, StackPath: path, RequestID: lockHolder, RequestShortID: "req-7k3p9x",
		Kind: requests.KindUpdate, Status: requests.StatusApproved, FencingToken: 48214,
		ApprovedPlanHash: strings.Repeat("ab", 32), Environment: env, RequestedBy: "ada", PRNumber: 812,
		AllowStatefulDestroy: env == "production"}, nil
}

func lookupRouter(svc RequestLookupService) http.Handler {
	return NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal:         AnyAuthenticator{NewOIDCAuthenticator(fakeTokenVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:            okPinger{},
		InternalHandlers: []RouteRegistrar{NewRequestLookupHandler(svc)},
	})
}

const lookupURL = "/v1/internal/requests/by-path?stack_path=environments/development/payments/orders"

func TestRequestLookupRouteClass(t *testing.T) {
	h := lookupRouter(&fakeLookup{})
	for header, want := range map[string]int{
		"Bearer apply.jwt.token":      200, // apply.yml on main
		"Bearer " + testInternalToken: 200, // shared token, not workflow-scoped
		"Bearer prvalidate.jwt.token": 403, // a PR token must never read fencing tokens or approved hashes
		"Bearer release.jwt.token":    403,
		"Bearer bad.jwt.token":        401,
		"":                            401,
	} {
		rec := do(t, h, "GET", lookupURL, "", "Authorization", header)
		if rec.Code != want {
			t.Errorf("%q: status = %d, want %d: %s", header, rec.Code, want, rec.Body)
		}
	}
	if rec := do(t, h, "GET", lookupURL, ""); rec.Code != 401 {
		t.Fatalf("the dev user authenticator must not open a machine route: %d", rec.Code)
	}
}

// A production stack's lock credential and approved hash go only to an apply.yml job that
// ran in the production GitHub Environment, i.e. after its required reviewers approved it
// (WBS 8.8). A job bound to another Environment cannot read a stack of a different one.
func TestRequestLookupProductionNeedsTheEnvironment(t *testing.T) {
	h := lookupRouter(&fakeLookup{})
	const prodURL = "/v1/internal/requests/by-path?stack_path=environments/production/payments/orders"
	const stagingURL = "/v1/internal/requests/by-path?stack_path=environments/staging/payments/orders"
	tests := []struct {
		url, token string
		status     int
		code       string
	}{
		{prodURL, "applyprod.jwt.token", 200, ""},
		{prodURL, "apply.jwt.token", 403, "production_environment_required"},
		{prodURL, "applystaging.jwt.token", 403, "production_environment_required"},
		{prodURL, testInternalToken, 403, "oidc_required"},
		{stagingURL, "applystaging.jwt.token", 200, ""},
		{stagingURL, "apply.jwt.token", 200, ""}, // no Environment: still accepted outside production
		{stagingURL, "applyprod.jwt.token", 403, "environment_mismatch"},
		{lookupURL, "applystaging.jwt.token", 403, "environment_mismatch"},
	}
	for _, tt := range tests {
		rec := do(t, h, "GET", tt.url, "", "Authorization", "Bearer "+tt.token)
		if rec.Code != tt.status || (tt.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+tt.code+`"`)) {
			t.Errorf("%s with %s: %d %s, want %d %s", tt.url, tt.token, rec.Code, rec.Body, tt.status, tt.code)
		}
	}
	rec := do(t, h, "GET", prodURL, "", "Authorization", "Bearer applyprod.jwt.token")
	var got struct {
		Environment          string `json:"environment"`
		RequestedBy          string `json:"requested_by"`
		PRNumber             int    `json:"pr_number"`
		AllowStatefulDestroy *bool  `json:"allow_stateful_destroy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Environment != "production" || got.RequestedBy != "ada" ||
		got.PRNumber != 812 || got.AllowStatefulDestroy == nil || !*got.AllowStatefulDestroy {
		t.Fatalf("production body = %s (%+v, %v)", rec.Body, got, err)
	}
}

func TestRequestLookupBody(t *testing.T) {
	svc := &fakeLookup{}
	h := lookupRouter(svc)
	auth := []string{"Authorization", "Bearer " + testInternalToken}
	head := strings.Repeat("c", 40)

	rec := do(t, h, "GET", lookupURL+"&head_sha="+head, "", auth...)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	var got struct {
		StackID          uuid.UUID `json:"stack_id"`
		RequestID        uuid.UUID `json:"request_id"`
		RequestShortID   string    `json:"request_short_id"`
		FencingToken     int64     `json:"fencing_token"`
		ApprovedPlanHash string    `json:"approved_plan_hash"`
		Status           string    `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.StackID != lockStack || got.RequestID != lockHolder || got.RequestShortID != "req-7k3p9x" || got.FencingToken != 48214 ||
		got.ApprovedPlanHash != strings.Repeat("ab", 32) || got.Status != "approved" {
		t.Fatalf("body = %+v", got)
	}
	if want := [2]string{"environments/development/payments/orders", head}; svc.calls[0] != want {
		t.Fatalf("service called with %v, want %v", svc.calls[0], want)
	}

	for url, want := range map[string]string{
		"/v1/internal/requests/by-path":                          "invalid_stack_path",
		lookupURL + "&stack_path=environments/development/a1/b1": "invalid_stack_path", // sent twice
		lookupURL + "&head_sha=" + head + "&head_sha=" + head:    "invalid_stack_path",
		lookupURL + "&path=environments/development/payments/x":  "bad_request", // not a documented parameter
		lookupURL + "&headsha=" + head:                           "bad_request", // a typo must not skip the head check
	} {
		rec := do(t, h, "GET", url, "", auth...)
		if rec.Code != 422 && rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"`+want+`"`) {
			t.Errorf("%s: %d %s, want %s", url, rec.Code, rec.Body, want)
		}
	}
	if len(svc.calls) != 1 {
		t.Fatalf("rejected queries reached the service: %v", svc.calls)
	}
}
