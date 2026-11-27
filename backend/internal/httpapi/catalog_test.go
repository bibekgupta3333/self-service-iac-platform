package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

type fakeCatalog struct {
	versions []catalog.ModuleVersion
	indexed  []identity.Principal
}

func (f *fakeCatalog) ListModules(context.Context, catalog.ModuleFilter) ([]catalog.ModuleSummary, error) {
	latest := f.versions[0]
	return []catalog.ModuleSummary{{Module: catalog.Module{Name: "sqs", Status: "active", Owners: []string{"platform"}}, Latest: &latest}}, nil
}

func (f *fakeCatalog) GetModule(_ context.Context, name string) (catalog.ModuleSummary, error) {
	if name != "sqs" {
		return catalog.ModuleSummary{}, apperr.NotFound("module %q not found", name)
	}
	latest := f.versions[0]
	return catalog.ModuleSummary{Module: catalog.Module{Name: "sqs"}, Latest: &latest}, nil
}

func (f *fakeCatalog) ListVersions(_ context.Context, name string) ([]catalog.ModuleVersion, error) {
	return f.versions, nil
}

func (f *fakeCatalog) GetVersion(_ context.Context, name, version string) (catalog.ModuleVersion, error) {
	for _, v := range f.versions {
		if v.Version.String() == version {
			return v, nil
		}
	}
	return catalog.ModuleVersion{}, apperr.NotFound("module %s has no version %q", name, version)
}

// UpgradePath runs the real path logic over the fake's versions.
func (f *fakeCatalog) UpgradePath(_ context.Context, name, from, to string) (catalog.UpgradePath, error) {
	if name != "sqs" {
		return catalog.UpgradePath{}, apperr.NotFound("module %q not found", name)
	}
	fromV, err := catalog.ParseSemVer(from)
	if err != nil {
		return catalog.UpgradePath{}, apperr.Invalid("validation_failed", "bad from")
	}
	toV := f.versions[0].Version
	if to != "" && to != catalog.LatestTarget {
		if toV, err = catalog.ParseSemVer(to); err != nil {
			return catalog.UpgradePath{}, apperr.Invalid("validation_failed", "bad to")
		}
	}
	return catalog.BuildUpgradePath(name, fromV, toV, f.versions)
}

func (f *fakeCatalog) Index(_ context.Context, by identity.Principal, cmd catalog.IndexCommand) (catalog.ModuleVersion, bool, error) {
	if cmd.Module == "" {
		return catalog.ModuleVersion{}, false, apperr.Invalid("validation_failed", "module release is invalid",
			apperr.FieldError{Pointer: "/module", Code: "pattern", Message: "bad"})
	}
	if cmd.Module == "boom" {
		return catalog.ModuleVersion{}, false, errors.New("database exploded")
	}
	f.indexed = append(f.indexed, by)
	return f.versions[0], len(f.indexed) == 1, nil
}

func (f *fakeCatalog) SourceAddress(v catalog.ModuleVersion) string {
	return "git::https://example.test//modules/" + v.ModuleName + "?ref=" + v.GitTag
}

type okPinger struct{ err error }

func (p okPinger) Ping(context.Context) error { return p.err }

var devPrincipal = identity.Principal{UserID: uuid.MustParse("4b1f2a0e-8a71-4b0c-9c1e-2f1d5e6a7b80"), Login: "dev", Type: identity.ActorUser, PlatformAdmin: true}

var releaseBot = identity.Principal{Login: "internal-token", Type: identity.ActorGitHubActions}

const testInternalToken = "test-internal-token-0123456789abcdef"

func newTestRouter(cat CatalogService, ready Pinger, extra ...RouteRegistrar) http.Handler {
	return NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{},
		Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:     DevAuthenticator{Principal: devPrincipal},
		Internal: NewTokenAuthenticator(testInternalToken, releaseBot),
		Ready:    ready,
		Catalog:  NewCatalogHandler(cat),
		Public:   extra,
	})
}

func do(t *testing.T, h http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sqsVersion() catalog.ModuleVersion {
	return catalog.ModuleVersion{
		ModuleName: "sqs", Version: catalog.SemVer{Major: 1, Minor: 3}, GitTag: "modules/sqs/v1.3.0",
		GitSHA: strings.Repeat("9c", 20), Status: "active", InputsSchema: json.RawMessage(`{"type":"object"}`),
		Outputs: []catalog.Output{{Name: "queue_arn"}},
	}
}

func TestCatalogHandlers(t *testing.T) {
	cat := &fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}
	h := newTestRouter(cat, okPinger{})

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCT     string
		check      func(t *testing.T, body map[string]any)
	}{
		{name: "list modules", method: "GET", path: "/v1/catalog/modules", wantStatus: 200, wantCT: "application/json; charset=utf-8",
			check: func(t *testing.T, b map[string]any) {
				data := b["data"].([]any)
				if len(data) != 1 || data[0].(map[string]any)["latest_version"] != "1.3.0" || b["next_cursor"] != nil {
					t.Fatalf("body = %v", b)
				}
			}},
		{name: "get module", method: "GET", path: "/v1/catalog/modules/sqs", wantStatus: 200,
			check: func(t *testing.T, b map[string]any) {
				latest := b["latest"].(map[string]any)
				if latest["source"] != "git::https://example.test//modules/sqs?ref=modules/sqs/v1.3.0" {
					t.Fatalf("latest = %v", latest)
				}
			}},
		{name: "unknown module", method: "GET", path: "/v1/catalog/modules/nope", wantStatus: 404, wantCT: "application/problem+json",
			check: func(t *testing.T, b map[string]any) {
				if b["code"] != "not_found" || b["type"] != ErrorTypeBase+"not_found" || b["instance"] != "/v1/catalog/modules/nope" || b["request_id"] == "" {
					t.Fatalf("problem = %v", b)
				}
			}},
		{name: "list versions", method: "GET", path: "/v1/catalog/modules/sqs/versions", wantStatus: 200,
			check: func(t *testing.T, b map[string]any) {
				if v := b["data"].([]any)[0].(map[string]any); v["version"] != "1.3.0" || v["git_tag"] != "modules/sqs/v1.3.0" {
					t.Fatalf("versions = %v", b)
				}
			}},
		{name: "get version", method: "GET", path: "/v1/catalog/modules/sqs/versions/1.3.0", wantStatus: 200,
			check: func(t *testing.T, b map[string]any) {
				if b["git_sha"] != strings.Repeat("9c", 20) || b["inputs_schema"] == nil || b["cost_hints"] != nil {
					t.Fatalf("version = %v", b)
				}
			}},
		{name: "index created", method: "POST", path: "/v1/internal/catalog/index", body: `{"module":"sqs","version":"1.3.0"}`, wantStatus: 201},
		{name: "index replay", method: "POST", path: "/v1/internal/catalog/index", body: `{"module":"sqs","version":"1.3.0"}`, wantStatus: 200},
		{name: "index invalid", method: "POST", path: "/v1/internal/catalog/index", body: `{"module":""}`, wantStatus: 422,
			check: func(t *testing.T, b map[string]any) {
				errs := b["errors"].([]any)
				if b["code"] != "validation_failed" || errs[0].(map[string]any)["pointer"] != "/module" {
					t.Fatalf("problem = %v", b)
				}
			}},
		{name: "index unknown field", method: "POST", path: "/v1/internal/catalog/index", body: `{"modul":"sqs"}`, wantStatus: 400},
		{name: "index malformed", method: "POST", path: "/v1/internal/catalog/index", body: `{`, wantStatus: 400},
		{name: "internal error hides detail", method: "POST", path: "/v1/internal/catalog/index", body: `{"module":"boom"}`, wantStatus: 500,
			check: func(t *testing.T, b map[string]any) {
				if b["code"] != "internal" || b["detail"] != nil {
					t.Fatalf("problem leaks detail: %v", b)
				}
			}},
		{name: "unknown route", method: "GET", path: "/v1/nope", wantStatus: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.method, tt.path, tt.body, "Authorization", "Bearer "+testInternalToken)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantCT != "" && rec.Header().Get("Content-Type") != tt.wantCT {
				t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
			}
			if tt.check != nil {
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode: %v (%s)", err, rec.Body)
				}
				tt.check(t, body)
			}
		})
	}
	if len(cat.indexed) != 2 || cat.indexed[0] != releaseBot {
		t.Fatalf("index should receive the internal principal, not the dev user: %+v", cat.indexed)
	}
}

func TestUpgradePathHandler(t *testing.T) {
	mk := func(v, notes string) catalog.ModuleVersion {
		mv := sqsVersion()
		mv.Version, _ = catalog.ParseSemVer(v)
		mv.GitTag, mv.UpgradeNotes = "modules/sqs/v"+v, notes
		return mv
	}
	cat := &fakeCatalog{versions: []catalog.ModuleVersion{mk("2.0.0", "Rename dlq."), mk("1.3.0", ""), mk("1.3.1", ""), mk("1.2.0", "")}}
	h := newTestRouter(cat, okPinger{})

	rec := do(t, h, "GET", "/v1/catalog/modules/sqs/upgrade-path?from=1.2.0", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		From, To, Change string
		UpToDate         bool    `json:"up_to_date"`
		MajorsCrossed    int     `json:"majors_crossed"`
		Next             *string `json:"next"`
		Targets          struct{ Patch, Minor, Major *string }
		Steps            []struct {
			Version, Change string
			Breaking        bool
			UpgradeNotes    string `json:"upgrade_notes"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.From != "1.2.0" || body.To != "2.0.0" || body.Change != "major" || body.UpToDate || body.MajorsCrossed != 1 ||
		body.Next == nil || *body.Next != "2.0.0" || body.Targets.Patch != nil || *body.Targets.Minor != "1.3.1" ||
		len(body.Steps) != 3 || body.Steps[0].Change != "minor" || body.Steps[1].Change != "patch" ||
		!body.Steps[2].Breaking || body.Steps[2].UpgradeNotes != "Rename dlq." {
		t.Fatalf("body = %s", rec.Body)
	}

	rec = do(t, h, "GET", "/v1/catalog/modules/sqs/upgrade-path?from=2.0.0&to=latest", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"up_to_date":true`) || !strings.Contains(rec.Body.String(), `"steps":[]`) ||
		!strings.Contains(rec.Body.String(), `"next":null`) {
		t.Fatalf("up to date: %d %s", rec.Code, rec.Body)
	}
	for path, want := range map[string]int{
		"/v1/catalog/modules/sqs/upgrade-path":             400,
		"/v1/catalog/modules/sqs/upgrade-path?from=1.x":    422,
		"/v1/catalog/modules/nope/upgrade-path?from=1.0.0": 404,
	} {
		if rec := do(t, h, "GET", path, ""); rec.Code != want {
			t.Errorf("%s: status %d, want %d (%s)", path, rec.Code, want, rec.Body)
		}
	}
}

// The release tool sends upgrade_notes; the strict decoder must accept it.
func TestIndexAcceptsUpgradeNotes(t *testing.T) {
	cat := &fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}
	h := newTestRouter(cat, okPinger{})
	rec := do(t, h, "POST", "/v1/internal/catalog/index", `{"module":"sqs","version":"1.3.0","upgrade_notes":"Rename x."}`,
		"Authorization", "Bearer "+testInternalToken)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestHealthAndRequestID(t *testing.T) {
	h := newTestRouter(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}, okPinger{})
	if rec := do(t, h, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("readyz = %d", rec.Code)
	}
	rec := do(t, h, "GET", "/healthz", "", "X-Request-Id", "abc-123")
	if rec.Header().Get("X-Request-Id") != "abc-123" {
		t.Fatalf("request id not echoed: %q", rec.Header().Get("X-Request-Id"))
	}
	rec = do(t, h, "GET", "/healthz", "", "X-Request-Id", "bad id with spaces")
	if got := rec.Header().Get("X-Request-Id"); got == "" || strings.Contains(got, " ") {
		t.Fatalf("malformed request id should be replaced, got %q", got)
	}

	down := newTestRouter(&fakeCatalog{}, okPinger{err: errors.New("no db")})
	if rec := do(t, down, "GET", "/readyz", ""); rec.Code != 503 {
		t.Fatalf("readyz with db down = %d", rec.Code)
	}
}

type failingAuth struct{}

func (failingAuth) Authenticate(*http.Request) (identity.Principal, error) {
	return identity.Principal{}, errors.New("missing session")
}

func TestUnauthenticated(t *testing.T) {
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: failingAuth{},
		Ready: okPinger{}, Catalog: NewCatalogHandler(&fakeCatalog{})})
	rec := do(t, h, "GET", "/v1/catalog/modules", "")
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"code":"unauthenticated"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatal("health endpoints must not require auth")
	}
}
