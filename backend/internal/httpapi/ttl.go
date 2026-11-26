package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

// TTLExtender is the user-facing TTL port (ttl.Service).
type TTLExtender interface {
	Extend(ctx context.Context, by identity.Principal, id uuid.UUID, hours int, correlationID string) (ttl.ExtendResult, error)
}

// TTLHandler serves POST /v1/stacks/{id}/ttl/extend.
type TTLHandler struct{ svc TTLExtender }

// NewTTLHandler returns a TTLHandler.
func NewTTLHandler(svc TTLExtender) *TTLHandler { return &TTLHandler{svc: svc} }

// Routes implements RouteRegistrar. Mount it under authenticated /v1.
func (h *TTLHandler) Routes(r chi.Router) {
	r.Post("/stacks/{id}/ttl/extend", h.extend)
}

type extendBody struct {
	Hours int `json:"hours"`
}

type extendDTO struct {
	StackID                 uuid.UUID `json:"stack_id"`
	ExpiresAt               time.Time `json:"expires_at"`
	ExtendedHours           int       `json:"extended_hours"`
	RemainingExtensionHours int       `json:"remaining_extension_hours"`
}

func (h *TTLHandler) extend(w http.ResponseWriter, r *http.Request) {
	id, err := stackIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in extendBody
	if err := decodeBody(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	if in.Hours == 0 {
		writeError(w, r, requiredField("/hours"))
		return
	}
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Extend(r.Context(), p, id, in.Hours, RequestIDFrom(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, extendDTO{StackID: id, ExpiresAt: res.ExpiresAt.UTC(), ExtendedHours: res.ExtendedHours,
		RemainingExtensionHours: res.RemainingExtHours})
}

// TTLReaper is the machine-facing TTL port for the hourly reaper (ttl.Service).
type TTLReaper interface {
	Due(ctx context.Context) ([]ttl.DueItem, error)
	Warn(ctx context.Context, id uuid.UUID, correlationID string) (ttl.WarnResult, error)
	Teardown(ctx context.Context, id uuid.UUID, correlationID string) (ttl.DestroyResult, error)
}

// TTLReaperHandler serves /v1/internal/ttl/* for ttl-reaper.yml.
type TTLReaperHandler struct{ svc TTLReaper }

// NewTTLReaperHandler returns a TTLReaperHandler.
func NewTTLReaperHandler(svc TTLReaper) *TTLReaperHandler { return &TTLReaperHandler{svc: svc} }

// Routes implements RouteRegistrar. Mount it under /v1/internal. Only ttl-reaper.yml on
// main from its schedule or a dispatch (or the shared token) may use them.
func (h *TTLReaperHandler) Routes(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(requireRoute(auth.RouteTTL))
		g.Get("/ttl/due", h.due)
		g.Post("/ttl/{id}/warn", h.warn)
		g.Post("/ttl/{id}/teardown", h.teardown)
	})
}

type dueStackDTO struct {
	StackID     uuid.UUID `json:"stack_id"`
	Path        string    `json:"path"`
	Team        string    `json:"team"`
	Environment string    `json:"environment"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type dueDTO struct {
	// Warnings are stacks inside the 24 h window that were not warned yet.
	Warnings []dueStackDTO `json:"warnings"`
	// Teardowns are expired stacks without an open destroy request, not in backoff and not blocked.
	Teardowns []dueStackDTO `json:"teardowns"`
	// Blocked are expired stacks that used up their TTL destroy attempts. They are reported
	// only: calling teardown for them opens nothing, and they never count against a run's cap.
	Blocked []dueStackDTO `json:"blocked"`
}

func toDueStack(d ttl.DueItem) dueStackDTO {
	return dueStackDTO{StackID: d.Stack.ID, Path: d.Stack.Path, Team: d.Stack.Team, Environment: d.Stack.Environment,
		ExpiresAt: d.Stack.ExpiresAt.UTC()}
}

func (h *TTLReaperHandler) due(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.Due(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	isPhase := func(p ttl.Phase) func(ttl.DueItem) bool { return func(d ttl.DueItem) bool { return d.Phase == p } }
	writeJSON(w, http.StatusOK, dueDTO{
		Warnings:  fp.Map(fp.Filter(items, isPhase(ttl.PhaseWarn)), toDueStack),
		Teardowns: fp.Map(fp.Filter(items, isPhase(ttl.PhaseTeardown)), toDueStack),
		Blocked:   fp.Map(fp.Filter(items, isPhase(ttl.PhaseBlocked)), toDueStack),
	})
}

type warnDTO struct {
	Warned bool   `json:"warned"`
	Reason string `json:"reason,omitempty"`
}

func (h *TTLReaperHandler) warn(w http.ResponseWriter, r *http.Request) {
	id, err := stackIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Warn(r.Context(), id, RequestIDFrom(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, warnDTO{Warned: res.Warned, Reason: res.Reason})
}

type teardownDTO struct {
	// Status is "created", or "already_pending" when a destroy request was already open.
	Status    string     `json:"status"`
	RequestID *uuid.UUID `json:"request_id,omitempty"`
	ShortID   string     `json:"short_id,omitempty"`
	Queued    bool       `json:"queued,omitempty"`
}

func (h *TTLReaperHandler) teardown(w http.ResponseWriter, r *http.Request) {
	id, err := stackIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Teardown(r.Context(), id, RequestIDFrom(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !res.Created {
		writeJSON(w, http.StatusOK, teardownDTO{Status: "already_pending"})
		return
	}
	writeJSON(w, http.StatusAccepted, teardownDTO{Status: "created", RequestID: &res.RequestID, ShortID: res.ShortID, Queued: res.Queued})
}
