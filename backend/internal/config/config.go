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

// DefaultDatabaseURL points at the docker compose Postgres (local development only).
const DefaultDatabaseURL = "postgres://iacp:iacp-local-only@localhost:5432/iacp?sslmode=disable"

// AuthMode selects the Authenticator implementation.
type AuthMode string

const (
	// AuthModeDev authenticates every caller as a fixed local developer.
	AuthModeDev AuthMode = "dev"
)

// Config is the fully resolved backend configuration.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	AutoMigrate     bool
	AuthMode        AuthMode
	DevUserLogin    string
	ModuleRepoURL   string
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
		HTTPAddr:        get("HTTP_ADDR", ":8080"),
		DatabaseURL:     get("DATABASE_URL", DefaultDatabaseURL),
		LogLevel:        level,
		ShutdownTimeout: timeout,
		AutoMigrate:     autoMigrate,
		AuthMode:        AuthMode(get("AUTH_MODE", string(AuthModeDev))),
		DevUserLogin:    get("DEV_USER", "local-dev"),
		ModuleRepoURL:   get("MODULE_REPO_URL", "https://github.com/bibekgupta3333/self-service-iac-platform.git"),
	}
	if cfg.AuthMode != AuthModeDev {
		errs = append(errs, fmt.Errorf("%sAUTH_MODE: unsupported value %q (supported: dev)", Prefix, cfg.AuthMode))
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
