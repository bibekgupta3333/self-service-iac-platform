package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

func (f *flow) req(method, path, body string, cookies []*http.Cookie, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// signIn runs login + callback and returns the session cookie.
func (f *flow) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	rec := f.req("GET", "/v1/auth/login?return_to=/requests", "", nil)
	st := cookieOf(rec, StateCookieName)
	if st == nil {
		t.Fatal("no state cookie")
	}
	cb := f.req("GET", "/v1/auth/callback?code=abc&state="+st.Value, "", []*http.Cookie{st})
	c := cookieOf(cb, session.CookieName)
	if cb.Code != http.StatusFound || c == nil {
		t.Fatalf("callback = %d, cookie %v", cb.Code, c)
	}
	return c
}

func TestLoginRedirectsToGitHubAndBindsState(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	rec := f.req("GET", "/v1/auth/login?return_to=/catalog", "", nil)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://github.com/login/oauth/authorize") {
		t.Fatalf("login = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if f.login.begun[0] != "/catalog" {
		t.Fatalf("return_to = %v", f.login.begun)
	}
	c := cookieOf(rec, StateCookieName)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 600 || c.Value == "" {
		t.Fatalf("state cookie = %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("login redirect must not be cached")
	}
}

func TestSessionCookieFlags(t *testing.T) {
	for _, secure := range []bool{true, false} {
		f := newFlow(t, secure, rbac.RoleDeveloper)
		c := f.signIn(t)
		if !c.HttpOnly || c.Secure != secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" ||
			c.MaxAge != 8*3600 || !session.ValidID(c.Value) {
			t.Fatalf("secure=%v cookie = %+v", secure, c)
		}
	}
}

func TestCallbackFailuresRedirectWithACodeAndSetNoSession(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	st := cookieOf(f.req("GET", "/v1/auth/login", "", nil), StateCookieName)
	cases := map[string]struct {
		path    string
		cookies []*http.Cookie
		fail    error
		want    string
	}{
		"no state cookie":   {path: "/v1/auth/callback?code=a&state=" + st.Value, want: "invalid_state"},
		"wrong state":       {path: "/v1/auth/callback?code=a&state=zzz", cookies: []*http.Cookie{st}, want: "invalid_state"},
		"user denied":       {path: "/v1/auth/callback?error=access_denied&state=" + st.Value, cookies: []*http.Cookie{st}, want: "invalid_state"},
		"not an org member": {path: "/v1/auth/callback?code=a&state=" + st.Value, cookies: []*http.Cookie{st}, fail: oauth.ErrNotOrgMember, want: "not_org_member"},
	}
	for name, tc := range cases {
		f.login.fail = tc.fail
		rec := f.req("GET", tc.path, "", tc.cookies)
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusFound || !strings.Contains(loc, "login_error="+tc.want) || !strings.HasPrefix(loc, "/") {
			t.Errorf("%s: %d %q", name, rec.Code, loc)
		}
		if cookieOf(rec, session.CookieName) != nil {
			t.Errorf("%s: a failed login must not set a session", name)
		}
		if c := cookieOf(rec, StateCookieName); c == nil || c.MaxAge >= 0 {
			t.Errorf("%s: the state cookie must be cleared", name)
		}
	}
}

func TestFixationOldSessionIsRevokedAndReplaced(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	old := f.signIn(t)
	st := cookieOf(f.req("GET", "/v1/auth/login", "", nil), StateCookieName)
	cb := f.req("GET", "/v1/auth/callback?code=abc&state="+st.Value, "", []*http.Cookie{st, old})
	fresh := cookieOf(cb, session.CookieName)
	if fresh == nil || fresh.Value == old.Value {
		t.Fatal("a login must issue a new session id")
	}
	if rec := f.req("GET", "/v1/me", "", []*http.Cookie{old}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the previous session must be dead: %d", rec.Code)
	}
	if rec := f.req("GET", "/v1/me", "", []*http.Cookie{fresh}); rec.Code != http.StatusOK {
		t.Fatalf("the new session works: %d", rec.Code)
	}
}

func TestMe(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	c := f.signIn(t)
	rec := f.req("GET", "/v1/me", "", []*http.Cookie{c})
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("me = %d, cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got meDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GitHubLogin != "priya" || got.AuthMode != "github" || len(got.Teams) != 1 || got.Teams[0].Role != "developer" ||
		got.CSRFToken != session.CSRFToken(c.Value) {
		t.Fatalf("me = %+v", got)
	}
	if rec := f.req("GET", "/v1/me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /me = %d", rec.Code)
	}
}

func TestCSRFRequiredOnUnsafeMethods(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	c := f.signIn(t)
	tok := session.CSRFToken(c.Value)
	body := `{"kind":"create"}`
	key := []string{"Idempotency-Key", "0123456789abcdef0123"}

	for name, hdr := range map[string][]string{
		"missing":       key,
		"wrong":         append([]string{"X-CSRF-Token", "nope"}, key...),
		"another token": append([]string{"X-CSRF-Token", session.CSRFToken(strings.Repeat("A", 43))}, key...),
		"session id":    append([]string{"X-CSRF-Token", c.Value}, key...),
	} {
		rec := f.req("POST", "/v1/requests", body, []*http.Cookie{c}, hdr...)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "csrf_token_invalid") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(f.reqs.cmds) != 0 {
		t.Fatal("a rejected CSRF check must never reach the handler")
	}
	rec := f.req("POST", "/v1/requests", body, []*http.Cookie{c}, append([]string{"X-CSRF-Token", tok}, key...)...)
	if rec.Code == http.StatusForbidden || len(f.reqs.cmds) != 1 {
		t.Fatalf("valid token: %d %s", rec.Code, rec.Body.String())
	}
	// Reads never need the token.
	if rec := f.req("GET", "/v1/catalog/modules", "", []*http.Cookie{c}); rec.Code != http.StatusOK {
		t.Fatalf("GET without token = %d", rec.Code)
	}
	// Without a session, a token alone is a 401, not a 403.
	if rec := f.req("POST", "/v1/requests", body, nil, "X-CSRF-Token", tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token without session = %d", rec.Code)
	}
}

func TestLogoutRevokesAndClears(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	c := f.signIn(t)
	if rec := f.req("POST", "/v1/auth/logout", "", []*http.Cookie{c}); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF = %d", rec.Code)
	}
	rec := f.req("POST", "/v1/auth/logout", "", []*http.Cookie{c}, "X-CSRF-Token", session.CSRFToken(c.Value))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if cl := cookieOf(rec, session.CookieName); cl == nil || cl.MaxAge >= 0 || cl.Value != "" || !cl.HttpOnly {
		t.Fatalf("clear cookie = %+v", cl)
	}
	if rec := f.req("GET", "/v1/me", "", []*http.Cookie{c}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session must not work: %d", rec.Code)
	}
	if n := len(f.audit.events); n != 1 || f.audit.events[0].Action != "auth.logout" {
		t.Fatalf("audit = %+v", f.audit.events)
	}
}

func TestSessionExpiry(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	c := f.signIn(t)
	f.clock.t = f.clock.t.Add(25 * time.Minute)
	if rec := f.req("GET", "/v1/me", "", []*http.Cookie{c}); rec.Code != http.StatusOK {
		t.Fatalf("within idle: %d", rec.Code)
	}
	f.clock.t = f.clock.t.Add(31 * time.Minute)
	if rec := f.req("GET", "/v1/me", "", []*http.Cookie{c}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("idle timeout: %d", rec.Code)
	}
}

func TestForgedAndMalformedCookies(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	for _, v := range []string{"", "x", strings.Repeat("A", 43), strings.Repeat("A", 500)} {
		if rec := f.req("GET", "/v1/me", "", []*http.Cookie{{Name: session.HostPrefix + session.CookieName, Value: v}}); rec.Code != http.StatusUnauthorized {
			t.Errorf("cookie %q = %d", v, rec.Code)
		}
	}
}

func TestRoutePoliciesAndDenialAudit(t *testing.T) {
	cases := []struct {
		name   string
		role   rbac.Role
		method string
		path   string
		want   int
	}{
		{"viewer reads requests", rbac.RoleViewer, "GET", "/v1/requests", 200},
		{"no team role cannot list requests", rbac.RoleNone, "GET", "/v1/requests", 403},
		{"no team role can browse the catalog", rbac.RoleNone, "GET", "/v1/catalog/modules", 200},
		{"developer cannot verify the audit chain", rbac.RoleDeveloper, "GET", "/v1/audit/verify", 403},
		{"maintainer cannot verify the audit chain", rbac.RoleMaintainer, "GET", "/v1/audit/verify", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t, true, tc.role)
			c := f.signIn(t)
			rec := f.req(tc.method, tc.path, "", []*http.Cookie{c})
			if rec.Code != tc.want {
				t.Fatalf("%d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			denied := 0
			for _, e := range f.audit.events {
				if e.Action == "rbac.denied" {
					denied++
					if e.ActorID != f.user.String() || e.SubjectType != "route" || !strings.HasPrefix(e.SubjectID, tc.method+" /v1/") {
						t.Fatalf("denial event = %+v", e)
					}
				}
			}
			if (tc.want == 403) != (denied == 1) {
				t.Fatalf("denial events = %d for status %d", denied, rec.Code)
			}
		})
	}
}

func TestPlatformAdminPassesEveryPolicy(t *testing.T) {
	f := newFlow(t, true, rbac.RoleNone)
	c := f.signIn(t)
	rec := f.store.rows[string(session.HashID(c.Value))]
	rec.PlatformAdmin = true
	f.store.rows[string(session.HashID(c.Value))] = rec
	for _, p := range []string{"/v1/audit/verify", "/v1/requests"} {
		if rec := f.req("GET", p, "", []*http.Cookie{c}); rec.Code != 200 {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
}

// Dev mode must behave exactly as before: no cookie, no CSRF token, everything allowed
// for the fixed local user.
func TestDevModeUnchanged(t *testing.T) {
	h := NewRouter(Deps{
		Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:        DevAuthenticator{Principal: adminDev()},
		Ready:       okPinger{},
		Public:      []RouteRegistrar{NewRequestHandler(&fakeRequests{byHash: map[string]*requests.Request{}}, allowAllAccess{}), NewAuditHandler(&fakeVerifier{})},
		Policies:    DefaultPolicies(),
		Roles:       flowRoles{role: rbac.RoleNone},
		DenialAudit: &flowAudit{},
	})
	for _, p := range []string{"/v1/requests", "/v1/audit/verify"} {
		if rec := do(t, h, "GET", p, ""); rec.Code != 200 {
			t.Errorf("dev GET %s = %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

func adminDev() identity.Principal { p := devPrincipal; p.PlatformAdmin = true; return p }
