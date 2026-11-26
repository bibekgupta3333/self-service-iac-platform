package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/recovery"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RollbackService opens rollback requests (recovery.RollbackService).
type RollbackService interface {
	Rollback(ctx context.Context, by identity.Principal, cmd recovery.RollbackCommand) (requests.CreateResult, error)
}

// RollbackHandler serves POST /v1/stacks/{id}/rollback.
type RollbackHandler struct{ svc RollbackService }

// NewRollbackHandler returns a RollbackHandler.
func NewRollbackHandler(svc RollbackService) *RollbackHandler { return &RollbackHandler{svc: svc} }

// Routes implements RouteRegistrar. Mount it with Deps.Public (user routes).
func (h *RollbackHandler) Routes(r chi.Router) {
	r.Post("/stacks/{id}/rollback", h.rollback)
}

type rollbackBody struct {
	Reason string `json:"reason"`
}

// rollback opens an update request that restores the stack's last applied configuration.
// Like POST /v1/requests it needs an Idempotency-Key and answers 202 with the request.
func (h *RollbackHandler) rollback(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, apperr.NotFound("stack %q not found", chi.URLParam(r, "id")))
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if err := requests.ValidateIdempotencyKey(key); err != nil {
		writeError(w, r, err)
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in rollbackBody
	if err := decodeStrict(body, &in); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Rollback(r.Context(), p, recovery.RollbackCommand{StackID: id, Reason: in.Reason, IdempotencyKey: key,
		RequestHash: requests.RequestHash(r.Method, r.URL.Path, body), CorrelationID: RequestIDFrom(r.Context())})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/requests/"+res.Request.ID.String())
	writeJSON(w, http.StatusAccepted, toRequestDTO(res.Request, false))
}
