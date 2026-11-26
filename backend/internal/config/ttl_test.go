package config

import (
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

func TestLoadTTL(t *testing.T) {
	load := func(extra map[string]string) (Config, error) {
		env := map[string]string{"IACP_AUTH_MODE": "dev"}
		for k, v := range extra {
			env[k] = v
		}
		return Load(lookupFrom(env))
	}

	c, err := load(nil)
	if err != nil || c.TTL != ttl.DefaultPolicy() {
		t.Fatalf("defaults: %+v, %v", c.TTL, err)
	}
	c, err = load(map[string]string{"IACP_TTL_MIN_HOURS": "2", "IACP_TTL_MAX_HOURS": "48",
		"IACP_TTL_DEFAULT_HOURS": "12", "IACP_TTL_MAX_EXTENSION_HOURS": "24"})
	want := ttl.Policy{MinHours: 2, MaxHours: 48, DefaultHours: 12, MaxExtensionHours: 24}
	if err != nil || c.TTL != want {
		t.Fatalf("overrides: %+v, %v", c.TTL, err)
	}

	for name, env := range map[string]map[string]string{
		"not a number":      {"IACP_TTL_MAX_HOURS": "soon"},
		"default above max": {"IACP_TTL_MAX_HOURS": "24", "IACP_TTL_DEFAULT_HOURS": "48"},
		"max below min":     {"IACP_TTL_MIN_HOURS": "10", "IACP_TTL_MAX_HOURS": "5", "IACP_TTL_DEFAULT_HOURS": "10"},
		"zero minimum":      {"IACP_TTL_MIN_HOURS": "0"},
	} {
		if _, err := load(env); err == nil || !strings.Contains(err.Error(), "IACP_TTL") {
			t.Errorf("%s: want a TTL configuration error, got %v", name, err)
		}
	}
}
