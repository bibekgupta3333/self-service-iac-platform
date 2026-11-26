package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
)

// LockService is the stack-lock use-case port consumed by LockHandler.
type LockService interface {
	Acquire(ctx context.Context, cmd locks.AcquireCommand) (locks.Result, error)
	Heartbeat(ctx context.Context, cmd locks.TokenCommand) (locks.Lock, error)
	Release(ctx context.Context, cmd locks.TokenCommand) (locks.ReleaseResult, error)
	Get(ctx context.Context, stackID uuid.UUID) (locks.Lock, error)
}

// LockHandler serves /v1/internal/stacks/{id}/lock for the worker and GitHub Actions.
type LockHandler struct{ svc LockService }

// NewLockHandler returns a LockHandler.
func NewLockHandler(svc LockService) *LockHandler { return &LockHandler{svc: svc} }

// Routes implements RouteRegistrar. Mount it under /v1/internal.
// Only apply/destroy workflows on main (or the shared token) may use them.
func (h *LockHandler) Routes(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(requireRoute(auth.RouteLocks))
		g.Get("/stacks/{id}/lock", h.get)
		g.Post("/stacks/{id}/lock", h.acquire)
		g.Post("/stacks/{id}/lock/heartbeat", h.heartbeat)
		g.Delete("/stacks/{id}/lock", h.release)
	})
}

type lockDTO struct {
	StackID      uuid.UUID `json:"stack_id"`
	RequestID    uuid.UUID `json:"request_id"`
	Holder       string    `json:"holder"`
	FencingToken int64     `json:"fencing_token"`
	AcquiredAt   time.Time `json:"acquired_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func toLockDTO(l locks.Lock) lockDTO {
	return lockDTO{StackID: l.StackID, RequestID: l.RequestID, Holder: l.Holder, FencingToken: l.FencingToken,
		AcquiredAt: l.AcquiredAt.UTC(), ExpiresAt: l.ExpiresAt.UTC()}
}

type acquireDTO struct {
	Lock         locks.Outcome `json:"lock"`
	QueuedBehind *uuid.UUID    `json:"queued_behind"`
	Current      lockDTO       `json:"current"`
}

type releaseDTO struct {
	Released lockDTO  `json:"released"`
	Promoted *lockDTO `json:"promoted"`
}

type acquireBody struct {
	RequestID uuid.UUID `json:"request_id"`
}

type tokenBody struct {
	RequestID    uuid.UUID `json:"request_id"`
	FencingToken int64     `json:"fencing_token"`
}

func stackIDParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, apperr.NotFound("stack %q not found", chi.URLParam(r, "id"))
	}
	return id, nil
}

func (h *LockHandler) get(w http.ResponseWriter, r *http.Request) {
	stackID, err := stackIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	l, err := h.svc.Get(r.Context(), stackID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLockDTO(l))
}

func (h *LockHandler) acquire(w http.ResponseWriter, r *http.Request) {
	stackID, err := stackIDParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in acquireBody
	if err := decodeBody(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	if in.RequestID == uuid.Nil {
		writeError(w, r, requiredField("/request_id"))
		return
	}
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Acquire(r.Context(), locks.AcquireCommand{StackID: stackID, RequestID: in.RequestID, By: p,
		CorrelationID: RequestIDFrom(r.Context())})
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Outcome == locks.OutcomeQueued {
		status = http.StatusAccepted
	}
	writeJSON(w, status, acquireDTO{Lock: res.Outcome, QueuedBehind: res.QueuedBehind, Current: toLockDTO(res.Lock)})
}

func (h *LockHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	cmd, err := tokenCommand(r, func(in *tokenBody) error { return decodeBody(r, in) })
	if err != nil {
		writeError(w, r, err)
		return
	}
	l, err := h.svc.Heartbeat(r.Context(), cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLockDTO(l))
}

// release takes request_id and fencing_token as query parameters (DELETE has no body).
func (h *LockHandler) release(w http.ResponseWriter, r *http.Request) {
	cmd, err := tokenCommand(r, func(in *tokenBody) error {
		q := r.URL.Query()
		if v := q.Get("request_id"); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				return badRequest("request_id must be a UUID")
			}
			in.RequestID = id
		}
		if v := q.Get("fencing_token"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return badRequest("fencing_token must be an integer")
			}
			in.FencingToken = n
		}
		return nil
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.svc.Release(r.Context(), cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := releaseDTO{Released: toLockDTO(res.Released)}
	if res.Promoted != nil {
		p := toLockDTO(*res.Promoted)
		out.Promoted = &p
	}
	writeJSON(w, http.StatusOK, out)
}

// tokenCommand builds a TokenCommand from the path, the caller and read (body or query).
func tokenCommand(r *http.Request, read func(*tokenBody) error) (locks.TokenCommand, error) {
	stackID, err := stackIDParam(r)
	if err != nil {
		return locks.TokenCommand{}, err
	}
	var in tokenBody
	if err := read(&in); err != nil {
		return locks.TokenCommand{}, err
	}
	switch {
	case in.RequestID == uuid.Nil:
		return locks.TokenCommand{}, requiredField("/request_id")
	case in.FencingToken <= 0:
		return locks.TokenCommand{}, requiredField("/fencing_token")
	}
	p, err := identity.FromContext(r.Context())
	if err != nil {
		return locks.TokenCommand{}, err
	}
	return locks.TokenCommand{StackID: stackID, RequestID: in.RequestID, FencingToken: in.FencingToken, By: p,
		CorrelationID: RequestIDFrom(r.Context())}, nil
}

// decodeBody reads and strictly decodes a JSON body into v.
func decodeBody(r *http.Request, v any) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	return decodeStrict(body, v)
}

func requiredField(pointer string) error {
	return apperr.Invalid("validation_failed", pointer[1:]+" is required",
		apperr.FieldError{Pointer: pointer, Code: "required", Message: "is required"})
}
