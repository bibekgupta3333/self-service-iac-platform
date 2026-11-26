package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// BudgetAdminService is the budget administration use case (budgets.AdminService).
type BudgetAdminService interface {
	Set(ctx context.Context, by identity.Principal, team, month string, in budgets.SetInput, correlationID string) (budgets.Setting, error)
	List(ctx context.Context, by identity.Principal, team, month string) ([]budgets.Setting, error)
	RecordActual(ctx context.Context, by identity.Principal, in budgets.ActualInput, correlationID string) (budgets.Setting, error)
}

// BudgetAdminHandler serves the admin budget routes under /v1/teams/{slug}/budgets, and
// the internal actual-spend route.
type BudgetAdminHandler struct{ svc BudgetAdminService }

// NewBudgetAdminHandler returns a BudgetAdminHandler.
func NewBudgetAdminHandler(svc BudgetAdminService) *BudgetAdminHandler {
	return &BudgetAdminHandler{svc: svc}
}

// Routes implements RouteRegistrar. Mount it under authenticated /v1: every route needs an
// entry in DefaultPolicies, and the service checks the caller's role in the target team.
func (h *BudgetAdminHandler) Routes(r chi.Router) {
	r.Get("/teams/{slug}/budgets", h.list)
	r.Get("/teams/{slug}/budgets/{month}", h.list)
	r.Put("/teams/{slug}/budgets/{month}", h.put)
}

// Internal returns the registrar of POST /budget-actuals, to mount under /v1/internal.
func (h *BudgetAdminHandler) Internal() RouteRegistrar { return budgetActualsRoutes{h} }

type budgetActualsRoutes struct{ h *BudgetAdminHandler }

// Routes implements RouteRegistrar.
func (b budgetActualsRoutes) Routes(r chi.Router) {
	r.With(requireRoute(auth.RouteBudgetActuals)).Post("/budget-actuals", b.h.recordActual)
}

type overrideDTO struct {
	GrantedBy string    `json:"granted_by"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

type budgetDTO struct {
	ID              string       `json:"id"`
	Team            string       `json:"team"`
	Environment     *string      `json:"environment"` // null = every environment
	Month           string       `json:"month"`       // YYYY-MM
	Currency        string       `json:"currency"`
	LimitUSD        float64      `json:"limit_usd"`
	SpentUSD        float64      `json:"spent_usd"`
	ActualUSD       *float64     `json:"actual_usd"`
	UsedUSD         float64      `json:"used_usd"`
	UtilisationPct  float64      `json:"utilisation_pct"`
	HardBlock       bool         `json:"hard_block"`
	AlertThresholds []int        `json:"alert_thresholds"`
	Override        *overrideDTO `json:"override"`
	UpdatedBy       *string      `json:"updated_by"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

func toBudgetDTO(s budgets.Setting) budgetDTO {
	d := budgetDTO{ID: s.ID.String(), Team: s.Team, Month: s.Month.Format("2006-01"), Currency: budgets.Currency,
		LimitUSD: s.LimitUSD, SpentUSD: s.SpentUSD, ActualUSD: s.ActualUSD, UsedUSD: s.UsedUSD(),
		UtilisationPct: s.Utilisation(), HardBlock: s.HardBlock, AlertThresholds: s.AlertThresholds, UpdatedAt: s.UpdatedAt.UTC()}
	if s.Environment != "" {
		env := s.Environment
		d.Environment = &env
	}
	if s.UpdatedBy != "" {
		by := s.UpdatedBy
		d.UpdatedBy = &by
	}
	if o := s.Override; o != nil {
		d.Override = &overrideDTO{GrantedBy: o.GrantedBy, Reason: o.Reason, ExpiresAt: o.ExpiresAt.UTC()}
	}
	return d
}

func (h *BudgetAdminHandler) put(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in budgets.SetInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	s, err := h.svc.Set(r.Context(), p, chi.URLParam(r, "slug"), chi.URLParam(r, "month"), in, RequestIDFrom(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBudgetDTO(s))
}

func (h *BudgetAdminHandler) list(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := h.svc.List(r.Context(), p, chi.URLParam(r, "slug"), chi.URLParam(r, "month"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]budgetDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, toBudgetDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *BudgetAdminHandler) recordActual(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in budgets.ActualInput
	if err := decodeBody(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	s, err := h.svc.RecordActual(r.Context(), p, in, RequestIDFrom(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBudgetDTO(s))
}
