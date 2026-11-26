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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RequestService is the request use-case port consumed by RequestHandler.
type RequestService interface {
	Create(ctx context.Context, by identity.Principal, cmd requests.CreateCommand) (requests.CreateResult, error)
	Get(ctx context.Context, id uuid.UUID) (*requests.Request, error)
	List(ctx context.Context, f requests.ListFilter) ([]*requests.Request, *requests.Cursor, error)
}

// RequestHandler serves /v1/requests.
type RequestHandler struct{ svc RequestService }

// NewRequestHandler returns a RequestHandler.
func NewRequestHandler(svc RequestService) *RequestHandler { return &RequestHandler{svc: svc} }

// Routes implements RouteRegistrar.
func (h *RequestHandler) Routes(r chi.Router) {
	r.Post("/requests", h.create)
	r.Get("/requests", h.list)
	r.Get("/requests/{id}", h.get)
}

type requestLinks struct {
	Self   string `json:"self"`
	Events string `json:"events"`
}

type requestDTO struct {
	ID            uuid.UUID         `json:"id"`
	ShortID       string            `json:"short_id"`
	Kind          requests.Kind     `json:"kind"`
	Status        requests.Status   `json:"status"`
	Stack         requests.StackRef `json:"stack"`
	QueuedBehind  *uuid.UUID        `json:"queued_behind"`
	Justification string            `json:"justification,omitempty"`
	Spec          json.RawMessage   `json:"spec,omitempty"`
	CreatedBy     uuid.UUID         `json:"created_by"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	CompletedAt   *time.Time        `json:"completed_at"`
	GitHub        *requests.PRLink  `json:"github"`
	FailureReason string            `json:"failure_reason,omitempty"`
	Links         requestLinks      `json:"links"`
	Allowed       []requests.Event  `json:"allowed_events"`
}

func toRequestDTO(r *requests.Request, withSpec bool) requestDTO {
	self := "/v1/requests/" + r.ID.String()
	d := requestDTO{
		ID: r.ID, ShortID: r.ShortID, Kind: r.Kind, Status: r.Status, Stack: r.Stack, QueuedBehind: r.QueuedBehind,
		Justification: r.Justification, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(), CompletedAt: r.CompletedAt, GitHub: r.PR, FailureReason: r.FailureReason,
		Links:   requestLinks{Self: self, Events: self + "/events"},
		Allowed: requests.AllowedEvents(r.Status),
	}
	if withSpec {
		d.Spec = r.Spec
	}
	return d
}

func (h *RequestHandler) create(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
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
	var in requests.CreateInput
	if err := decodeStrict(body, &in); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Create(r.Context(), p, requests.CreateCommand{
		Input:          in,
		IdempotencyKey: key,
		RequestHash:    requests.RequestHash(r.Method, r.URL.Path, body),
		CorrelationID:  RequestIDFrom(r.Context()),
	})
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

func (h *RequestHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, apperr.NotFound("request %q not found", chi.URLParam(r, "id")))
		return
	}
	req, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRequestDTO(req, true))
}

func (h *RequestHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := requests.ListFilter{Team: q.Get("team"), Status: requests.Status(q.Get("status")), Kind: requests.Kind(q.Get("kind"))}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, r, badRequest("limit must be a positive integer"))
			return
		}
		f.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		c, err := requests.DecodeCursor(v)
		if err != nil {
			writeError(w, r, err)
			return
		}
		f.After = &c
	}
	if q.Get("mine") == "true" {
		p, err := identity.FromContext(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		f.CreatedBy = &p.UserID
	}
	rows, next, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := List[requestDTO]{Data: fp.Map(rows, func(x *requests.Request) requestDTO { return toRequestDTO(x, false) })}
	if next != nil {
		s := next.Encode()
		out.NextCursor = &s
	}
	writeJSON(w, http.StatusOK, out)
}
