package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/webhooks"
)

// maxWebhookBytes bounds webhook bodies (pull_request payloads are tens of KiB).
const maxWebhookBytes = 2 << 20

var eventNameRe = regexp.MustCompile(`^[a-z_]{1,64}$`)

// WebhookIngester is the webhook use-case port.
type WebhookIngester interface {
	Ingest(ctx context.Context, d webhooks.Delivery) (webhooks.Result, error)
}

// WebhookHandler serves POST /v1/webhooks/github. It authenticates by signature, so mount
// it with Deps.Webhooks, outside the user and machine authenticators.
type WebhookHandler struct {
	ing    WebhookIngester
	secret []byte
	wake   func() // optional: nudge the worker after enqueueing
}

// NewWebhookHandler returns a WebhookHandler. An empty secret rejects every delivery.
func NewWebhookHandler(ing WebhookIngester, secret string, wake func()) *WebhookHandler {
	return &WebhookHandler{ing: ing, secret: []byte(secret), wake: wake}
}

// Routes implements RouteRegistrar.
func (h *WebhookHandler) Routes(r chi.Router) { r.Post("/webhooks/github", h.receive) }

type webhookAcceptedDTO struct {
	Accepted  bool `json:"accepted"`
	Duplicate bool `json:"duplicate"`
	Enqueued  bool `json:"enqueued"`
}

func (h *WebhookHandler) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes+1))
	if err != nil {
		writeError(w, r, badRequest("could not read request body"))
		return
	}
	if len(body) > maxWebhookBytes {
		writeError(w, r, badRequest(fmt.Sprintf("webhook body exceeds %d bytes", maxWebhookBytes)))
		return
	}
	if err := webhooks.VerifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		code := "invalid_signature"
		if errors.Is(err, webhooks.ErrNoSecret) {
			code = "webhook_not_configured"
		}
		observability.Logger(r.Context()).WarnContext(r.Context(), "webhook rejected", "reason", err.Error())
		writeError(w, r, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: code, Message: "webhook signature verification failed"})
		return
	}
	id, err := uuid.Parse(r.Header.Get("X-GitHub-Delivery"))
	if err != nil {
		writeError(w, r, badRequest("X-GitHub-Delivery must be a GUID"))
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if !eventNameRe.MatchString(event) {
		writeError(w, r, badRequest("X-GitHub-Event is missing or invalid"))
		return
	}
	res, err := h.ing.Ingest(r.Context(), webhooks.Delivery{ID: id, Event: event, Body: body})
	if errors.Is(err, webhooks.ErrMalformedPayload) {
		writeError(w, r, badRequest(err.Error()))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if res.Enqueued && h.wake != nil {
		h.wake()
	}
	writeJSON(w, http.StatusAccepted, webhookAcceptedDTO{Accepted: true, Duplicate: res.Duplicate, Enqueued: res.Enqueued})
}
