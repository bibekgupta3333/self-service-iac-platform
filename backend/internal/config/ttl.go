package config

import (
	"errors"
	"fmt"
	"strconv"
)

// maxTTLCeilingHours mirrors ttl.MaxTTLCeilingHours (one year).
const maxTTLCeilingHours = 8760

// TTLConfig bounds the lifetime of ephemeral stacks (docs/06 §6). All values are whole
// hours except MaxAttempts. It mirrors ttl.Policy field for field (main converts it),
// because config must stay a leaf package: the ttl package depends on the outbox, whose
// tests use config.
type TTLConfig struct {
	MinHours          int // IACP_TTL_MIN_HOURS, default 1
	MaxHours          int // IACP_TTL_MAX_HOURS, default 720 (30 days), at most 8760
	DefaultHours      int // IACP_TTL_DEFAULT_HOURS, default 72
	MaxExtensionHours int // IACP_TTL_MAX_EXTENSION_HOURS, default 168: the total extensions may add to one stack
	BackoffHours      int // IACP_TTL_RETRY_BACKOFF_HOURS, default 24: wait after a failed TTL destroy; 0 disables
	MaxAttempts       int // IACP_TTL_MAX_ATTEMPTS, default 3: failed TTL destroys before the stack is blocked; 0 means no limit
}

// Validate reports an inconsistent set of bounds.
func (t TTLConfig) Validate() error {
	switch {
	case t.MinHours < 1:
		return fmt.Errorf("minimum must be at least 1 hour, got %d", t.MinHours)
	case t.MaxHours < t.MinHours:
		return fmt.Errorf("maximum %d is below the minimum %d", t.MaxHours, t.MinHours)
	case t.MaxHours > maxTTLCeilingHours:
		return fmt.Errorf("maximum %d exceeds the ceiling of %d hours", t.MaxHours, maxTTLCeilingHours)
	case t.DefaultHours < t.MinHours || t.DefaultHours > t.MaxHours:
		return fmt.Errorf("default %d must be between %d and %d", t.DefaultHours, t.MinHours, t.MaxHours)
	case t.MaxExtensionHours < 0:
		return fmt.Errorf("maximum total extension must not be negative, got %d", t.MaxExtensionHours)
	case t.BackoffHours < 0 || t.MaxAttempts < 0:
		return fmt.Errorf("retry backoff (%d) and maximum attempts (%d) must not be negative", t.BackoffHours, t.MaxAttempts)
	}
	return nil
}

// loadTTL reads the IACP_TTL_* bounds.
func loadTTL(get func(name, def string) string) (TTLConfig, error) {
	var errs []error
	num := func(name string, def int) int {
		v, err := strconv.Atoi(get(name, strconv.Itoa(def)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s must be a whole number", Prefix, name))
			return def
		}
		return v
	}
	t := TTLConfig{
		MinHours:          num("TTL_MIN_HOURS", 1),
		MaxHours:          num("TTL_MAX_HOURS", 720),
		DefaultHours:      num("TTL_DEFAULT_HOURS", 72),
		MaxExtensionHours: num("TTL_MAX_EXTENSION_HOURS", 168),
		BackoffHours:      num("TTL_RETRY_BACKOFF_HOURS", 24),
		MaxAttempts:       num("TTL_MAX_ATTEMPTS", 3),
	}
	if len(errs) == 0 {
		if err := t.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%sTTL_*: %w", Prefix, err))
		}
	}
	return t, errors.Join(errs...)
}
