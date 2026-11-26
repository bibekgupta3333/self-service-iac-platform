package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/recovery"
)

// SurgeryService records break-glass state surgery (recovery.SurgeryRecorder).
type SurgeryService interface {
	Record(ctx context.Context, by identity.Principal, r recovery.SurgeryReport) (recovery.Recorded, error)
}

// SurgeryHandler serves POST /v1/internal/state-surgery, the audit report of the
// break-glass state-surgery.yml workflow. Mount it with Deps.InternalHandlers.
type SurgeryHandler struct{ svc SurgeryService }

// NewSurgeryHandler returns a SurgeryHandler.
func NewSurgeryHandler(svc SurgeryService) *SurgeryHandler { return &SurgeryHandler{svc: svc} }

// Routes implements RouteRegistrar.
func (h *SurgeryHandler) Routes(r chi.Router) {
	r.With(requireRoute(auth.RouteStateSurgery)).Post("/state-surgery", h.record)
}

func (h *SurgeryHandler) record(w http.ResponseWriter, r *http.Request) {
	var in recovery.SurgeryReport
	if err := decodeBody(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Record(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, res)
}
