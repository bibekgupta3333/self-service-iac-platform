package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
)

// BudgetContextService builds the conftest policy context (budgets.Service).
type BudgetContextService interface {
	Context(ctx context.Context, stackPath string) (budgets.Document, error)
}

// BudgetContextHandler serves GET /v1/internal/budget-context for pr-validate and apply.
type BudgetContextHandler struct{ svc BudgetContextService }

// NewBudgetContextHandler returns a BudgetContextHandler.
func NewBudgetContextHandler(svc BudgetContextService) *BudgetContextHandler {
	return &BudgetContextHandler{svc: svc}
}

// Routes implements RouteRegistrar. Mount it under /v1/internal.
func (h *BudgetContextHandler) Routes(r chi.Router) {
	r.With(requireRoute(auth.RouteBudgetContext)).Get("/budget-context", h.get)
}

// get answers ?stack_path=environments/<env>/<team>/<stack> (docs/03 §6.9). ?stack= is
// accepted as an alias; the two must agree when both are sent.
func (h *BudgetContextHandler) get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path, alias := q.Get("stack_path"), q.Get("stack")
	switch {
	case path == "" && alias == "":
		writeError(w, r, apperr.Invalid("invalid_stack_path", "stack_path query parameter is required"))
		return
	case path == "":
		path = alias
	case alias != "" && alias != path:
		writeError(w, r, apperr.Invalid("invalid_stack_path", "stack and stack_path disagree"))
		return
	}
	doc, err := h.svc.Context(r.Context(), path)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}
