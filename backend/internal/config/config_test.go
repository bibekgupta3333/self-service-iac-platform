package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func lookupFrom(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoad(t *testing.T) {
	dev := map[string]string{"IACP_AUTH_MODE": "dev"}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{"IACP_AUTH_MODE": "dev"}
		for k, v := range extra {
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
			name: "defaults bind loopback",
			env:  dev,
			check: func(t *testing.T, c Config) {
				if !strings.HasPrefix(c.DatabaseURL, "postgres://iacp_app:") || c.MigrateDatabaseURL != DefaultDatabaseURL ||
					c.AppDBPassword != DevAppDBPassword {
					t.Fatalf("runtime must use iacp_app and migrations the owner: %+v", c)
				}
				if c.HTTPAddr != "127.0.0.1:8080" || c.LogLevel != slog.LevelInfo ||
					c.ShutdownTimeout != 15*time.Second || !c.AutoMigrate || c.AuthMode != AuthModeDev || c.InternalToken != "" {
					t.Fatalf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name: "overrides",
			env: with(map[string]string{
				"IACP_HTTP_ADDR": ":9090", "IACP_LOG_LEVEL": "debug", "IACP_SHUTDOWN_TIMEOUT": "3s",
				"IACP_AUTO_MIGRATE": "false", "IACP_DATABASE_URL": "postgres://x",
				"IACP_INTERNAL_TOKEN":       strings.Repeat("t", 32),
				"IACP_MIGRATE_DATABASE_URL": "postgres://owner", "IACP_APP_DB_PASSWORD": "s3cret",
			}),
			check: func(t *testing.T, c Config) {
				if c.MigrateDatabaseURL != "postgres://owner" || c.AppDBPassword != "s3cret" {
					t.Fatalf("db overrides not applied: %+v", c)
				}
				if c.HTTPAddr != ":9090" || c.LogLevel != slog.LevelDebug || c.ShutdownTimeout != 3*time.Second ||
					c.AutoMigrate || c.DatabaseURL != "postgres://x" || len(c.InternalToken) != 32 {
					t.Fatalf("overrides not applied: %+v", c)
				}
			},
		},
		{
			name: "oidc disabled by default",
			env:  dev,
			check: func(t *testing.T, c Config) {
				if c.OIDC.Enabled() || c.OIDC.Issuer != DefaultOIDCIssuer ||
					c.OIDC.JWKSURL != DefaultOIDCIssuer+"/.well-known/jwks" || c.OIDC.Leeway != 30*time.Second {
					t.Fatalf("oidc defaults: %+v", c.OIDC)
				}
			},
		},
		{
			name: "oidc configured",
			env: with(map[string]string{
				"IACP_OIDC_AUDIENCE": "https://api.iacp.acme.internal", "IACP_OIDC_REPOSITORY": "acme/iacp",
				"IACP_OIDC_REPOSITORY_ID": "87654321", "IACP_OIDC_ISSUER": "http://127.0.0.1:9999/",
				"IACP_OIDC_ALLOWED_REFS": "refs/heads/main, refs/pull/*/merge", "IACP_OIDC_LEEWAY": "10s",
				"IACP_OIDC_ALLOWED_WORKFLOWS": "acme/iacp/.github/workflows/apply.yml@refs/heads/main",
			}),
			check: func(t *testing.T, c Config) {
				o := c.OIDC
				if !o.Enabled() || o.Repository != "acme/iacp" || o.RepositoryID != "87654321" || o.Issuer != "http://127.0.0.1:9999" ||
					o.JWKSURL != "http://127.0.0.1:9999/.well-known/jwks" || len(o.Refs) != 2 || o.Refs[1] != "refs/pull/*/merge" ||
					len(o.WorkflowRefs) != 1 || o.Leeway != 10*time.Second {
					t.Fatalf("oidc = %+v", o)
				}
			},
		},
		{name: "oidc audience without repository", env: with(map[string]string{"IACP_OIDC_AUDIENCE": "x"}), wantErr: "must be set together"},
		{name: "oidc repository without audience", env: with(map[string]string{"IACP_OIDC_REPOSITORY": "a/b"}), wantErr: "must be set together"},
		{name: "oidc leeway too large", env: with(map[string]string{"IACP_OIDC_LEEWAY": "1h"}), wantErr: "OIDC_LEEWAY"},
		{name: "auth mode is required", env: map[string]string{}, wantErr: "IACP_AUTH_MODE must be set"},
		{name: "blank auth mode is required", env: map[string]string{"IACP_AUTH_MODE": "  "}, wantErr: "IACP_AUTH_MODE must be set"},
		{name: "unknown auth mode", env: map[string]string{"IACP_AUTH_MODE": "none"}, wantErr: "unsupported value"},
		{name: "short internal token", env: with(map[string]string{"IACP_INTERNAL_TOKEN": "short"}), wantErr: "INTERNAL_TOKEN"},
		{name: "short metrics token", env: with(map[string]string{"IACP_METRICS_TOKEN": "short"}), wantErr: "METRICS_TOKEN must be at least"},
		{name: "metrics token reuses the internal token", env: with(map[string]string{
			"IACP_METRICS_TOKEN": strings.Repeat("t", 32), "IACP_INTERNAL_TOKEN": strings.Repeat("t", 32)}), wantErr: "must differ"},
		{name: "metrics token", env: with(map[string]string{"IACP_METRICS_TOKEN": strings.Repeat("m", 32)}), check: func(t *testing.T, c Config) {
			if c.MetricsToken != strings.Repeat("m", 32) {
				t.Fatalf("metrics token = %q", c.MetricsToken)
			}
		}},
		{name: "bad level", env: with(map[string]string{"IACP_LOG_LEVEL": "loud"}), wantErr: "LOG_LEVEL"},
		{name: "bad timeout", env: with(map[string]string{"IACP_SHUTDOWN_TIMEOUT": "soon"}), wantErr: "SHUTDOWN_TIMEOUT"},
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
