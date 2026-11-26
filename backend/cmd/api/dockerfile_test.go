package main

import (
	"os"
	"strings"
	"testing"
)

// TestDockerfileFailsClosed guards the image defaults: listen on all interfaces inside the
// container, run as nonroot, and never preselect an auth mode.
func TestDockerfileFailsClosed(t *testing.T) {
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	df := string(b)
	for _, want := range []string{"ENV IACP_HTTP_ADDR=0.0.0.0:8080", "USER nonroot:nonroot", ":nonroot@sha256:"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile is missing %q", want)
		}
	}
	for _, line := range strings.Split(df, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ENV") && strings.Contains(line, "IACP_AUTH_MODE") {
			t.Errorf("Dockerfile must not set IACP_AUTH_MODE: %q", line)
		}
	}
}
