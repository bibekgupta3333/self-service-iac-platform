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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/stacks"
)

// StackService is the stack read port consumed by StackHandler (stacks.Service).
type StackService interface {
	List(ctx context.Context, by identity.Principal, q stacks.Query) ([]stacks.Stack, *requests.Cursor, error)
	Get(ctx context.Context, by identity.Principal, id uuid.UUID) (stacks.Stack, error)
}

// StackHandler serves GET /v1/stacks and GET /v1/stacks/{id}.
type StackHandler struct{ svc StackService }

// NewStackHandler returns a StackHandler.
func NewStackHandler(svc StackService) *StackHandler { return &StackHandler{svc: svc} }

// Routes implements RouteRegistrar.
func (h *StackHandler) Routes(r chi.Router) {
	r.Get("/stacks", h.list)
	r.Get("/stacks/{id}", h.get)
}

type latestRequestDTO struct {
	ID        uuid.UUID       `json:"id"`
	ShortID   string          `json:"short_id"`
	Kind      requests.Kind   `json:"kind"`
	Status    requests.Status `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
}

// stackLockDTO is the platform lock on a stack. It stays visible after a failed release, so the
// team can see that the stack is still held, by which request, and until when.
type stackLockDTO struct {
	RequestID      uuid.UUID `json:"request_id"`
	RequestShortID string    `json:"request_short_id"`
	Holder         string    `json:"holder"`
	AcquiredAt     time.Time `json:"acquired_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type stackLinks struct {
	Self          string  `json:"self"`
	LatestRequest *string `json:"latest_request"`
}

type stackDTO struct {
	ID             uuid.UUID            `json:"id"`
	Team           string               `json:"team"`
	Environment    string               `json:"environment"`
	Name           string               `json:"name"`
	Path           string               `json:"path"`
	Region         string               `json:"region"`
	Status         requests.StackStatus `json:"status"`
	Health         requests.StackHealth `json:"health"`
	Lock           *stackLockDTO        `json:"lock"`
	IsEphemeral    bool                 `json:"is_ephemeral"`
	ExpiresAt      *time.Time           `json:"expires_at"`
	Outputs        json.RawMessage      `json:"outputs"`
	LastAppliedSHA string               `json:"last_applied_sha,omitempty"`
	LatestRequest  *latestRequestDTO    `json:"latest_request"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	Links          stackLinks           `json:"links"`
}

func toStackDTO(s stacks.Stack) stackDTO {
	d := stackDTO{
		ID: s.ID, Team: s.Team, Environment: s.Environment, Name: s.Name, Path: s.Path, Region: s.Region, Status: s.Status,
		Health: s.Health, IsEphemeral: s.IsEphemeral, ExpiresAt: s.ExpiresAt, Outputs: s.Outputs, LastAppliedSHA: s.LastAppliedSHA,
		CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(),
		Links: stackLinks{Self: "/v1/stacks/" + s.ID.String()},
	}
	if d.Health == "" {
		d.Health = requests.HealthUnknown
	}
	if l := s.Lock; l != nil {
		d.Lock = &stackLockDTO{RequestID: l.RequestID, RequestShortID: l.RequestShortID, Holder: l.Holder,
			AcquiredAt: l.AcquiredAt.UTC(), ExpiresAt: l.ExpiresAt.UTC()}
	}
	if len(d.Outputs) == 0 {
		d.Outputs = json.RawMessage(`{}`)
	}
	if lr := s.LatestRequest; lr != nil {
		d.LatestRequest = &latestRequestDTO{ID: lr.ID, ShortID: lr.ShortID, Kind: lr.Kind, Status: lr.Status, CreatedAt: lr.CreatedAt.UTC()}
		link := "/v1/requests/" + lr.ID.String()
		d.Links.LatestRequest = &link
	}
	return d
}

func (h *StackHandler) list(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	v := r.URL.Query()
	q := stacks.Query{Team: v.Get("team"), Environment: v.Get("env"), Status: requests.StackStatus(v.Get("status"))}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, r, badRequest("limit must be a positive integer"))
			return
		}
		q.Limit = n
	}
	if s := v.Get("cursor"); s != "" {
		c, err := requests.DecodeCursor(s)
		if err != nil {
			writeError(w, r, err)
			return
		}
		q.After = &c
	}
	rows, next, err := h.svc.List(r.Context(), p, q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := List[stackDTO]{Data: fp.Map(rows, toStackDTO)}
	if next != nil {
		s := next.Encode()
		out.NextCursor = &s
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *StackHandler) get(w http.ResponseWriter, r *http.Request) {
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
	s, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toStackDTO(s))
}
