package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

func mustProxies(t *testing.T, entries ...string) TrustedProxies {
	t.Helper()
	p, err := ParseTrustedProxies(entries)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseTrustedProxies(t *testing.T) {
	for _, bad := range []string{"nope", "10.0.0.0/33", "10.0.0.1/8/9"} {
		if _, err := ParseTrustedProxies([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	p := mustProxies(t, " 10.0.0.0/8 ", "192.168.1.7", "::1", "")
	if p.Empty() || len(p.nets) != 3 {
		t.Fatalf("parsed %v", p.nets)
	}
	if !mustProxies(t).Empty() {
		t.Fatal("no entries must trust nobody")
	}
}

func TestClientIP(t *testing.T) {
	proxies := mustProxies(t, "10.0.0.0/8", "172.16.0.5", "fd00::/8")
	tests := []struct {
		name    string
		trust   TrustedProxies
		remote  string
		xff     []string
		want    string
		comment string
	}{
		{name: "no proxies configured: the peer, header ignored", trust: TrustedProxies{}, remote: "203.0.113.9:4000",
			xff: []string{"198.51.100.1"}, want: "203.0.113.9"},
		{name: "untrusted peer cannot forge the header", trust: proxies, remote: "203.0.113.9:4000",
			xff: []string{"198.51.100.1"}, want: "203.0.113.9"},
		{name: "trusted peer, one hop", trust: proxies, remote: "10.1.2.3:4000",
			xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "spoofed left entries are skipped", trust: proxies, remote: "10.1.2.3:4000",
			xff: []string{"1.1.1.1, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "right-most untrusted wins over a client-supplied one further left", trust: proxies, remote: "10.1.2.3:4000",
			xff: []string{"6.6.6.6, 198.51.100.1, 10.9.9.9, 172.16.0.5"}, want: "198.51.100.1"},
		{name: "several header lines are one list", trust: proxies, remote: "10.1.2.3:4000",
			xff: []string{"6.6.6.6", "198.51.100.1, 10.9.9.9"}, want: "198.51.100.1"},
		{name: "all hops trusted: the peer", trust: proxies, remote: "10.1.2.3:4000",
			xff: []string{"10.9.9.9, 172.16.0.5"}, want: "10.1.2.3"},
		{name: "no header: the peer", trust: proxies, remote: "10.1.2.3:4000", want: "10.1.2.3"},
		{name: "garbage entry from a trusted hop: the peer", trust: proxies, remote: "10.1.2.3:4000",
			xff: []string{"198.51.100.1, not-an-ip"}, want: "10.1.2.3"},
		{name: "ipv6 peer and client", trust: proxies, remote: "[fd00::1]:4000",
			xff: []string{"2001:db8::7"}, want: "2001:db8::7"},
		{name: "v4-mapped peer matches a v4 range", trust: proxies, remote: "[::ffff:10.1.2.3]:4000",
			xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "remote without a port", trust: proxies, remote: "10.1.2.3", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "unparseable peer", trust: proxies, remote: "garbage", xff: []string{"198.51.100.1"}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.trust.ClientIP(tc.remote, tc.xff); got != tc.want {
				t.Fatalf("ClientIP(%q, %q) = %q, want %q", tc.remote, tc.xff, got, tc.want)
			}
		})
	}
}

func loginFrom(f *flow, remote, xff string) int {
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/login", nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec.Code
}

// Behind a trusted proxy the limiter keys on the real client, so one noisy client cannot
// lock everyone else out; without trust it stays one bucket per peer.
func TestLoginLimiterKeysOnTheTrustedClientAddress(t *testing.T) {
	f := newFlow(t, true, rbac.RoleDeveloper)
	f.limitLogin(2)
	f.ah.Proxies = mustProxies(t, "10.0.0.0/8")
	const proxy = "10.0.0.7:5000"
	for i := 0; i < 2; i++ {
		if code := loginFrom(f, proxy, "198.51.100.1"); code != http.StatusFound {
			t.Fatalf("client A attempt %d = %d", i, code)
		}
	}
	if code := loginFrom(f, proxy, "198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("client A over the limit = %d", code)
	}
	if code := loginFrom(f, proxy, "198.51.100.2"); code != http.StatusFound {
		t.Fatalf("client B behind the same proxy must have its own bucket, got %d", code)
	}
	// A forged left entry does not move the key.
	if code := loginFrom(f, proxy, "9.9.9.9, 198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("forged left entry escaped the limiter: %d", code)
	}

	g := newFlow(t, true, rbac.RoleDeveloper)
	g.limitLogin(2)
	for i := 0; i < 2; i++ {
		loginFrom(g, proxy, "198.51.100.1")
	}
	if code := loginFrom(g, proxy, "198.51.100.2"); code != http.StatusTooManyRequests {
		t.Fatalf("with no trusted proxies the proxy peer is the one bucket, got %d", code)
	}
}

// The rbac.denied audit event records the client address by the same rule.
func TestDenialAuditUsesTheTrustedClientAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		proxies []string
		want    string
	}{
		{"trusted proxy", []string{"192.0.2.0/24"}, "198.51.100.1"}, // httptest peers are 192.0.2.1
		{"no trust", nil, "192.0.2.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t, true, rbac.RoleDeveloper)
			f.deps.TrustedProxies = mustProxies(t, tc.proxies...)
			f.h = NewRouter(f.deps)
			c := f.signIn(t)
			f.audit.events = nil
			if rec := f.req("GET", "/v1/audit/verify", "", []*http.Cookie{c}, "X-Forwarded-For", "9.9.9.9, 198.51.100.1"); rec.Code != http.StatusForbidden {
				t.Fatalf("verify = %d", rec.Code)
			}
			if len(f.audit.events) != 1 || f.audit.events[0].ActorIP != tc.want {
				t.Fatalf("audit events = %+v, want actor_ip %s", f.audit.events, tc.want)
			}
		})
	}
}
