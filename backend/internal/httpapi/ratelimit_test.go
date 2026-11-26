package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

func TestLoginIsRateLimitedPerAddress(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	f.limitLogin(3)
	for i := 0; i < 3; i++ {
		if rec := f.req("GET", "/v1/auth/login", "", nil); rec.Code != http.StatusFound {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	rec := f.req("GET", "/v1/auth/login", "", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" ||
		!strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("over the limit = %d %s", rec.Code, rec.Body.String())
	}
	if len(f.login.begun) != 3 {
		t.Fatal("a limited request must not start a login")
	}
	// The callback is not limited by the login limiter.
	if rec := f.req("GET", "/v1/auth/callback?code=a&state=b", "", nil); rec.Code != http.StatusFound {
		t.Fatalf("callback = %d", rec.Code)
	}
}

func TestDenialAuditVolumeIsBounded(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	f.limitDenials(2)
	c := f.signIn(t)
	for i := 0; i < 10; i++ {
		if rec := f.req("GET", "/v1/audit/verify", "", []*http.Cookie{c}); rec.Code != http.StatusForbidden {
			t.Fatalf("denial %d = %d", i, rec.Code)
		}
	}
	n := 0
	for _, e := range f.audit.events {
		if e.Action == "rbac.denied" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("audited denials = %d, want 2 (the rest are suppressed)", n)
	}
	// The next window reports how many were skipped.
	f.denialClock.t = f.denialClock.t.Add(2 * time.Minute)
	f.req("GET", "/v1/audit/verify", "", []*http.Cookie{c})
	last := f.audit.events[len(f.audit.events)-1]
	if last.Action != "rbac.denied" || !strings.Contains(string(last.Payload), `"suppressed_before":8`) {
		t.Fatalf("last event = %s %s", last.Action, last.Payload)
	}
}
