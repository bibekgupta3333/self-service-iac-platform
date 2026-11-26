// Command api runs the platform control-plane HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/config"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/generator"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/httpapi"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/prflow"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish/githubapp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish/localgit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/stacks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/webhooks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	if cfg.AutoMigrate {
		if err := migrate(ctx, cfg, logger); err != nil {
			return err
		}
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("runtime database (as %s): %w", db.AppRole, err)
	}
	defer pool.Close()
	if privileged, err := db.Privileged(ctx, pool); err != nil {
		return err
	} else if privileged {
		slog.Warn("IACP_DATABASE_URL connects as a superuser or schema owner: audit_events append-only grants are not enforced; use the iacp_app role")
	}

	auth, err := newAuthenticator(ctx, cfg, identity.NewUserStore(pool))
	if err != nil {
		return err
	}

	clock, ids := platform.SystemClock{}, platform.RandomIDs{}
	auditWriter := audit.NewWriter(clock, ids)
	catalogSvc := catalog.NewService(catalog.NewPostgresRepository(pool, auditWriter), clock, cfg.ModuleRepoURL)
	requestRepo := requests.NewPostgresRepository(pool, auditWriter)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, auditWriter), clock, locks.DefaultLease)
	requestSvc := requests.NewService(requestRepo, catalogSvc, requestRepo, lockSvc, clock, ids)

	pub, err := newPublisher(ctx, cfg, clock)
	if err != nil {
		return err
	}
	var heads webhooks.PRHeads
	if pub != nil {
		heads = pub
	}
	handlers := map[string]outbox.Handler{outbox.TopicGitHubEvent: webhooks.NewApplier(requestSvc, heads)}
	if pub != nil {
		requestSvc.EnablePublishing()
		lockSvc.OnPromote(requests.PublishOnPromote(clock))
		render := generator.New(cfg.ModuleRepoURL, generator.LocalBackend{}, generator.LocalStackProvider{Endpoint: cfg.LocalStackEndpoint})
		handlers[outbox.TopicRequestPublish] = prflow.NewService(requestSvc, catalogSvc, requestRepo, render, pub,
			prflow.Options{PortalURL: cfg.PortalURL})
		slog.Info("publisher enabled", "publisher", pub.Name(), "base_branch", cfg.Publisher.BaseBranch)
	} else {
		slog.Info("publisher disabled (IACP_PUBLISHER=none): requests stay in draft")
	}
	worker := outbox.NewWorker(outbox.NewPostgresStore(pool), handlers, clock,
		outbox.Options{PollInterval: cfg.WorkerPollInterval, Logger: logger})
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); worker.Run(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	if cfg.WebhookSecret == "" {
		slog.Warn("IACP_GITHUB_WEBHOOK_SECRET is not set: POST /v1/webhooks/github rejects every delivery")
	}
	webhookHandler := httpapi.NewWebhookHandler(
		webhooks.NewIngester(webhooks.NewPostgresRepository(pool), clock, cfg.Publisher.GitHub.Repository()),
		cfg.WebhookSecret, worker.Wake)

	replayStore := newReplayStore(cfg.OIDC, pool, clock)
	startReplaySweeper(ctx, replayStore, cfg.OIDC.Enabled(), logger)
	internalAuth, err := newMachineAuthenticator(cfg, clock, replayStore)
	if err != nil {
		return err
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Logger:   logger,
		Auth:     auth,
		Internal: internalAuth,
		Ready:    pool,
		Catalog:  httpapi.NewCatalogHandler(catalogSvc),
		Public: []httpapi.RouteRegistrar{
			httpapi.NewRequestHandler(requestSvc),
			httpapi.NewRequestEventsHandler(requestSvc, httpapi.StreamOptions{Stop: ctx.Done()}),
			httpapi.NewStackHandler(stacks.NewService(stacks.NewPostgresRepository(pool))),
			httpapi.NewAuditHandler(audit.NewVerifier(pool)),
		},
		InternalHandlers: []httpapi.RouteRegistrar{
			httpapi.NewLockHandler(lockSvc),
			httpapi.NewRequestLookupHandler(requestSvc),
			httpapi.NewBudgetContextHandler(budgets.NewService(budgets.NewPostgresRepository(pool), clock)),
		},
		Webhooks:         []httpapi.RouteRegistrar{webhookHandler},
		PipelineHandlers: []httpapi.RouteRegistrar{httpapi.NewRunHandler(runs.NewService(runs.NewPostgresRepository(pool), clock))},
		Metrics:          newMetrics(pool),
		MetricsAccess:    httpapi.NewMetricsAccess(cfg.MetricsToken),
	})

	return serve(ctx, newServer(cfg.HTTPAddr, handler), cfg.ShutdownTimeout)
}

// newPublisher builds the configured publish.Publisher; nil for IACP_PUBLISHER=none.
func newPublisher(ctx context.Context, cfg config.Config, clock platform.Clock) (publish.Publisher, error) {
	switch cfg.Publisher.Kind {
	case config.PublisherLocal:
		return localgit.New(ctx, localgit.Config{RepoPath: cfg.Publisher.LocalRepoPath, BaseBranch: cfg.Publisher.BaseBranch})
	case config.PublisherGitHub:
		g := cfg.Publisher.GitHub
		key, err := os.ReadFile(g.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read GitHub App private key: %w", err)
		}
		return githubapp.New(githubapp.Config{AppID: g.AppID, InstallationID: g.InstallationID, PrivateKey: key,
			Owner: g.Owner, Repo: g.Repo, BaseBranch: cfg.Publisher.BaseBranch, APIURL: g.APIURL}, clock)
	}
	return nil, nil
}

// newMetrics builds the Prometheus registry served on /metrics, including pool gauges.
func newMetrics(pool *pgxpool.Pool) *observability.Metrics {
	m := observability.NewMetrics()
	m.RegisterPool(func() observability.PoolStats {
		s := pool.Stat()
		return observability.PoolStats{Acquired: s.AcquiredConns(), Idle: s.IdleConns(), Total: s.TotalConns(), Max: s.MaxConns()}
	})
	return m
}

// newServer bounds every phase of a connection so slow or idle clients cannot pin
// goroutines and file descriptors.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second, // bodies are capped at 1 MiB
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// migrate applies migrations as the schema owner, then enables login for the runtime
// role when a password is configured. The owner connection is closed afterwards.
func migrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	owner, err := db.Open(ctx, cfg.MigrateDatabaseURL)
	if err != nil {
		return fmt.Errorf("migration database: %w", err)
	}
	defer owner.Close()
	if err := db.NewMigrator(owner, migrations.FS, logger).Up(ctx); err != nil {
		return err
	}
	if cfg.AppDBPassword != "" {
		if err := db.ProvisionAppRole(ctx, owner, cfg.AppDBPassword); err != nil {
			return err
		}
	}
	return nil
}

func newAuthenticator(ctx context.Context, cfg config.Config, users *identity.UserStore) (httpapi.Authenticator, error) {
	switch cfg.AuthMode {
	case config.AuthModeDev:
		p, err := users.EnsureDevUser(ctx, cfg.DevUserLogin)
		if err != nil {
			return nil, err
		}
		slog.Warn("dev authentication enabled: every caller acts as the local user", "login", p.Login)
		return httpapi.DevAuthenticator{Principal: p}, nil
	}
	return nil, fmt.Errorf("unsupported auth mode %q", cfg.AuthMode)
}

// newMachineAuthenticator builds the authenticator for /v1/internal/* and /v1/runs. It
// fails closed: GitHub OIDC when configured, the shared token when set, and with
// neither every call is rejected.
func newMachineAuthenticator(cfg config.Config, clock platform.Clock, replay auth.ReplayStore) (httpapi.Authenticator, error) {
	var chain httpapi.AnyAuthenticator
	if cfg.OIDC.Enabled() {
		o := cfg.OIDC
		policy := auth.Policy{Issuer: o.Issuer, Audience: o.Audience, Repository: o.Repository, RepositoryID: o.RepositoryID,
			Refs: o.Refs, WorkflowRefs: o.WorkflowRefs}
		if len(policy.Refs) == 0 {
			policy.Refs = auth.DefaultRefs
		}
		if len(policy.WorkflowRefs) == 0 {
			policy.WorkflowRefs = auth.DefaultWorkflowRefs(o.Repository)
		}
		keys, err := auth.NewJWKSCache(o.JWKSURL, clock, auth.JWKSOptions{})
		if err != nil {
			return nil, err
		}
		verifier, err := auth.NewVerifier(policy, keys, clock, o.Leeway)
		if err != nil {
			return nil, err
		}
		chain = append(chain, httpapi.NewOIDCAuthenticator(auth.NewReplayGuard(verifier, replay, o.Leeway)))
		if o.RepositoryID == "" {
			slog.Warn("IACP_OIDC_REPOSITORY_ID is not set: tokens are matched by owner/name only, which can be re-registered after a rename or delete")
		}
		slog.Info("GitHub OIDC enabled for machine endpoints", "repository", o.Repository, "issuer", o.Issuer)
	}
	if cfg.InternalToken != "" {
		chain = append(chain, httpapi.NewTokenAuthenticator(cfg.InternalToken,
			identity.Principal{Login: "internal-token", Type: identity.ActorGitHubActions}))
	}
	if len(chain) == 0 {
		slog.Warn("neither IACP_OIDC_AUDIENCE nor IACP_INTERNAL_TOKEN is set: /v1/internal and /v1/runs reject every request")
		return httpapi.DenyAuthenticator{}, nil
	}
	return chain, nil
}

// newReplayStore picks where used OIDC jti values are remembered (docs/03 §6.9).
func newReplayStore(o config.OIDCConfig, pool *pgxpool.Pool, clock platform.Clock) auth.ReplayStore {
	if o.ReplayStore == config.ReplayStoreMemory {
		if o.Enabled() {
			slog.Warn("IACP_OIDC_REPLAY_STORE=memory: replayed tokens are only caught per instance; use postgres with more than one replica")
		}
		return auth.NewMemoryReplayCache(auth.DefaultReplayCapacity, clock)
	}
	return auth.NewPostgresReplayStore(pool, clock)
}

// startReplaySweeper purges expired oidc_jti rows in the background when the shared
// store is in use; claims only remove an expired row for their own jti.
func startReplaySweeper(ctx context.Context, store auth.ReplayStore, enabled bool, logger *slog.Logger) {
	if pg, ok := store.(*auth.PostgresReplayStore); ok && enabled {
		go auth.RunReplaySweeper(ctx, pg, 5*time.Minute, logger)
	}
}

// serve runs srv until ctx is cancelled, then drains in-flight requests.
func serve(ctx context.Context, srv *http.Server, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down http server", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
