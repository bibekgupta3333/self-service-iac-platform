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

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/config"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/httpapi"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
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
	requestSvc := requests.NewService(requestRepo, catalogSvc, requestRepo, clock, ids)

	if cfg.InternalToken == "" {
		slog.Warn("IACP_INTERNAL_TOKEN is not set: /v1/internal endpoints reject every request")
	}
	internalAuth := httpapi.NewTokenAuthenticator(cfg.InternalToken,
		identity.Principal{Login: "internal-token", Type: identity.ActorGitHubActions})

	handler := httpapi.NewRouter(httpapi.Deps{
		Logger:   logger,
		Auth:     auth,
		Internal: internalAuth,
		Ready:    pool,
		Catalog:  httpapi.NewCatalogHandler(catalogSvc),
		Public: []httpapi.RouteRegistrar{
			httpapi.NewRequestHandler(requestSvc),
			httpapi.NewAuditHandler(audit.NewVerifier(pool)),
		},
	})

	return serve(ctx, newServer(cfg.HTTPAddr, handler), cfg.ShutdownTimeout)
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
