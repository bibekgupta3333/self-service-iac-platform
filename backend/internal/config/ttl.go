package config

import (
	"errors"
	"fmt"
	"strconv"
)

// TTLConfig bounds the lifetime of ephemeral stacks (docs/06 §6). All values are whole
// hours. It mirrors ttl.Policy field for field (main converts it), because config must
// stay a leaf package: the ttl package depends on the outbox, whose tests use config.
type TTLConfig struct {
	MinHours          int // IACP_TTL_MIN_HOURS, default 1
	MaxHours          int // IACP_TTL_MAX_HOURS, default 720 (30 days)
	DefaultHours      int // IACP_TTL_DEFAULT_HOURS, default 72
	MaxExtensionHours int // IACP_TTL_MAX_EXTENSION_HOURS, default 168: the total extensions may add to one stack
}

// Validate reports an inconsistent set of bounds.
func (t TTLConfig) Validate() error {
	switch {
	case t.MinHours < 1:
		return fmt.Errorf("minimum must be at least 1 hour, got %d", t.MinHours)
	case t.MaxHours < t.MinHours:
		return fmt.Errorf("maximum %d is below the minimum %d", t.MaxHours, t.MinHours)
	case t.DefaultHours < t.MinHours || t.DefaultHours > t.MaxHours:
		return fmt.Errorf("default %d must be between %d and %d", t.DefaultHours, t.MinHours, t.MaxHours)
	case t.MaxExtensionHours < 0:
		return fmt.Errorf("maximum total extension must not be negative, got %d", t.MaxExtensionHours)
	}
	return nil
}

// loadTTL reads the IACP_TTL_* bounds.
func loadTTL(get func(name, def string) string) (TTLConfig, error) {
	var errs []error
	hours := func(name string, def int) int {
		v, err := strconv.Atoi(get(name, strconv.Itoa(def)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s must be a whole number of hours", Prefix, name))
			return def
		}
		return v
	}
	t := TTLConfig{
		MinHours:          hours("TTL_MIN_HOURS", 1),
		MaxHours:          hours("TTL_MAX_HOURS", 720),
		DefaultHours:      hours("TTL_DEFAULT_HOURS", 72),
		MaxExtensionHours: hours("TTL_MAX_EXTENSION_HOURS", 168),
	}
	if len(errs) == 0 {
		if err := t.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%sTTL_*: %w", Prefix, err))
		}
	}
	return t, errors.Join(errs...)
}
