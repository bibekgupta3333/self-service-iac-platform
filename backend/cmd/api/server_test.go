package main

import (
	"net/http"
	"testing"
)

func TestNewServerSetsTimeouts(t *testing.T) {
	s := newServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Fatalf("all server timeouts and header limits must be set: %+v", s)
	}
	if s.ReadTimeout < s.ReadHeaderTimeout || s.WriteTimeout < s.ReadTimeout {
		t.Fatalf("timeouts should nest (header <= read <= write): %v %v %v", s.ReadHeaderTimeout, s.ReadTimeout, s.WriteTimeout)
	}
}
