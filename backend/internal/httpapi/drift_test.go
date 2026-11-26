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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/drift"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

var (
	driftReportID = uuid.MustParse("5d1c2d3e-4f50-4617-8829-3a4b5c6d7e8f")
	driftAt       = time.Date(2026, 11, 26, 2, 0, 7, 0, time.UTC)
)

type driftVerifier struct{}

func (driftVerifier) Verify(_ context.Context, raw string) (auth.Claims, error) {
	switch raw {
	case "drift.jwt.token":
		return workflowClaims("drift.yml", "refs/heads/main", "schedule"), nil
	case "driftdispatch.jwt.token":
		return workflowClaims("drift.yml", "refs/heads/main", "workflow_dispatch"), nil
	case "driftpr.jwt.token":
		return workflowClaims("drift.yml", "refs/pull/812/merge", "pull_request"), nil
	case "driftpush.jwt.token":
		return workflowClaims("drift.yml", "refs/heads/main", "push"), nil
	case "apply.jwt.token":
		return workflowClaims("apply.yml", "refs/heads/main", "push"), nil
	case "prvalidate.jwt.token":
		return workflowClaims("pr-validate.yml", "refs/pull/812/merge", "pull_request"), nil
	}
	return auth.Claims{}, auth.ErrInvalidToken
}

type fakeDriftIngest struct {
	reports []drift.Report
	limits  []int
}

func (f *fakeDriftIngest) Record(_ context.Context, _ identity.Principal, r drift.Report) (drift.Recorded, error) {
	if errs := drift.Validate(r); len(errs) > 0 {
		return drift.Recorded{}, apperr.Invalid("validation_failed", "drift report is invalid", errs...)
	}
	f.reports = append(f.reports, r)
	open := drift.StatusOpen
	e := drift.Entry{ID: driftReportID, Result: r.Result}
	if r.Result == drift.ResultDrifted {
		e.Status = &open
	}
	return drift.Recorded{Entry: e, Replayed: len(f.reports) > 1}, nil
}

func (f *fakeDriftIngest) DueStacks(_ context.Context, limit int) ([]drift.DueStack, error) {
	f.limits = append(f.limits, limit)
	return []drift.DueStack{{ID: lockStack, Path: "environments/development/payments/orders", Team: "payments",
		Environment: "development", Region: "us-east-1"}}, nil
}

func driftRouter(svc DriftRecorder) http.Handler {
	return NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal:         AnyAuthenticator{NewOIDCAuthenticator(driftVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:            okPinger{},
		InternalHandlers: []RouteRegistrar{NewDriftIngestHandler(svc)},
	})
}

const driftBody = `{
  "stack_path": "environments/development/payments/orders",
  "github": {"run_id": 11873399021, "run_attempt": 2, "head_sha": "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4"},
  "result": "drifted", "terraform_exit_code": 2,
  "summary": {"drifted": 1, "updated": 1, "deleted": 0},
  "changed": [{"address": "module.q.aws_sqs_queue.this", "type": "aws_sqs_queue", "actions": ["update"]}],
  "plan_hash": "` + "3f9a1c0b3f9a1c0b3f9a1c0b3f9a1c0b3f9a1c0b3f9a1c0b3f9a1c0b3f9a1c0b" + `"
}`

func TestDriftRoutesAcceptOnlyTheDriftWorkflowOnMain(t *testing.T) {
	for header, want := range map[string]int{
		"Bearer drift.jwt.token":         200, // drift.yml on main, schedule
		"Bearer driftdispatch.jwt.token": 200, // drift.yml on main, workflow_dispatch
		"Bearer " + testInternalToken:    200, // shared token, not workflow-scoped
		"Bearer driftpr.jwt.token":       403, // drift.yml from a PR ref: the file is PR-controlled
		"Bearer driftpush.jwt.token":     403, // a push is not a drift trigger
		"Bearer apply.jwt.token":         403,
		"Bearer prvalidate.jwt.token":    403,
		"Bearer bad.jwt.token":           401,
		"":                               401,
	} {
		for _, route := range []struct{ method, path, body string }{
			{"POST", "/v1/internal/drift", driftBody},
			{"GET", "/v1/internal/drift/stacks", ""},
		} {
			rec := do(t, driftRouter(&fakeDriftIngest{}), route.method, route.path, route.body, "Authorization", header)
			if rec.Code != want {
				t.Errorf("%s %s with %q = %d, want %d: %s", route.method, route.path, header, rec.Code, want, rec.Body)
			}
		}
	}
	// A browser user (dev auth) never reaches the machine routes.
	if rec := do(t, driftRouter(&fakeDriftIngest{}), "POST", "/v1/internal/drift", driftBody); rec.Code != 401 {
		t.Fatalf("anonymous = %d", rec.Code)
	}
}

func TestDriftIngestBody(t *testing.T) {
	svc := &fakeDriftIngest{}
	h := driftRouter(svc)
	hdr := []string{"Authorization", "Bearer " + testInternalToken}

	rec := do(t, h, "POST", "/v1/internal/drift", driftBody, hdr...)
	if rec.Code != 200 {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Accepted bool    `json:"accepted"`
		ReportID string  `json:"report_id"`
		Result   string  `json:"result"`
		Status   *string `json:"status"`
		Replayed bool    `json:"replayed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.Accepted || got.ReportID != driftReportID.String() ||
		got.Result != "drifted" || got.Status == nil || *got.Status != "open" || got.Replayed {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	if len(svc.reports) != 1 || svc.reports[0].GitHub.RunAttempt != 2 || svc.reports[0].Summary.Drifted != 1 {
		t.Fatalf("service saw %+v", svc.reports)
	}

	// Unknown fields and a result that disagrees with the exit code are refused.
	if rec := do(t, h, "POST", "/v1/internal/drift", strings.Replace(driftBody, `"result"`, `"severity": "high", "result"`, 1), hdr...); rec.Code != 400 {
		t.Errorf("unknown field = %d", rec.Code)
	}
	bad := strings.Replace(driftBody, `"result": "drifted"`, `"result": "clean"`, 1)
	if rec := do(t, h, "POST", "/v1/internal/drift", bad, hdr...); rec.Code != 422 || !strings.Contains(rec.Body.String(), `"/result"`) {
		t.Errorf("mismatched result = %d %s", rec.Code, rec.Body)
	}

	rec = do(t, h, "GET", "/v1/internal/drift/stacks?limit=10", "", hdr...)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"path":"environments/development/payments/orders"`) ||
		!strings.Contains(rec.Body.String(), `"stack_id":"`+lockStack.String()+`"`) || svc.limits[0] != 10 {
		t.Fatalf("stacks: %d %s limits=%v", rec.Code, rec.Body, svc.limits)
	}
	if rec := do(t, h, "GET", "/v1/internal/drift/stacks?limit=0", "", hdr...); rec.Code != 400 {
		t.Errorf("limit=0 = %d", rec.Code)
	}
}

type fakeDriftUser struct {
	by      []identity.Principal
	inputs  []drift.ResolveInput
	listFor []uuid.UUID
}

func driftEntry() drift.Entry {
	open := drift.StatusOpen
	return drift.Entry{ID: driftReportID, StackID: lockStack, StackPath: "environments/development/payments/orders",
		Result: drift.ResultDrifted, TerraformExit: 2, Summary: drift.Summary{Drifted: 1, Updated: 1},
		Changed: json.RawMessage(`[{"address":"a.b","type":"a","actions":["update"]}]`), PlanHash: "ab", DetectedAt: driftAt, Status: &open}
}

func (f *fakeDriftUser) List(_ context.Context, _ identity.Principal, id uuid.UUID, _ int) ([]drift.Entry, error) {
	f.listFor = append(f.listFor, id)
	if id != lockStack {
		return nil, apperr.NotFound("stack %s not found", id)
	}
	return []drift.Entry{driftEntry()}, nil
}

func (f *fakeDriftUser) Resolve(_ context.Context, by identity.Principal, id uuid.UUID, in drift.ResolveInput) (drift.Resolved, error) {
	f.by, f.inputs = append(f.by, by), append(f.inputs, in)
	if id != driftReportID {
		return drift.Resolved{}, apperr.NotFound("drift report %s not found", id)
	}
	if errs := drift.ValidateResolve(in); len(errs) > 0 {
		return drift.Resolved{}, apperr.Invalid("validation_failed", "resolve request is invalid", errs...)
	}
	e := driftEntry()
	st, _ := drift.StatusFor(in.Action)
	e.Status = &st
	res := drift.Resolved{Entry: e}
	if in.Action == drift.ActionReconcile {
		res.Request = &drift.Reconciled{RequestID: lockHolder, ShortID: "req-7k3p9x", Status: "draft"}
	}
	return res, nil
}

func TestDriftUserRoutes(t *testing.T) {
	svc := &fakeDriftUser{}
	h := newTestRouter(nil, okPinger{}, NewDriftHandler(svc))

	rec := do(t, h, "GET", "/v1/stacks/"+lockStack.String()+"/drift?limit=5", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Data []struct {
			ID      string          `json:"id"`
			Result  string          `json:"result"`
			Status  *string         `json:"status"`
			Changed json.RawMessage `json:"changed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 1 || list.Data[0].Result != "drifted" ||
		*list.Data[0].Status != "open" || !strings.Contains(string(list.Data[0].Changed), "a.b") {
		t.Fatalf("list body = %s (%v)", rec.Body, err)
	}
	if rec := do(t, h, "GET", "/v1/stacks/not-a-uuid/drift", ""); rec.Code != 404 {
		t.Errorf("bad id = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/v1/stacks/"+uuid.NewString()+"/drift", ""); rec.Code != 404 {
		t.Errorf("unknown stack = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/v1/stacks/"+lockStack.String()+"/drift?limit=abc", ""); rec.Code != 400 {
		t.Errorf("bad limit = %d", rec.Code)
	}

	url := "/v1/drift/" + driftReportID.String() + "/resolve"
	rec = do(t, h, "POST", url, `{"action":"reconcile"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"reconciled"`) ||
		!strings.Contains(rec.Body.String(), `"short_id":"req-7k3p9x"`) ||
		!strings.Contains(rec.Body.String(), `"link":"/v1/requests/`+lockHolder.String()+`"`) {
		t.Fatalf("reconcile: %d %s", rec.Code, rec.Body)
	}
	if svc.by[0].UserID != devPrincipal.UserID {
		t.Fatalf("resolved by %+v", svc.by[0])
	}
	rec = do(t, h, "POST", url, `{"action":"codify","reason":"intended console change"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"codified"`) ||
		!strings.Contains(rec.Body.String(), `"note":"Codify is recorded only`) || strings.Contains(rec.Body.String(), `"short_id"`) {
		t.Fatalf("codify: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", url, `{"action":"ignore"}`); rec.Code != 422 || !strings.Contains(rec.Body.String(), `"/reason"`) {
		t.Errorf("ignore without a reason = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", url, `{"action":"reconcile","extra":1}`); rec.Code != 400 {
		t.Errorf("unknown field = %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/v1/drift/"+uuid.NewString()+"/resolve", `{"action":"reconcile"}`); rec.Code != 404 {
		t.Errorf("unknown report = %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/v1/drift/nope/resolve", `{"action":"reconcile"}`); rec.Code != 404 {
		t.Errorf("bad report id = %d", rec.Code)
	}
}

func TestDriftRoutePoliciesFollowTheMatrix(t *testing.T) {
	p := DefaultPolicies()
	if got := p["GET /v1/stacks/{id}/drift"]; got.Scope != ScopeAnyTeam || got.Action != "read" {
		t.Errorf("history policy = %+v", got)
	}
	if got := p["POST /v1/drift/{id}/resolve"]; got.Scope != ScopeAnyTeam || got.Action != "drift.reconcile" {
		t.Errorf("resolve policy = %+v", got)
	}
}
