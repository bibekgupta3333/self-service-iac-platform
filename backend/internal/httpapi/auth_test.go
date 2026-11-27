package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

func TestTokenAuthenticator(t *testing.T) {
	a := NewTokenAuthenticator(testInternalToken, releaseBot)
	tests := []struct {
		name   string
		header string
		ok     bool
	}{
		{name: "valid", header: "Bearer " + testInternalToken, ok: true},
		{name: "missing", header: ""},
		{name: "wrong scheme", header: "Basic " + testInternalToken},
		{name: "wrong token", header: "Bearer " + strings.Repeat("x", len(testInternalToken))},
		{name: "prefix of token", header: "Bearer " + testInternalToken[:10]},
		{name: "empty bearer", header: "Bearer "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/internal/catalog/index", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			p, err := a.Authenticate(r)
			if (err == nil) != tt.ok || (tt.ok && p != releaseBot) {
				t.Fatalf("Authenticate = %+v, %v", p, err)
			}
		})
	}

	disabled := NewTokenAuthenticator("", releaseBot)
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer ")
	if _, err := disabled.Authenticate(r); err == nil {
		t.Fatal("an unconfigured token authenticator must reject every request")
	}
}

func workflowClaims(file, ref, event string) auth.Claims {
	return auth.Claims{Repository: "acme/iacp", Ref: ref, RunID: "11873399021", RunAttempt: "2", EventName: event,
		JobWorkflowRef: "acme/iacp/.github/workflows/" + file + "@" + ref}
}

// fakeTokenVerifier maps fixed token strings to verified workflow identities:
// good/apply = apply.yml on main, release = module-release.yml on main, prvalidate =
// pr-validate.yml on a PR merge ref; "forbidden.jwt.token" is a global allow-list rejection.
type fakeTokenVerifier struct{}

func (fakeTokenVerifier) Verify(_ context.Context, raw string) (auth.Claims, error) {
	switch raw {
	case "good.jwt.token", "apply.jwt.token":
		return workflowClaims("apply.yml", "refs/heads/main", "push"), nil
	case "release.jwt.token":
		return workflowClaims("module-release.yml", "refs/heads/main", "push"), nil
	case "reaper.jwt.token":
		return workflowClaims("ttl-reaper.yml", "refs/heads/main", "schedule"), nil
	case "prvalidate.jwt.token":
		return workflowClaims("pr-validate.yml", "refs/pull/812/merge", "pull_request"), nil
	case "surgery.jwt.token":
		return workflowClaims("state-surgery.yml", "refs/heads/main", "workflow_dispatch"), nil
	case "applydispatch.jwt.token":
		return workflowClaims("apply.yml", "refs/heads/main", "workflow_dispatch"), nil
	case "applyprod.jwt.token", "applystaging.jwt.token":
		c := workflowClaims("apply.yml", "refs/heads/main", "push")
		c.Environment = map[string]string{"applyprod.jwt.token": "production", "applystaging.jwt.token": "staging"}[raw]
		return c, nil
	case "badrun.jwt.token":
		return auth.Claims{RunID: "x", RunAttempt: "1"}, nil
	case "forbidden.jwt.token":
		return auth.Claims{}, &apperr.Error{Kind: apperr.KindForbidden, Code: "workflow_not_allowed", Message: "ref not allowed"}
	}
	return auth.Claims{}, fmt.Errorf("%w: bad signature", auth.ErrInvalidToken)
}

func TestOIDCAuthenticator(t *testing.T) {
	a := NewOIDCAuthenticator(fakeTokenVerifier{})
	tests := []struct {
		name      string
		header    string
		ok        bool
		forbidden bool
	}{
		{name: "valid", header: "Bearer good.jwt.token", ok: true},
		{name: "invalid", header: "Bearer bad.jwt.token"},
		{name: "not a jwt", header: "Bearer " + testInternalToken},
		{name: "missing", header: ""},
		{name: "bad run id claim", header: "Bearer badrun.jwt.token"},
		{name: "workflow not allowed", header: "Bearer forbidden.jwt.token", forbidden: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/runs", nil)
			r.Header.Set("Authorization", tt.header)
			p, err := a.Authenticate(r)
			if tt.ok != (err == nil) || tt.forbidden != apperr.IsKind(err, apperr.KindForbidden) {
				t.Fatalf("Authenticate = %+v, %v", p, err)
			}
			if tt.ok && (p.Type != identity.ActorGitHubActions || p.Login != "acme/iacp/.github/workflows/apply.yml@refs/heads/main" ||
				p.Workflow == nil || p.Workflow.RunID != 11873399021 || p.Workflow.RunAttempt != 2 || p.Workflow.Ref != "refs/heads/main") {
				t.Fatalf("principal = %+v (%+v)", p, p.Workflow)
			}
		})
	}
}

func TestOIDCOrTokenOnInternalRoutes(t *testing.T) {
	cat := &fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal: AnyAuthenticator{NewOIDCAuthenticator(fakeTokenVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:    okPinger{}, Catalog: NewCatalogHandler(cat)})
	body := `{"module":"sqs","version":"1.3.0"}`
	tests := []struct {
		header string
		want   int
	}{
		{"Bearer release.jwt.token", 201},
		{"Bearer " + testInternalToken, 200},
		{"Bearer apply.jwt.token", 403},      // not module-release.yml
		{"Bearer prvalidate.jwt.token", 403}, // PR-controlled workflow
		{"Bearer bad.jwt.token", 401},
		{"Bearer forbidden.jwt.token", 403},
		{"", 401},
	}
	for _, tt := range tests {
		rec := do(t, h, "POST", "/v1/internal/catalog/index", body, "Authorization", tt.header)
		if rec.Code != tt.want {
			t.Errorf("%q: status = %d, want %d: %s", tt.header, rec.Code, tt.want, rec.Body)
		}
		if tt.want == 403 && !strings.Contains(rec.Body.String(), `"code":"workflow_not_allowed"`) {
			t.Errorf("403 body = %s", rec.Body)
		}
	}
	if len(cat.indexed) != 2 || cat.indexed[0].Workflow == nil || cat.indexed[1] != releaseBot {
		t.Fatalf("indexed by = %+v", cat.indexed)
	}
}

func TestInternalRoutesUseTheInternalAuthenticator(t *testing.T) {
	cat := &fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}
	body := `{"module":"sqs","version":"1.3.0"}`

	// The dev user authenticator must not open /v1/internal.
	h := newTestRouter(cat, okPinger{})
	if rec := do(t, h, "POST", "/v1/internal/catalog/index", body); rec.Code != 401 {
		t.Fatalf("index without token = %d, want 401", rec.Code)
	}
	if rec := do(t, h, "POST", "/v1/internal/catalog/index", body, "Authorization", "Bearer wrong"); rec.Code != 401 {
		t.Fatalf("index with wrong token = %d, want 401", rec.Code)
	}
	if rec := do(t, h, "POST", "/v1/internal/catalog/index", body, "Authorization", "Bearer "+testInternalToken); rec.Code != 201 {
		t.Fatalf("index with token = %d, want 201", rec.Code)
	}
	// The internal token does not grant user routes either way: they use the user authenticator.
	if rec := do(t, h, "GET", "/v1/catalog/modules", ""); rec.Code != 200 {
		t.Fatalf("user route = %d", rec.Code)
	}

	// No internal authenticator configured: deny.
	denied := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Ready: okPinger{}, Catalog: NewCatalogHandler(cat)})
	if rec := do(t, denied, "POST", "/v1/internal/catalog/index", body, "Authorization", "Bearer "+testInternalToken); rec.Code != 401 {
		t.Fatalf("index with no internal authenticator = %d, want 401", rec.Code)
	}
}

func TestMethodNotAllowedIsProblemWithAllow(t *testing.T) {
	h := newTestRouter(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}, okPinger{}, NewRequestHandler(&fakeRequests{}, allowAllAccess{}))
	tests := []struct {
		method, path, allow string
	}{
		{"PUT", "/v1/requests", "GET, POST"},
		{"DELETE", "/v1/catalog/modules", "GET"},
		{"GET", "/v1/internal/catalog/index", "POST"},
		{"POST", "/healthz", "GET"},
	}
	for _, tt := range tests {
		rec := do(t, h, tt.method, tt.path, "", "Authorization", "Bearer "+testInternalToken)
		if rec.Code != 405 || rec.Header().Get("Allow") != tt.allow ||
			rec.Header().Get("Content-Type") != "application/problem+json" ||
			!strings.Contains(rec.Body.String(), `"code":"method_not_allowed"`) {
			t.Errorf("%s %s = %d Allow=%q %s", tt.method, tt.path, rec.Code, rec.Header().Get("Allow"), rec.Body)
		}
	}
}

func TestUnauthenticatedBodyIsGeneric(t *testing.T) {
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: failingAuth{},
		Ready: okPinger{}, Catalog: NewCatalogHandler(&fakeCatalog{})})
	rec := do(t, h, "GET", "/v1/catalog/modules", "")
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "missing session") ||
		!strings.Contains(rec.Body.String(), `"detail":"authentication required"`) {
		t.Fatalf("401 body must not echo the authenticator error: %d %s", rec.Code, rec.Body)
	}
}
