package httpapi

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
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
	denied := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Ready: okPinger{}, Catalog: NewCatalogHandler(cat)})
	if rec := do(t, denied, "POST", "/v1/internal/catalog/index", body, "Authorization", "Bearer "+testInternalToken); rec.Code != 401 {
		t.Fatalf("index with no internal authenticator = %d, want 401", rec.Code)
	}
}

func TestUnauthenticatedBodyIsGeneric(t *testing.T) {
	h := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: failingAuth{},
		Ready: okPinger{}, Catalog: NewCatalogHandler(&fakeCatalog{})})
	rec := do(t, h, "GET", "/v1/catalog/modules", "")
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "missing session") ||
		!strings.Contains(rec.Body.String(), `"detail":"authentication required"`) {
		t.Fatalf("401 body must not echo the authenticator error: %d %s", rec.Code, rec.Body)
	}
}
