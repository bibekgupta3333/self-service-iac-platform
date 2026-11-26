package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Over https the cookies carry the __Host- prefix, so the browser enforces Secure, Path=/
// and no Domain, and the server ignores an unprefixed look-alike set by a sibling origin.
func TestSecureCookiesUseTheHostPrefix(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	rec := f.req("GET", "/v1/auth/login", "", nil)
	var names []string
	for _, c := range rec.Result().Cookies() {
		names = append(names, c.Name)
		if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Errorf("cookie %+v does not satisfy __Host- rules", c)
		}
	}
	if len(names) != 1 || names[0] != "__Host-iacp_oauth_state" {
		t.Fatalf("login cookies = %v", names)
	}
	session := f.signIn(t)
	if session.Name != "__Host-iacp_session" {
		t.Fatalf("session cookie = %s", session.Name)
	}
	plain := &http.Cookie{Name: "iacp_session", Value: session.Value}
	if got := f.req("GET", "/v1/me", "", []*http.Cookie{plain}); got.Code != http.StatusUnauthorized {
		t.Fatalf("an unprefixed cookie must not authenticate over https: %d", got.Code)
	}
}

// Plain-http loopback cannot use the prefix (browsers require Secure for it).
func TestLoopbackCookiesAreUnprefixed(t *testing.T) {
	f := newFlow(t, false, rbac.RoleDeveloper)
	if c := f.signIn(t); c.Name != "iacp_session" {
		t.Fatalf("loopback session cookie = %s", c.Name)
	}
}
