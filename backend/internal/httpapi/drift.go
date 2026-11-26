package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/drift"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// DriftRecorder is the machine side of the drift service (drift.Service).
type DriftRecorder interface {
	Record(ctx context.Context, by identity.Principal, r drift.Report) (drift.Recorded, error)
	DueStacks(ctx context.Context, limit int) ([]drift.DueStack, error)
}

// DriftIngestHandler serves the machine routes of drift.yml under /v1/internal:
// GET /drift/stacks (what to scan) and POST /drift (one result per stack).
type DriftIngestHandler struct{ svc DriftRecorder }

// NewDriftIngestHandler returns a DriftIngestHandler.
func NewDriftIngestHandler(svc DriftRecorder) *DriftIngestHandler {
	return &DriftIngestHandler{svc: svc}
}

// Routes implements RouteRegistrar. Mount it under /v1/internal.
func (h *DriftIngestHandler) Routes(r chi.Router) {
	r.With(requireRoute(auth.RouteDrift)).Get("/drift/stacks", h.stacks)
	r.With(requireRoute(auth.RouteDrift)).Post("/drift", h.record)
}

type dueStackDTO struct {
	StackID     uuid.UUID `json:"stack_id"`
	Path        string    `json:"path"`
	Team        string    `json:"team"`
	Environment string    `json:"environment"`
	Region      string    `json:"region"`
}

// stacks answers ?limit= (at most drift.MaxDueStacks).
func (h *DriftIngestHandler) stacks(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, r, badRequest("limit must be a positive integer"))
			return
		}
		limit = n
	}
	rows, err := h.svc.DueStacks(r.Context(), limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stacks": fp.Map(rows, func(d drift.DueStack) dueStackDTO {
		return dueStackDTO{StackID: d.ID, Path: d.Path, Team: d.Team, Environment: d.Environment, Region: d.Region}
	})})
}

type driftAcceptedDTO struct {
	Accepted bool      `json:"accepted"`
	ReportID uuid.UUID `json:"report_id"`
	Result   string    `json:"result"`
	Status   *string   `json:"status"`
	Replayed bool      `json:"replayed"`
}

func (h *DriftIngestHandler) record(w http.ResponseWriter, r *http.Request) {
	var in drift.Report
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
	var status *string
	if res.Entry.Status != nil {
		s := string(*res.Entry.Status)
		status = &s
	}
	writeJSON(w, http.StatusOK, driftAcceptedDTO{Accepted: true, ReportID: res.Entry.ID, Result: string(res.Entry.Result),
		Status: status, Replayed: res.Replayed})
}

// DriftUserService is the user side of the drift service (drift.Service).
type DriftUserService interface {
	List(ctx context.Context, by identity.Principal, stackID uuid.UUID, limit int) ([]drift.Entry, error)
	Resolve(ctx context.Context, by identity.Principal, id uuid.UUID, in drift.ResolveInput) (drift.Resolved, error)
}

// DriftHandler serves GET /v1/stacks/{id}/drift and POST /v1/drift/{id}/resolve.
type DriftHandler struct{ svc DriftUserService }

// NewDriftHandler returns a DriftHandler.
func NewDriftHandler(svc DriftUserService) *DriftHandler { return &DriftHandler{svc: svc} }

// Routes implements RouteRegistrar.
func (h *DriftHandler) Routes(r chi.Router) {
	r.Get("/stacks/{id}/drift", h.list)
	r.Post("/drift/{id}/resolve", h.resolve)
}

type driftReportDTO struct {
	ID                uuid.UUID       `json:"id"`
	StackID           uuid.UUID       `json:"stack_id"`
	StackPath         string          `json:"stack_path"`
	Result            drift.Result    `json:"result"`
	Status            *drift.Status   `json:"status"`
	TerraformExitCode int             `json:"terraform_exit_code"`
	Summary           drift.Summary   `json:"summary"`
	Changed           json.RawMessage `json:"changed"`
	PlanHash          string          `json:"plan_hash,omitempty"`
	LogURL            string          `json:"log_url,omitempty"`
	Error             string          `json:"error,omitempty"`
	DetectedAt        time.Time       `json:"detected_at"`
	ResolvedBy        *uuid.UUID      `json:"resolved_by"`
	ResolvedAt        *time.Time      `json:"resolved_at"`
	Reason            string          `json:"reason,omitempty"`
	RequestID         *uuid.UUID      `json:"request_id"`
}

func toDriftDTO(e drift.Entry) driftReportDTO {
	d := driftReportDTO{ID: e.ID, StackID: e.StackID, StackPath: e.StackPath, Result: e.Result, Status: e.Status,
		TerraformExitCode: e.TerraformExit, Summary: e.Summary, Changed: e.Changed, PlanHash: e.PlanHash, LogURL: e.LogURL,
		Error: e.Error, DetectedAt: e.DetectedAt.UTC(), ResolvedBy: e.ResolvedBy, Reason: e.ResolutionNote,
		RequestID: e.ResolutionReqID}
	if e.ResolvedAt != nil {
		t := e.ResolvedAt.UTC()
		d.ResolvedAt = &t
	}
	if len(d.Changed) == 0 {
		d.Changed = json.RawMessage(`[]`)
	}
	return d
}

func (h *DriftHandler) list(w http.ResponseWriter, r *http.Request) {
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
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, r, badRequest("limit must be a positive integer"))
			return
		}
		limit = n
	}
	rows, err := h.svc.List(r.Context(), p, id, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, List[driftReportDTO]{Data: fp.Map(rows, toDriftDTO)})
}

type driftRequestDTO struct {
	ID      uuid.UUID `json:"id"`
	ShortID string    `json:"short_id"`
	Status  string    `json:"status"`
	Link    string    `json:"link"`
}

type driftResolvedDTO struct {
	driftReportDTO
	Replayed bool             `json:"replayed"`
	Request  *driftRequestDTO `json:"request"`
	// Note says what is left to do by hand; only a codify has one.
	Note string `json:"note,omitempty"`
}

// codifyNote is what a codify leaves to the operator: the platform records the decision but
// does not turn the drift diff into module inputs (docs/06 §4).
const codifyNote = "Codify is recorded only. Submit an update request that sets the module inputs to the live " +
	"values; the next drift check closes the finding when the configuration matches."

func (h *DriftHandler) resolve(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, apperr.NotFound("drift report %q not found", chi.URLParam(r, "id")))
		return
	}
	var in drift.ResolveInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Resolve(r.Context(), p, id, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := driftResolvedDTO{driftReportDTO: toDriftDTO(res.Entry), Replayed: res.Replayed}
	if q := res.Request; q != nil {
		out.Request = &driftRequestDTO{ID: q.RequestID, ShortID: q.ShortID, Status: q.Status, Link: "/v1/requests/" + q.RequestID.String()}
	}
	if res.Entry.Status != nil && *res.Entry.Status == drift.StatusCodified {
		out.Note = codifyNote
	}
	writeJSON(w, http.StatusOK, out)
}
