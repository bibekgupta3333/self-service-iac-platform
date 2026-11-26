package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

// RunService is the run-report use-case port consumed by RunHandler.
type RunService interface {
	Record(ctx context.Context, by identity.Principal, r runs.Report) (runs.Recorded, error)
}

// RunObserver is told about a run report after it was recorded (recovery classifies
// failed apply stages and schedules retries). It runs after the report committed; an
// error answers 503 so the reporting workflow sends the report again, which records it
// again idempotently, so an observer must be idempotent too.
type RunObserver interface {
	RunRecorded(ctx context.Context, by identity.Principal, r runs.Report) error
}

// RunHandler serves POST /v1/runs, the GitHub Actions callback (docs/03 §6.9).
// Mount it with Deps.PipelineHandlers so it uses the machine authenticator.
type RunHandler struct {
	svc       RunService
	observers []RunObserver
}

// NewRunHandler returns a RunHandler.
func NewRunHandler(svc RunService) *RunHandler { return &RunHandler{svc: svc} }

// WithObserver adds an observer of recorded reports.
func (h *RunHandler) WithObserver(o RunObserver) *RunHandler {
	h.observers = append(h.observers, o)
	return h
}

// Routes implements RouteRegistrar.
func (h *RunHandler) Routes(r chi.Router) {
	r.With(requireRoute(auth.RouteRuns)).Post("/runs", h.record)
}

type runAcceptedDTO struct {
	Accepted      bool        `json:"accepted"`
	RunID         uuid.UUID   `json:"run_id"`
	RunStatus     runs.Status `json:"run_status"`
	RequestStatus *string     `json:"request_status"`
}

func (h *RunHandler) record(w http.ResponseWriter, r *http.Request) {
	var in runs.Report
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
	for _, o := range h.observers {
		if err := o.RunRecorded(r.Context(), p, in); err != nil {
			observability.Logger(r.Context()).ErrorContext(r.Context(), "run report observer failed", "error", err)
			writeError(w, r, &apperr.Error{Kind: apperr.KindUnavailable, Code: "run_follow_up_failed",
				Message: "the report was recorded but its follow-up failed; send it again"})
			return
		}
	}
	writeJSON(w, http.StatusOK, runAcceptedDTO{Accepted: true, RunID: res.RunID, RunStatus: res.RunStatus, RequestStatus: res.RequestStatus})
}
