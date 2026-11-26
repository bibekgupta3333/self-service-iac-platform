package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadQueueRetention(t *testing.T) {
	env := map[string]string{"IACP_AUTH_MODE": "dev"}
	c, err := Load(lookupFrom(env))
	if err != nil || c.QueueRetention != 7*24*time.Hour {
		t.Fatalf("default = %v, %v", c.QueueRetention, err)
	}
	env["IACP_QUEUE_RETENTION"] = "48h"
	if c, err = Load(lookupFrom(env)); err != nil || c.QueueRetention != 48*time.Hour {
		t.Fatalf("override = %v, %v", c.QueueRetention, err)
	}
	for _, bad := range []string{"30m", "-1h", "soon"} {
		env["IACP_QUEUE_RETENTION"] = bad
		if _, err = Load(lookupFrom(env)); err == nil || !strings.Contains(err.Error(), "IACP_QUEUE_RETENTION") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}
