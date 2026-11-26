package config

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

// loadTTL reads the IACP_TTL_* bounds for ephemeral stacks (docs/06 §6). Values are whole
// hours; the result must be a consistent ttl.Policy.
func loadTTL(get func(name, def string) string) (ttl.Policy, error) {
	def := ttl.DefaultPolicy()
	var errs []error
	hours := func(name string, d int) int {
		v, err := strconv.Atoi(get(name, strconv.Itoa(d)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s must be a whole number of hours", Prefix, name))
			return d
		}
		return v
	}
	p := ttl.Policy{
		MinHours:          hours("TTL_MIN_HOURS", def.MinHours),
		MaxHours:          hours("TTL_MAX_HOURS", def.MaxHours),
		DefaultHours:      hours("TTL_DEFAULT_HOURS", def.DefaultHours),
		MaxExtensionHours: hours("TTL_MAX_EXTENSION_HOURS", def.MaxExtensionHours),
	}
	if len(errs) == 0 {
		if err := p.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%sTTL_*: %w", Prefix, err))
		}
	}
	return p, errors.Join(errs...)
}
