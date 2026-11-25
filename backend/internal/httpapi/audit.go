package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
)

// AuditVerifier recomputes the audit hash chain over a seq range.
type AuditVerifier interface {
	Verify(ctx context.Context, fromSeq, toSeq int64) (audit.VerifyResult, error)
}

// AuditHandler serves /v1/audit.
type AuditHandler struct{ v AuditVerifier }

// NewAuditHandler returns an AuditHandler.
func NewAuditHandler(v AuditVerifier) *AuditHandler { return &AuditHandler{v: v} }

// Routes implements RouteRegistrar.
func (h *AuditHandler) Routes(r chi.Router) {
	r.Get("/audit/verify", h.verify)
}

// verify handles GET /v1/audit/verify?from_seq=&to_seq= (at most audit.MaxVerifyRange events per call).
func (h *AuditHandler) verify(w http.ResponseWriter, r *http.Request) {
	parse := func(name string, def int64) (int64, bool) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return def, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil && n >= 0
	}
	from, ok1 := parse("from_seq", 1)
	to, ok2 := parse("to_seq", 0)
	if !ok1 || !ok2 {
		writeError(w, r, badRequest("from_seq and to_seq must be non-negative integers"))
		return
	}
	from, to, err := audit.VerifyWindow(from, to)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.v.Verify(r.Context(), from, to)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
