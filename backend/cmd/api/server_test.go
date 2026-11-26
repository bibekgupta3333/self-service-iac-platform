package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/config"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/httpapi"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestMachineAuthenticatorFailsClosed(t *testing.T) {
	token := strings.Repeat("t", 32)
	oidc := config.OIDCConfig{Audience: "https://api.iacp.acme.internal", Repository: "acme/iacp",
		Issuer: config.DefaultOIDCIssuer, JWKSURL: "https://token.actions.githubusercontent.com/.well-known/jwks", Leeway: 30 * time.Second}
	tests := []struct {
		name      string
		cfg       config.Config
		wantErr   bool
		tokenOpen bool // the shared token authenticates
	}{
		{name: "nothing configured denies everything", cfg: config.Config{}},
		{name: "token only", cfg: config.Config{InternalToken: token}, tokenOpen: true},
		{name: "oidc only rejects the token", cfg: config.Config{OIDC: oidc}},
		{name: "oidc and token", cfg: config.Config{OIDC: oidc, InternalToken: token}, tokenOpen: true},
		{name: "bad oidc policy refuses to start", cfg: config.Config{OIDC: func() config.OIDCConfig {
			o := oidc
			o.Issuer = "http://issuer.example"
			return o
		}()}, wantErr: true},
		{name: "plain http JWKS URL refuses to start", cfg: config.Config{OIDC: func() config.OIDCConfig {
			o := oidc
			o.JWKSURL = "http://keys.example/jwks"
			return o
		}()}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := newMachineAuthenticator(tt.cfg, platform.SystemClock{})
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, header := range []string{"", "Bearer ", "Bearer " + token} {
				r := httptest.NewRequest("POST", "/v1/runs", nil)
				r.Header.Set("Authorization", header)
				_, err := a.Authenticate(r)
				if want := tt.tokenOpen && header == "Bearer "+token; want != (err == nil) {
					t.Fatalf("header %q: err = %v", header, err)
				}
			}
			if _, isDeny := a.(httpapi.DenyAuthenticator); isDeny != (tt.cfg.InternalToken == "" && !tt.cfg.OIDC.Enabled()) {
				t.Fatalf("authenticator = %T", a)
			}
		})
	}
}

func TestNewServerSetsTimeouts(t *testing.T) {
	s := newServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Fatalf("all server timeouts and header limits must be set: %+v", s)
	}
	if s.ReadTimeout < s.ReadHeaderTimeout || s.WriteTimeout < s.ReadTimeout {
		t.Fatalf("timeouts should nest (header <= read <= write): %v %v %v", s.ReadHeaderTimeout, s.ReadTimeout, s.WriteTimeout)
	}
}
