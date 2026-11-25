// Package config loads process configuration from IACP_-prefixed environment variables.
//
// Load is pure: it reads values through a lookup function, so tests pass a map
// instead of mutating the process environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
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
	// InternalToken authenticates /v1/internal/* callers until the GitHub OIDC
	// verifier lands (WBS 4.10). Empty means every internal call is rejected.
	InternalToken string
}

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
	}
	switch cfg.AuthMode {
	case AuthModeDev:
		if cfg.AppDBPassword == "" {
			cfg.AppDBPassword = DevAppDBPassword // matches DefaultAppDatabaseURL
		}
	case "":
		errs = append(errs, fmt.Errorf("%sAUTH_MODE must be set explicitly (supported: dev)", Prefix))
	default:
		errs = append(errs, fmt.Errorf("%sAUTH_MODE: unsupported value %q (supported: dev)", Prefix, cfg.AuthMode))
	}
	if cfg.InternalToken != "" && len(cfg.InternalToken) < MinInternalTokenLen {
		errs = append(errs, fmt.Errorf("%sINTERNAL_TOKEN must be at least %d characters", Prefix, MinInternalTokenLen))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("%sLOG_LEVEL: %w", Prefix, err)
	}
	return l, nil
}
