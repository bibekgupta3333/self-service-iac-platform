package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

var runID = uuid.MustParse("7a1c2d3e-4f50-4617-8829-3a4b5c6d7e8f")

type fakeRuns struct{ by []identity.Principal }

func (f *fakeRuns) Record(_ context.Context, by identity.Principal, r runs.Report) (runs.Recorded, error) {
	if errs := runs.Validate(r); len(errs) > 0 {
		return runs.Recorded{}, apperr.Invalid("validation_failed", "run report is invalid", errs...)
	}
	if err := runs.CheckProvenance(r, by.Workflow); err != nil {
		return runs.Recorded{}, err
	}
	if *r.FencingToken != 48213 {
		return runs.Recorded{}, apperr.Conflict("stale_fencing_token", "fencing token is not current")
	}
	f.by = append(f.by, by)
	status := "plan_ready"
	return runs.Recorded{RunID: runID, RunStatus: runs.StatusSucceeded, RequestStatus: &status}, nil
}

const runBody = `{
  "pipeline": "validate",
  "stack_path": "environments/development/payments/order-events",
  "request_short_id": "req-7k3p9x",
  "fencing_token": %TOKEN%,
  "github": { "run_id": 11873399021, "run_attempt": 1, "job_id": 33412988710, "head_sha": "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4" },
  "stage": "summary",
  "status": "passed",
  "plan": { "hash": "3f9a1c0b", "counts": { "add": 11 } },
  "policy": { "opa": { "deny": [] } },
  "cost": { "currency": "USD", "monthly_after": 4.10 }
}`

func runReport(token string) string { return strings.Replace(runBody, "%TOKEN%", token, 1) }

// TestRunsEndToEndWithOIDC drives POST /v1/runs through a real Verifier and a JWKS
// served by httptest, as GitHub Actions would call it.
func TestRunsEndToEndWithOIDC(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "gh1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer jwks.Close()

	const repo = "acme/iacp"
	now := time.Now()
	clock := platform.FixedClock{T: now}
	keys, err := auth.NewJWKSCache(jwks.URL, clock, auth.JWKSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewVerifier(auth.Policy{Issuer: auth.GitHubIssuer, Audience: "https://api.iacp.acme.internal", Repository: repo,
		Refs: auth.DefaultRefs, WorkflowRefs: auth.DefaultWorkflowRefs(repo)}, keys, clock, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRuns{}
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal: AnyAuthenticator{NewOIDCAuthenticator(verifier), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:    okPinger{}, PipelineHandlers: []RouteRegistrar{NewRunHandler(fr)}})

	token := func(workflow, ref string, runID string) string {
		c := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: auth.GitHubIssuer, Audience: jwt.ClaimStrings{"https://api.iacp.acme.internal"},
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)), IssuedAt: jwt.NewNumericDate(now)},
			Repository: repo, RepositoryOwner: "acme", Ref: ref, JobWorkflowRef: repo + "/.github/workflows/" + workflow + "@" + ref,
			RunID: runID, RunAttempt: "1", SHA: "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4", EventName: "push"}
		if strings.HasPrefix(ref, "refs/pull/") {
			c.EventName = "pull_request"
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "gh1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + s
	}

	tests := []struct {
		name, auth, body string
		want             int
		code             string
	}{
		{name: "pr-validate run reports its plan", auth: token("pr-validate.yml", "refs/pull/812/merge", "11873399021"), body: runReport("48213"), want: 200},
		{name: "shared token still works", auth: "Bearer " + testInternalToken, body: runReport("48213"), want: 200},
		{name: "no credentials", body: runReport("48213"), want: 401},
		{name: "dev user is not a pipeline", auth: "", body: runReport("48213"), want: 401},
		{name: "report for another run", auth: token("pr-validate.yml", "refs/pull/812/merge", "5"), body: runReport("48213"), want: 403, code: "run_mismatch"},
		{name: "wrong workflow for the pipeline", auth: token("drift.yml", "refs/heads/main", "11873399021"), body: runReport("48213"), want: 403, code: "workflow_not_allowed"},
		{name: "workflow outside the allow-list", auth: token("apply.yml", "refs/pull/812/merge", "11873399021"), body: runReport("48213"), want: 403, code: "workflow_not_allowed"},
		{name: "stale fencing token", auth: token("pr-validate.yml", "refs/pull/812/merge", "11873399021"), body: runReport("1"), want: 409, code: "stale_fencing_token"},
		{name: "unknown field", auth: "Bearer " + testInternalToken, body: `{"pipeline":"validate","bogus":1}`, want: 400},
		{name: "invalid report", auth: "Bearer " + testInternalToken, body: `{"pipeline":"validate"}`, want: 422, code: "validation_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var headers []string
			if tt.auth != "" {
				headers = []string{"Authorization", tt.auth}
			}
			rec := do(t, h, "POST", "/v1/runs", tt.body, headers...)
			if rec.Code != tt.want || (tt.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+tt.code+`"`)) {
				t.Fatalf("status = %d, want %d (%s): %s", rec.Code, tt.want, tt.code, rec.Body)
			}
			if tt.want == 200 {
				var b map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || b["accepted"] != true ||
					b["request_status"] != "plan_ready" || b["run_id"] != runID.String() || b["run_status"] != "succeeded" {
					t.Fatalf("body = %s", rec.Body)
				}
			}
		})
	}
	if len(fr.by) != 2 || fr.by[0].Workflow == nil || fr.by[0].Workflow.RunID != 11873399021 || fr.by[1] != releaseBot {
		t.Fatalf("recorded by = %+v", fr.by)
	}
}
