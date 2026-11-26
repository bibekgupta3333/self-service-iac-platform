package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/config"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/httpapi"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/teamsync"
)

// userAuth is everything the router needs for user authentication and RBAC.
type userAuth struct {
	Authenticator httpapi.Authenticator
	// Handler serves /v1/me (both modes) and the OAuth endpoints (github mode).
	Handler *httpapi.AuthHandler
	Public  []httpapi.RouteRegistrar
	Roles   httpapi.RoleLookup
	Denials httpapi.EventRecorder
}

// newUserAuth builds the user authenticator for cfg.AuthMode. In dev mode the fixed local
// user is a platform admin for route policies only: request creation still reads its team
// role from the database, exactly as before.
func newUserAuth(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, clock platform.Clock,
	auditWriter *audit.Writer, logger *slog.Logger) (userAuth, error) {
	users := identity.NewUserStore(pool)
	recorder := audit.NewRecorder(db.NewTxRunner(pool), auditWriter)
	out := userAuth{Roles: users, Denials: recorder}

	switch cfg.AuthMode {
	case config.AuthModeDev:
		p, err := users.EnsureDevUser(ctx, cfg.DevUserLogin)
		if err != nil {
			return userAuth{}, err
		}
		p.PlatformAdmin = true
		slog.Warn("dev authentication enabled: every caller acts as the local user", "login", p.Login)
		out.Authenticator = httpapi.DevAuthenticator{Principal: p}
		out.Handler = &httpapi.AuthHandler{Profiles: users, Mode: string(config.AuthModeDev)}
		return out, nil

	case config.AuthModeGitHub:
		g := cfg.GitHub
		policy := session.Policy{AbsoluteTTL: g.SessionTTL, IdleTTL: g.SessionIdleTTL, TouchInterval: time.Minute}
		sessions := session.NewService(session.NewPostgresStore(pool), policy, clock)
		states := oauth.NewPostgresStateStore(pool)
		gh, err := oauth.NewGitHubClient(oauth.GitHubConfig{ClientID: g.ClientID, ClientSecret: g.ClientSecret,
			CallbackURL: g.CallbackURL, Org: g.Org})
		if err != nil {
			return userAuth{}, err
		}
		syncer := teamsync.NewSyncer(db.NewTxRunner(pool), auditWriter, clock, teamsync.Mapping{AdminTeam: g.AdminTeam})
		login := oauth.NewService(gh, states, syncer, sessions, recorder,
			oauth.RedirectPolicy{Origins: g.RedirectAllowlist, Default: g.DefaultRedirect}, clock)
		out.Authenticator = httpapi.NewSessionAuthenticator(sessions)
		out.Handler = &httpapi.AuthHandler{Login: login, Sessions: sessions, Profiles: users, Audit: recorder,
			Secure: g.SecureCookies(), Mode: string(config.AuthModeGitHub)}
		out.Public = []httpapi.RouteRegistrar{out.Handler.Public()}
		go sweepAuth(ctx, sessions, states, clock, logger)
		slog.Info("GitHub OAuth login enabled", "org", g.Org, "callback", g.CallbackURL, "secure_cookies", g.SecureCookies())
		return out, nil
	}
	return userAuth{}, fmt.Errorf("unsupported auth mode %q", cfg.AuthMode)
}

// sweepAuth purges dead sessions and expired login states every five minutes.
func sweepAuth(ctx context.Context, sessions *session.Service, states oauth.StateStore, clock platform.Clock, logger *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := sessions.Purge(ctx); err != nil {
				logger.Error("purge sessions", "error", err)
			}
			if _, err := states.Purge(ctx, clock.Now()); err != nil {
				logger.Error("purge login states", "error", err)
			}
		}
	}
}
