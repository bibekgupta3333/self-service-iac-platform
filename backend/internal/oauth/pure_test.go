package oauth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestChallengeMatchesRFC7636Vector(t *testing.T) {
	// RFC 7636 appendix B.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := Challenge(verifier); got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
	if !ValidToken(verifier) {
		t.Fatal("the RFC verifier has the token shape")
	}
}

func TestNewTokenShapeAndFailure(t *testing.T) {
	tok, err := NewToken(strings.NewReader(strings.Repeat("x", 32)))
	if err != nil || !ValidToken(tok) || len(tok) < 43 || len(tok) > 128 {
		t.Fatalf("NewToken = %q, %v", tok, err)
	}
	if _, err := NewToken(strings.NewReader("short")); err == nil {
		t.Fatal("short entropy must fail")
	}
	for _, bad := range []string{"", "abc", tok + "A", strings.Repeat("!", 43)} {
		if ValidToken(bad) {
			t.Errorf("ValidToken(%q)", bad)
		}
	}
}

func TestStatesMatch(t *testing.T) {
	a, _ := NewToken(strings.NewReader(strings.Repeat("a", 32)))
	b, _ := NewToken(strings.NewReader(strings.Repeat("b", 32)))
	if !StatesMatch(a, a) {
		t.Fatal("equal states must match")
	}
	for _, c := range [][2]string{{a, b}, {a, ""}, {"", a}, {"", ""}, {"x", "x"}} {
		if StatesMatch(c[0], c[1]) {
			t.Errorf("StatesMatch(%q, %q)", c[0], c[1])
		}
	}
}

func TestRedirectPolicyResolve(t *testing.T) {
	p := RedirectPolicy{Origins: []string{"https://portal.example.test", "http://localhost:3000"}, Default: "/home"}
	tests := []struct{ in, want string }{
		{"", "/home"},
		{"/requests", "/requests"},
		{"/requests?team=payments#x", "/requests?team=payments#x"},
		{"/", "/"},
		{"https://portal.example.test/requests", "https://portal.example.test/requests"},
		{"HTTPS://PORTAL.example.test/x", "https://portal.example.test/x"},
		{"http://localhost:3000/catalog", "http://localhost:3000/catalog"},
		// Open-redirect attempts.
		{"//evil.example.test", "/home"},
		{"//evil.example.test/x", "/home"},
		{"/\\evil.example.test", "/home"},
		{"\\\\evil.example.test", "/home"},
		{"https://evil.example.test/", "/home"},
		{"https://portal.example.test.evil.example.test/", "/home"},
		{"https://portal.example.test@evil.example.test/", "/home"},
		{"https://user:pw@portal.example.test/", "/home"},
		{"http://portal.example.test/", "/home"}, // scheme downgrade
		{"https://portal.example.test:8443/", "/home"},
		{"javascript:alert(1)", "/home"},
		{"data:text/html,x", "/home"},
		{"portal.example.test/x", "/home"},
		{"requests", "/home"},
		{"/ok\r\nSet-Cookie: a=b", "/home"},
		{"/ok\nx", "/home"},
		{"/ok\tx", "/home"},
		{"/ok\x00", "/home"},
		{"/" + strings.Repeat("a", 3000), "/home"},
	}
	for _, tt := range tests {
		if got := p.Resolve(tt.in); got != tt.want {
			t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if (RedirectPolicy{Default: "/"}).Resolve("https://portal.example.test/") != "/" {
		t.Fatal("an empty allowlist allows no absolute URL")
	}
}

func TestLoginErrorCode(t *testing.T) {
	for err, want := range map[error]string{
		nil:                                  "",
		ErrInvalidState:                      "invalid_state",
		fmt.Errorf("x: %w", ErrNotOrgMember): "not_org_member",
		ErrUserDisabled:                      "user_disabled",
		fmt.Errorf("%w: %w", ErrGitHubUnavailable, ErrGitHub): "github_unavailable",
		errors.New("boom"): "login_failed",
	} {
		if got := LoginErrorCode(err); got != want {
			t.Errorf("LoginErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
}
