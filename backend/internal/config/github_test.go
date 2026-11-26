package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadGitHubAuth(t *testing.T) {
	// Values are generated here, never real credentials.
	full := func(extra map[string]string) map[string]string {
		m := map[string]string{
			"IACP_AUTH_MODE":            "github",
			"IACP_GITHUB_CLIENT_ID":     "test-client-id",
			"IACP_GITHUB_CLIENT_SECRET": strings.Repeat("s", 40),
			"IACP_GITHUB_CALLBACK_URL":  "https://portal.example.test/api/iacp/v1/auth/callback",
			"IACP_GITHUB_ORG":           "acme",
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	without := func(m map[string]string, k string) map[string]string { delete(m, k); return m }

	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr string
	}{
		{name: "full config", env: full(nil), check: func(t *testing.T, c Config) {
			g := c.GitHub
			if g.Org != "acme" || g.AdminTeam != "iacp-admins" || g.DefaultRedirect != "/" ||
				g.SessionTTL != 8*time.Hour || g.SessionIdleTTL != time.Hour || !g.SecureCookies() {
				t.Fatalf("github = %+v", g)
			}
			if c.AppDBPassword != "" {
				t.Fatal("github mode must not apply the dev database password")
			}
		}},
		{name: "loopback http callback is not secure", env: full(map[string]string{
			"IACP_GITHUB_CALLBACK_URL": "http://localhost:3000/api/iacp/v1/auth/callback"}), check: func(t *testing.T, c Config) {
			if c.GitHub.SecureCookies() {
				t.Fatal("loopback http must not demand Secure cookies")
			}
		}},
		{name: "github mode needs everything", env: without(full(nil), "IACP_GITHUB_ORG"), wantErr: "must all be set together"},
		{name: "github mode without any github var", env: map[string]string{"IACP_AUTH_MODE": "github"}, wantErr: "must all be set together"},
		{name: "half config fails even in dev mode", env: map[string]string{"IACP_AUTH_MODE": "dev", "IACP_GITHUB_CLIENT_ID": "x"}, wantErr: "must all be set together"},
		{name: "http callback on a public host", env: full(map[string]string{
			"IACP_GITHUB_CALLBACK_URL": "http://portal.example.test/cb"}), wantErr: "must be https"},
		{name: "callback with credentials", env: full(map[string]string{
			"IACP_GITHUB_CALLBACK_URL": "https://user:pw@portal.example.test/cb"}), wantErr: "not a valid absolute URL"},
		{name: "relative callback", env: full(map[string]string{"IACP_GITHUB_CALLBACK_URL": "/cb"}), wantErr: "not a valid absolute URL"},
		{name: "org with slash", env: full(map[string]string{"IACP_GITHUB_ORG": "acme/x"}), wantErr: "organisation login"},
		{name: "allowlist", env: full(map[string]string{
			"IACP_AUTH_REDIRECT_ALLOWLIST": "https://a.example.test, https://b.example.test/"}), check: func(t *testing.T, c Config) {
			if got := strings.Join(c.GitHub.RedirectAllowlist, ","); got != "https://a.example.test,https://b.example.test" {
				t.Fatalf("allowlist = %s", got)
			}
		}},
		{name: "allowlist rejects paths", env: full(map[string]string{
			"IACP_AUTH_REDIRECT_ALLOWLIST": "https://a.example.test/x"}), wantErr: "AUTH_REDIRECT_ALLOWLIST"},
		{name: "allowlist rejects plain http", env: full(map[string]string{
			"IACP_AUTH_REDIRECT_ALLOWLIST": "http://a.example.test"}), wantErr: "AUTH_REDIRECT_ALLOWLIST"},
		{name: "default redirect must be a path", env: full(map[string]string{
			"IACP_AUTH_DEFAULT_REDIRECT": "//evil.example.test"}), wantErr: "AUTH_DEFAULT_REDIRECT"},
		{name: "session ttl bounds", env: full(map[string]string{"IACP_AUTH_SESSION_TTL": "10s"}), wantErr: "AUTH_SESSION_TTL"},
		{name: "idle ttl is capped by ttl", env: full(map[string]string{
			"IACP_AUTH_SESSION_TTL": "30m", "IACP_AUTH_SESSION_IDLE_TTL": "2h"}), check: func(t *testing.T, c Config) {
			if c.GitHub.SessionIdleTTL != 30*time.Minute {
				t.Fatalf("idle = %s", c.GitHub.SessionIdleTTL)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(lookupFrom(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, c)
		})
	}
}
