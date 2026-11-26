package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

// A browser whose session expired (or was revoked, or never existed) can still log out: the
// cookie is cleared and the answer is success, not a CSRF error.
func TestLogoutWithADeadSessionClearsTheCookie(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	c := f.signIn(t)
	f.clock.t = f.clock.t.Add(2 * time.Hour) // past the idle timeout

	rec := f.req("POST", "/v1/auth/logout", "", []*http.Cookie{c}) // no CSRF token: it could not be fetched
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout with an expired session = %d %s", rec.Code, rec.Body.String())
	}
	if cl := cookieOf(rec, session.CookieName); cl == nil || cl.MaxAge >= 0 || cl.Value != "" {
		t.Fatalf("cookie not cleared: %+v", cl)
	}
	before := len(f.audit.events)

	for name, cookies := range map[string][]*http.Cookie{
		"no cookie":      nil,
		"unknown id":     {{Name: session.CookieName, Value: "nope"}},
		"already logged": {c},
	} {
		got := f.req("POST", "/v1/auth/logout", "", cookies)
		if got.Code != http.StatusNoContent {
			t.Errorf("%s: %d", name, got.Code)
		}
	}
	if len(f.audit.events) != before {
		t.Fatal("a logout that revokes nothing is not audited as a logout")
	}
}

func TestLogoutOfALiveSessionStillNeedsTheCSRFToken(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	c := f.signIn(t)
	if rec := f.req("POST", "/v1/auth/logout", "", []*http.Cookie{c}); rec.Code != http.StatusForbidden {
		t.Fatalf("without a token = %d", rec.Code)
	}
	if rec := f.req("GET", "/v1/me", "", []*http.Cookie{c}); rec.Code != http.StatusOK {
		t.Fatal("a rejected logout must not end the session")
	}
}
