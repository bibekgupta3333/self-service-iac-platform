package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RequestLookupService resolves the request apply.yml acts on (requests.Service).
type RequestLookupService interface {
	ApplyTargetByPath(ctx context.Context, path, head string) (requests.ApplyTarget, error)
}

// RequestLookupHandler serves GET /v1/internal/requests/by-path for apply.yml.
type RequestLookupHandler struct{ svc RequestLookupService }

// NewRequestLookupHandler returns a RequestLookupHandler.
func NewRequestLookupHandler(svc RequestLookupService) *RequestLookupHandler {
	return &RequestLookupHandler{svc: svc}
}

// Routes implements RouteRegistrar. Mount it under /v1/internal.
func (h *RequestLookupHandler) Routes(r chi.Router) {
	r.With(requireRoute(auth.RouteRequestLookup)).Get("/requests/by-path", h.get)
}

type applyTargetDTO struct {
	StackID          uuid.UUID         `json:"stack_id"`
	StackPath        string            `json:"stack_path"`
	RequestID        uuid.UUID         `json:"request_id"`
	RequestShortID   string            `json:"request_short_id"`
	Kind             requests.Kind     `json:"kind"`
	Status           requests.Status   `json:"status"`
	FencingToken     int64             `json:"fencing_token"`
	ApprovedPlanHash string            `json:"approved_plan_hash,omitempty"`
	MergeCommitSHA   string            `json:"merge_commit_sha,omitempty"`
	Links            map[string]string `json:"links"`
}

// get answers ?stack_path=environments/<env>/<team>/<stack>[&head_sha=<40 hex>]. Both
// parameters are validated strictly by the service; an unknown parameter is refused so a
// misspelt one cannot silently skip the head check.
func (h *RequestLookupHandler) get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for name := range q {
		if name != "stack_path" && name != "head_sha" {
			writeError(w, r, badRequest("unknown query parameter "+name))
			return
		}
	}
	if len(q["stack_path"]) != 1 || len(q["head_sha"]) > 1 {
		writeError(w, r, apperr.Invalid("invalid_stack_path", "send stack_path once (and head_sha at most once)"))
		return
	}
	t, err := h.svc.ApplyTargetByPath(r.Context(), q.Get("stack_path"), q.Get("head_sha"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, applyTargetDTO{
		StackID: t.StackID, StackPath: t.StackPath, RequestID: t.RequestID, RequestShortID: t.RequestShortID,
		Kind: t.Kind, Status: t.Status, FencingToken: t.FencingToken, ApprovedPlanHash: t.ApprovedPlanHash,
		MergeCommitSHA: t.MergeCommitSHA,
		Links: map[string]string{"request": "/v1/requests/" + t.RequestID.String(),
			"lock": "/v1/internal/stacks/" + t.StackID.String() + "/lock"},
	})
}
