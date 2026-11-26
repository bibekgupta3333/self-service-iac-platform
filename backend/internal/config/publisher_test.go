package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadPublisher(t *testing.T) {
	app := map[string]string{
		"IACP_PUBLISHER": "github", "IACP_GITHUB_APP_ID": "12345", "IACP_GITHUB_INSTALLATION_ID": "99",
		"IACP_GITHUB_PRIVATE_KEY_PATH": "/run/secrets/app.pem", "IACP_GITHUB_REPOSITORY": "acme/infra",
	}
	with := func(base map[string]string, extra map[string]string) map[string]string {
		out := map[string]string{"IACP_AUTH_MODE": "dev"}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			if v == "" {
				delete(out, k)
				continue
			}
			out[k] = v
		}
		return out
	}
	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr string
	}{
		{
			name: "default is none, webhooks closed",
			env:  with(nil, nil),
			check: func(t *testing.T, c Config) {
				if c.Publisher.Kind != PublisherNone || c.Publisher.BaseBranch != "main" || c.WebhookSecret != "" ||
					c.PortalURL != "http://localhost:3000" || c.LocalStackEndpoint != "http://localhost:4566" ||
					c.WorkerPollInterval != time.Second || c.Publisher.GitHub.APIURL != DefaultGitHubAPIURL {
					t.Fatalf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name: "github app",
			env:  with(app, map[string]string{"IACP_PUBLISH_BASE_BRANCH": "trunk", "IACP_PORTAL_URL": "https://iacp.example/"}),
			check: func(t *testing.T, c Config) {
				g := c.Publisher.GitHub
				if c.Publisher.Kind != PublisherGitHub || g.AppID != 12345 || g.InstallationID != 99 ||
					g.PrivateKeyPath != "/run/secrets/app.pem" || g.Repository() != "acme/infra" ||
					c.Publisher.BaseBranch != "trunk" || c.PortalURL != "https://iacp.example" {
					t.Fatalf("github config: %+v", c.Publisher)
				}
			},
		},
		{
			name: "local",
			env:  with(nil, map[string]string{"IACP_PUBLISHER": "local", "IACP_LOCAL_GIT_REPO": "/tmp/scratch"}),
			check: func(t *testing.T, c Config) {
				if c.Publisher.Kind != PublisherLocal || c.Publisher.LocalRepoPath != "/tmp/scratch" {
					t.Fatalf("local config: %+v", c.Publisher)
				}
			},
		},
		{
			name: "repository alone is allowed (webhook repository filter)",
			env:  with(nil, map[string]string{"IACP_GITHUB_REPOSITORY": "acme/infra"}),
			check: func(t *testing.T, c Config) {
				if c.Publisher.GitHub.Repository() != "acme/infra" {
					t.Fatalf("repository: %+v", c.Publisher.GitHub)
				}
			},
		},
		{name: "half github config: no key", env: with(app, map[string]string{"IACP_GITHUB_PRIVATE_KEY_PATH": ""}),
			wantErr: "IACP_PUBLISHER=github needs [IACP_GITHUB_PRIVATE_KEY_PATH]"},
		{name: "half github config: no installation, no repo", env: with(app, map[string]string{"IACP_GITHUB_INSTALLATION_ID": "", "IACP_GITHUB_REPOSITORY": ""}),
			wantErr: "needs [IACP_GITHUB_INSTALLATION_ID IACP_GITHUB_REPOSITORY]"},
		{name: "app settings without github publisher", env: with(app, map[string]string{"IACP_PUBLISHER": ""}),
			wantErr: "only used with IACP_PUBLISHER=github"},
		{name: "local without repo", env: with(nil, map[string]string{"IACP_PUBLISHER": "local"}),
			wantErr: "IACP_PUBLISHER=local needs IACP_LOCAL_GIT_REPO"},
		{name: "local repo without local publisher", env: with(nil, map[string]string{"IACP_LOCAL_GIT_REPO": "/tmp/x"}),
			wantErr: "IACP_LOCAL_GIT_REPO is only used with"},
		{name: "unknown publisher", env: with(nil, map[string]string{"IACP_PUBLISHER": "gitlab"}),
			wantErr: `unsupported value "gitlab"`},
		{name: "bad app id", env: with(app, map[string]string{"IACP_GITHUB_APP_ID": "-3"}),
			wantErr: "IACP_GITHUB_APP_ID: must be a positive integer"},
		{name: "bad repository", env: with(app, map[string]string{"IACP_GITHUB_REPOSITORY": "acme"}),
			wantErr: "IACP_GITHUB_REPOSITORY must be owner/name"},
		{name: "bad base branch", env: with(nil, map[string]string{"IACP_PUBLISH_BASE_BRANCH": "-x"}),
			wantErr: "IACP_PUBLISH_BASE_BRANCH"},
		{name: "short webhook secret", env: with(nil, map[string]string{"IACP_GITHUB_WEBHOOK_SECRET": "short"}),
			wantErr: "IACP_GITHUB_WEBHOOK_SECRET must be at least 16"},
		{name: "bad poll interval", env: with(nil, map[string]string{"IACP_WORKER_POLL_INTERVAL": "1h"}),
			wantErr: "IACP_WORKER_POLL_INTERVAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(lookupFrom(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
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
