package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/webhooks"
)

type fakeIngester struct {
	seen  map[uuid.UUID]bool
	calls int
}

func (f *fakeIngester) Ingest(_ context.Context, d webhooks.Delivery) (webhooks.Result, error) {
	f.calls++
	if _, err := webhooks.Map(d.Event, d.ID.String(), d.Body); err != nil {
		return webhooks.Result{}, err
	}
	if f.seen[d.ID] {
		return webhooks.Result{Duplicate: true}, nil
	}
	f.seen[d.ID] = true
	return webhooks.Result{Enqueued: d.Event == "pull_request"}, nil
}

const hookSecret = "webhook-secret-for-tests"

func TestWebhookEndpoint(t *testing.T) {
	ing := &fakeIngester{seen: map[uuid.UUID]bool{}}
	woken := 0
	// failingAuth on every other route proves the webhook route needs no user or machine auth.
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: failingAuth{},
		Webhooks: []RouteRegistrar{NewWebhookHandler(ing, hookSecret, func() { woken++ })}})
	body := []byte(`{"action":"closed","number":3,"pull_request":{"merged":false,"head":{"ref":"iacp/req-7k3p9x"}}}`)
	delivery := uuid.NewString()
	send := func(b []byte, sig, id, event string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(b))
		for k, v := range map[string]string{"X-Hub-Signature-256": sig, "X-GitHub-Delivery": id, "X-GitHub-Event": event} {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	sig := webhooks.Sign([]byte(hookSecret), body)
	tests := []struct {
		name   string
		body   []byte
		sig    string
		id     string
		event  string
		status int
		code   string
		want   string
	}{
		{"accepted", body, sig, delivery, "pull_request", 202, "", `"enqueued":true`},
		{"redelivery is acknowledged once", body, sig, delivery, "pull_request", 202, "", `"duplicate":true`},
		{"unknown event acknowledged", []byte(`{}`), webhooks.Sign([]byte(hookSecret), []byte(`{}`)), uuid.NewString(), "star", 202, "", `"enqueued":false`},
		{"bad signature", body, webhooks.Sign([]byte("another-secret-value"), body), uuid.NewString(), "pull_request", 401, "invalid_signature", ""},
		{"missing signature", body, "", uuid.NewString(), "pull_request", 401, "invalid_signature", ""},
		{"bad delivery id", body, sig, "not-a-guid", "pull_request", 400, "bad_request", ""},
		{"missing event", body, sig, uuid.NewString(), "", 400, "bad_request", ""},
		{"malformed payload", []byte(`{`), webhooks.Sign([]byte(hookSecret), []byte(`{`)), uuid.NewString(), "pull_request", 400, "bad_request", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := send(tt.body, tt.sig, tt.id, tt.event)
			if rec.Code != tt.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if tt.code != "" {
				var p Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != tt.code {
					t.Fatalf("problem = %+v, %v", p, err)
				}
			}
			if tt.want != "" && !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("body %s, want %s", rec.Body, tt.want)
			}
		})
	}
	if woken != 1 {
		t.Fatalf("worker woken %d times, want 1", woken)
	}

	big := bytes.Repeat([]byte("a"), maxWebhookBytes+1)
	if rec := send(big, webhooks.Sign([]byte(hookSecret), big), uuid.NewString(), "pull_request"); rec.Code != 400 {
		t.Fatalf("oversized body: %d", rec.Code)
	}
}

func TestWebhookFailsClosedWithoutSecret(t *testing.T) {
	ing := &fakeIngester{seen: map[uuid.UUID]bool{}}
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: failingAuth{},
		Webhooks: []RouteRegistrar{NewWebhookHandler(ing, "", nil)}})
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", webhooks.Sign(nil, body))
	req.Header.Set("X-GitHub-Delivery", uuid.NewString())
	req.Header.Set("X-GitHub-Event", "ping")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "webhook_not_configured") || ing.calls != 0 {
		t.Fatalf("status %d body %s calls %d", rec.Code, rec.Body, ing.calls)
	}
}

type errIngester struct{}

func (errIngester) Ingest(context.Context, webhooks.Delivery) (webhooks.Result, error) {
	return webhooks.Result{}, fmt.Errorf("db down: %w", errors.New("x"))
}

func TestWebhookStoreErrorIs500(t *testing.T) {
	h := NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: failingAuth{},
		Webhooks: []RouteRegistrar{NewWebhookHandler(errIngester{}, hookSecret, nil)}})
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", webhooks.Sign([]byte(hookSecret), body))
	req.Header.Set("X-GitHub-Delivery", uuid.NewString())
	req.Header.Set("X-GitHub-Event", "ping")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
}
