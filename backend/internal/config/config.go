// Package config loads process configuration from IACP_-prefixed environment variables.
//
// Load is pure: it reads values through a lookup function, so tests pass a map
// instead of mutating the process environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Prefix is the namespace for every environment variable read by the backend.
const Prefix = "IACP_"

// DefaultDatabaseURL is the schema-owner connection to the docker compose Postgres
// (local development only). It is used for migrations, never for serving requests.
const DefaultDatabaseURL = "postgres://iacp:iacp-local-only@localhost:5432/iacp?sslmode=disable"

// DefaultAppDatabaseURL is the runtime connection as the least-privileged iacp_app role.
const DefaultAppDatabaseURL = "postgres://iacp_app:" + DevAppDBPassword + "@localhost:5432/iacp?sslmode=disable"

// DevAppDBPassword is the iacp_app password applied in dev mode when none is configured.
const DevAppDBPassword = "iacp-app-local-only"

// DefaultHTTPAddr binds loopback only; the container image overrides it (see Dockerfile).
const DefaultHTTPAddr = "127.0.0.1:8080"

// MinInternalTokenLen is the minimum length of IACP_INTERNAL_TOKEN.
const MinInternalTokenLen = 32

// AuthMode selects the Authenticator implementation. There is no default: an
// unconfigured process refuses to start rather than trusting every caller.
type AuthMode string

const (
	// AuthModeDev authenticates every caller as a fixed local developer.
	AuthModeDev AuthMode = "dev"
	// AuthModeGitHub authenticates browser users with GitHub OAuth and server-side sessions.
	AuthModeGitHub AuthMode = "github"
)

// Config is the fully resolved backend configuration.
type Config struct {
	HTTPAddr string
	// DatabaseURL is the runtime connection (role iacp_app: DML, audit INSERT/SELECT only).
	DatabaseURL string
	// MigrateDatabaseURL is the schema-owner connection used only to apply migrations.
	MigrateDatabaseURL string
	// AppDBPassword, when set, is applied to iacp_app (LOGIN) after migrating.
	AppDBPassword   string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	AutoMigrate     bool
	AuthMode        AuthMode
	DevUserLogin    string
	ModuleRepoURL   string
	// InternalToken is the shared bearer token for /v1/internal/* and /v1/runs, accepted
	// alongside GitHub OIDC. Empty means the token path rejects every call.
	InternalToken string
	// MetricsToken lets non-loopback scrapers read /metrics ("Authorization: Bearer").
	// Empty means /metrics answers loopback peers only. It is deliberately separate from
	// InternalToken, which can take stack locks.
	MetricsToken string
	// OIDC configures GitHub Actions OIDC verification; disabled when Audience is empty.
	OIDC OIDCConfig
	// GitHub configures GitHub OAuth login and sessions (IACP_AUTH_MODE=github).
	GitHub GitHubAuthConfig
	// Publisher selects how generated stacks reach the repository (WBS 4.8).
	Publisher PublisherConfig
	// WebhookSecret verifies X-Hub-Signature-256 on POST /v1/webhooks/github. Empty means
	// the endpoint rejects every delivery.
	WebhookSecret string
	// PortalURL is the developer portal base URL, used for request links in PR bodies.
	PortalURL string
	// LocalStackEndpoint is rendered into generated provider blocks (ADR-0009).
	LocalStackEndpoint string
	// TrustedProxies (IACP_TRUSTED_PROXIES, comma-separated CIDRs or addresses) are the
	// reverse proxies whose X-Forwarded-For header is believed for rate limiting and the
	// audit actor_ip. Empty (the default) trusts no proxy: the client is the TCP peer.
	TrustedProxies []string
	// WorkerPollInterval is how often the in-process worker polls the outbox when idle.
	WorkerPollInterval time.Duration
}

// DefaultOIDCIssuer is the GitHub Actions token issuer.
const DefaultOIDCIssuer = "https://token.actions.githubusercontent.com"

// OIDCConfig configures GitHub Actions OIDC verification for machine endpoints.
type OIDCConfig struct {
	Audience     string        // IACP_OIDC_AUDIENCE: the aud the workflows request
	Repository   string        // IACP_OIDC_REPOSITORY: owner/name allowed to call
	RepositoryID string        // IACP_OIDC_REPOSITORY_ID: optional numeric id
	Issuer       string        // IACP_OIDC_ISSUER (tests and local dev only)
	JWKSURL      string        // IACP_OIDC_JWKS_URL, default <issuer>/.well-known/jwks
	Refs         []string      // IACP_OIDC_ALLOWED_REFS: comma-separated patterns; empty means the defaults
	WorkflowRefs []string      // IACP_OIDC_ALLOWED_WORKFLOWS: comma-separated job_workflow_ref patterns
	Leeway       time.Duration // IACP_OIDC_LEEWAY: clock skew allowed on exp/nbf/iat
	ReplayStore  ReplayStore   // IACP_OIDC_REPLAY_STORE: where used jti values are kept
}

// ReplayStore selects where used OIDC token ids are remembered.
type ReplayStore string

const (
	// ReplayStorePostgres shares used ids across replicas (table oidc_jti). Default.
	ReplayStorePostgres ReplayStore = "postgres"
	// ReplayStoreMemory keeps them in a bounded per-process cache: single instance only.
	ReplayStoreMemory ReplayStore = "memory"
)

// Enabled reports whether OIDC verification is configured.
func (o OIDCConfig) Enabled() bool { return o.Audience != "" }

// LookupFunc returns the value of an environment variable and whether it was set.
type LookupFunc func(key string) (string, bool)

// Load resolves Config from lookup, applying defaults and validating values.
func Load(lookup LookupFunc) (Config, error) {
	get := func(name, def string) string {
		if v, ok := lookup(Prefix + name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}

	var errs []error
	level, err := parseLevel(get("LOG_LEVEL", "info"))
	errs = append(errs, err)
	timeout, err := time.ParseDuration(get("SHUTDOWN_TIMEOUT", "15s"))
	if err != nil {
		err = fmt.Errorf("%sSHUTDOWN_TIMEOUT: %w", Prefix, err)
	}
	errs = append(errs, err)
	autoMigrate, err := strconv.ParseBool(get("AUTO_MIGRATE", "true"))
	if err != nil {
		err = fmt.Errorf("%sAUTO_MIGRATE: %w", Prefix, err)
	}
	errs = append(errs, err)

	cfg := Config{
		HTTPAddr:           get("HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL:        get("DATABASE_URL", DefaultAppDatabaseURL),
		MigrateDatabaseURL: get("MIGRATE_DATABASE_URL", DefaultDatabaseURL),
		AppDBPassword:      get("APP_DB_PASSWORD", ""),
		LogLevel:           level,
		ShutdownTimeout:    timeout,
		AutoMigrate:        autoMigrate,
		AuthMode:           AuthMode(get("AUTH_MODE", "")),
		DevUserLogin:       get("DEV_USER", "local-dev"),
		ModuleRepoURL:      get("MODULE_REPO_URL", "https://github.com/bibekgupta3333/self-service-iac-platform.git"),
		InternalToken:      get("INTERNAL_TOKEN", ""),
		WebhookSecret:      get("GITHUB_WEBHOOK_SECRET", ""),
		PortalURL:          strings.TrimRight(get("PORTAL_URL", "http://localhost:3000"), "/"),
		LocalStackEndpoint: get("LOCALSTACK_ENDPOINT", "http://localhost:4566"),
		MetricsToken:       get("METRICS_TOKEN", ""),
		TrustedProxies:     splitList(get("TRUSTED_PROXIES", "")),
	}
	for _, e := range cfg.TrustedProxies {
		if !validProxyEntry(e) {
			errs = append(errs, fmt.Errorf("%sTRUSTED_PROXIES: %q is neither a CIDR nor an IP address", Prefix, e))
		}
	}
	oidc, err := loadOIDC(get)
	errs = append(errs, err)
	cfg.OIDC = oidc
	gh, err := loadGitHubAuth(get, cfg.AuthMode)
	errs = append(errs, err)
	cfg.GitHub = gh
	pub, err := loadPublisher(get)
	errs = append(errs, err)
	cfg.Publisher = pub
	cfg.WorkerPollInterval, err = loadWorker(get)
	errs = append(errs, err)
	if cfg.WebhookSecret != "" && len(cfg.WebhookSecret) < MinWebhookSecretLen {
		errs = append(errs, fmt.Errorf("%sGITHUB_WEBHOOK_SECRET must be at least %d characters", Prefix, MinWebhookSecretLen))
	}
	switch cfg.AuthMode {
	case AuthModeDev:
		if cfg.AppDBPassword == "" {
			cfg.AppDBPassword = DevAppDBPassword // matches DefaultAppDatabaseURL
		}
	case AuthModeGitHub:
		// Unlike dev mode, IACP_APP_DB_PASSWORD gets no default here (it stays empty unless set),
		// so nothing provisions the dev password on iacp_app. IACP_DATABASE_URL still defaults
		// to the local compose URL, which embeds that dev password: a real deployment must
		// set IACP_DATABASE_URL (and IACP_APP_DB_PASSWORD or provision the role itself).
	case "":
		errs = append(errs, fmt.Errorf("%sAUTH_MODE must be set explicitly (supported: dev, github)", Prefix))
	default:
		errs = append(errs, fmt.Errorf("%sAUTH_MODE: unsupported value %q (supported: dev, github)", Prefix, cfg.AuthMode))
	}
	if cfg.InternalToken != "" && len(cfg.InternalToken) < MinInternalTokenLen {
		errs = append(errs, fmt.Errorf("%sINTERNAL_TOKEN must be at least %d characters", Prefix, MinInternalTokenLen))
	}
	if cfg.MetricsToken != "" && len(cfg.MetricsToken) < MinInternalTokenLen {
		errs = append(errs, fmt.Errorf("%sMETRICS_TOKEN must be at least %d characters", Prefix, MinInternalTokenLen))
	}
	if cfg.MetricsToken != "" && cfg.MetricsToken == cfg.InternalToken {
		errs = append(errs, fmt.Errorf("%sMETRICS_TOKEN must differ from %sINTERNAL_TOKEN", Prefix, Prefix))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// loadOIDC reads the IACP_OIDC_* variables. Audience and repository must be set together:
// half a configuration fails loudly instead of silently disabling OIDC.
func loadOIDC(get func(name, def string) string) (OIDCConfig, error) {
	o := OIDCConfig{
		Audience:     get("OIDC_AUDIENCE", ""),
		Repository:   get("OIDC_REPOSITORY", ""),
		RepositoryID: get("OIDC_REPOSITORY_ID", ""),
		Issuer:       strings.TrimRight(get("OIDC_ISSUER", DefaultOIDCIssuer), "/"),
		Refs:         splitList(get("OIDC_ALLOWED_REFS", "")),
		WorkflowRefs: splitList(get("OIDC_ALLOWED_WORKFLOWS", "")),
	}
	o.JWKSURL = get("OIDC_JWKS_URL", o.Issuer+"/.well-known/jwks")
	var errs []error
	leeway, err := time.ParseDuration(get("OIDC_LEEWAY", "30s"))
	if err != nil || leeway < 0 || leeway > 5*time.Minute {
		errs = append(errs, fmt.Errorf("%sOIDC_LEEWAY must be a duration between 0 and 5m", Prefix))
	}
	o.Leeway = leeway
	o.ReplayStore = ReplayStore(get("OIDC_REPLAY_STORE", string(ReplayStorePostgres)))
	if o.ReplayStore != ReplayStorePostgres && o.ReplayStore != ReplayStoreMemory {
		errs = append(errs, fmt.Errorf("%sOIDC_REPLAY_STORE must be postgres or memory", Prefix))
	}
	if (o.Audience == "") != (o.Repository == "") {
		errs = append(errs, fmt.Errorf("%sOIDC_AUDIENCE and %sOIDC_REPOSITORY must be set together", Prefix, Prefix))
	}
	return o, errors.Join(errs...)
}

// validProxyEntry reports whether e is a CIDR or a single IP address. Pure.
func validProxyEntry(e string) bool {
	if _, err := netip.ParsePrefix(e); err == nil {
		return true
	}
	_, err := netip.ParseAddr(e)
	return err == nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("%sLOG_LEVEL: %w", Prefix, err)
	}
	return l, nil
}
