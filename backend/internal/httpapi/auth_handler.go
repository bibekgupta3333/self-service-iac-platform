package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ratelimit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

// StateCookieName (base name) binds a login attempt to the browser that started it (login
// CSRF). Like the session cookie it gets the __Host- prefix when Secure.
const StateCookieName = "iacp_oauth_state"

// LoginService is the OAuth login port (oauth.Service).
type LoginService interface {
	Begin(ctx context.Context, returnTo string) (authURL, state string, err error)
	Complete(ctx context.Context, code, queryState, cookieState string, meta oauth.Meta) (oauth.Result, error)
	RedirectPolicy() oauth.RedirectPolicy
}

// SessionManager ends sessions and reports lifetimes (session.Service).
type SessionManager interface {
	Resolve(ctx context.Context, id string) (session.Record, error)
	Revoke(ctx context.Context, id string) error
	Policy() session.Policy
}

// ProfileSource loads the caller's profile (identity.UserStore).
type ProfileSource interface {
	Profile(ctx context.Context, userID uuid.UUID) (identity.Profile, error)
}

// EventRecorder appends stand-alone audit events (audit.Recorder).
type EventRecorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// AuthHandler serves /v1/auth/* and /v1/me. With a nil Login (dev mode) only /v1/me exists.
type AuthHandler struct {
	Login    LoginService
	Sessions SessionManager
	Profiles ProfileSource
	Audit    EventRecorder
	// Secure sets the Secure attribute on cookies (false only for plain-http loopback).
	Secure bool
	// Mode is echoed by /v1/me ("dev" or "github").
	Mode string
	// LoginLimiter bounds GET /v1/auth/login per remote address (nil: unlimited). Behind the
	// portal proxy the remote address is the portal's, so it acts as a global cap there.
	LoginLimiter *ratelimit.Limiter
}

const stateCookieMaxAge = 10 * time.Minute

// PublicRoutes mounts the unauthenticated login endpoints.
func (h *AuthHandler) PublicRoutes(r chi.Router) {
	if h.Login == nil {
		return
	}
	r.Get("/auth/login", h.login)
	r.Get("/auth/callback", h.callback)
	r.Post("/auth/logout", h.logout)
}

type publicRegistrar struct{ h *AuthHandler }

func (p publicRegistrar) Routes(r chi.Router) { p.h.PublicRoutes(r) }

// Public returns a RouteRegistrar for Deps.PublicHandlers.
func (h *AuthHandler) Public() RouteRegistrar { return publicRegistrar{h} }

// Routes implements RouteRegistrar for the authenticated group.
func (h *AuthHandler) Routes(r chi.Router) {
	r.Get("/me", h.me)
}

func (h *AuthHandler) sessionCookie() string {
	return session.CookieNameFor(session.CookieName, h.Secure)
}

func (h *AuthHandler) stateCookie(value string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{Name: session.CookieNameFor(StateCookieName, h.Secure), Value: value, Path: "/", HttpOnly: true, Secure: h.Secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge.Seconds())}
}

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	if h.LoginLimiter != nil {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ok, retry := h.LoginLimiter.AllowN(ip); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeError(w, r, &apperr.Error{Kind: apperr.KindRateLimited, Code: "rate_limited",
				Message: "too many login attempts; try again shortly"})
			return
		}
	}
	authURL, state, err := h.Login.Begin(r.Context(), r.URL.Query().Get("return_to"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, h.stateCookie(state, stateCookieMaxAge))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *AuthHandler) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var cookieState string
	if c, err := r.Cookie(session.CookieNameFor(StateCookieName, h.Secure)); err == nil {
		cookieState = c.Value
	}
	http.SetCookie(w, h.stateCookie("", -time.Second)) // single use, whatever happens
	w.Header().Set("Cache-Control", "no-store")

	var res oauth.Result
	var err error
	if q.Get("error") != "" { // the user declined on GitHub
		err = oauth.ErrInvalidState
	} else {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		res, err = h.Login.Complete(r.Context(), q.Get("code"), q.Get("state"), cookieState,
			oauth.Meta{IP: ip, CorrelationID: RequestIDFrom(r.Context())})
	}
	if err != nil {
		observability.Logger(r.Context()).InfoContext(r.Context(), "login failed", "error", err)
		def := h.Login.RedirectPolicy().Default
		http.Redirect(w, r, withQuery(def, "login_error", oauth.LoginErrorCode(err)), http.StatusFound)
		return
	}
	// Fixation hygiene: drop any session the browser already held, then set the new one.
	if old, err := r.Cookie(h.sessionCookie()); err == nil {
		_ = h.Sessions.Revoke(r.Context(), old.Value)
	}
	http.SetCookie(w, session.Cookie(res.SessionID, h.Secure, h.Sessions.Policy().AbsoluteTTL))
	http.Redirect(w, r, res.ReturnTo, http.StatusFound)
}

// withQuery appends one query parameter to a path-or-URL target. Pure.
func withQuery(target, key, value string) string {
	u, err := url.Parse(target)
	if err != nil {
		return "/"
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// logout is public on purpose: a browser whose session already expired must still be able
// to clear its cookie. With a live session the CSRF token is required and the session is
// revoked and audited; with a missing, expired or revoked one it only clears the cookie.
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	c, err := r.Cookie(h.sessionCookie())
	if err != nil || c.Value == "" {
		http.SetCookie(w, session.ClearCookie(h.Secure))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rec, err := h.Sessions.Resolve(r.Context(), c.Value)
	switch {
	case err == nil:
		if !session.ValidCSRF(c.Value, r.Header.Get(session.CSRFHeader)) {
			writeError(w, r, errCSRF)
			return
		}
		if err := h.Sessions.Revoke(r.Context(), c.Value); err != nil {
			writeError(w, r, err)
			return
		}
		h.auditLogout(r, rec)
	case isDeadSession(err):
		// Nothing to revoke: fall through and clear the cookie.
	default:
		writeError(w, r, &apperr.Error{Kind: apperr.KindUnavailable, Code: "auth_unavailable", Message: "session store unavailable"})
		return
	}
	http.SetCookie(w, session.ClearCookie(h.Secure))
	w.WriteHeader(http.StatusNoContent)
}

func isDeadSession(err error) bool {
	for _, e := range []error{session.ErrNotFound, session.ErrExpired, session.ErrIdle, session.ErrRevoked, session.ErrDisabled} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (h *AuthHandler) auditLogout(r *http.Request, rec session.Record) {
	if h.Audit == nil {
		return
	}
	p := rec.Principal()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if err := h.Audit.Record(r.Context(), audit.Event{ActorType: string(p.Type), ActorID: p.AuditActorID(), ActorIP: ip,
		Action: "auth.logout", SubjectType: "user", SubjectID: p.AuditActorID(), CorrelationID: RequestIDFrom(r.Context())}); err != nil {
		observability.Logger(r.Context()).ErrorContext(r.Context(), "audit logout failed", "error", err)
	}
}

type teamRoleDTO struct {
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type meDTO struct {
	ID              uuid.UUID     `json:"id"`
	GitHubLogin     string        `json:"github_login"`
	DisplayName     string        `json:"display_name"`
	Email           string        `json:"email,omitempty"`
	IsPlatformAdmin bool          `json:"is_platform_admin"`
	Teams           []teamRoleDTO `json:"teams"`
	AuthMode        string        `json:"auth_mode"`
	CSRFToken       string        `json:"csrf_token,omitempty"`
}

func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	prof, err := h.Profiles.Profile(r.Context(), p.UserID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	dto := meDTO{ID: prof.ID, GitHubLogin: prof.Login, DisplayName: prof.DisplayName, Email: prof.Email,
		IsPlatformAdmin: prof.IsPlatformAdmin || p.PlatformAdmin, Teams: []teamRoleDTO{}, AuthMode: h.Mode}
	for _, t := range prof.Teams {
		dto.Teams = append(dto.Teams, teamRoleDTO{Slug: t.Slug, Role: string(t.Role)})
	}
	if c, err := r.Cookie(h.sessionCookie()); err == nil && session.ValidID(c.Value) {
		dto.CSRFToken = session.CSRFToken(c.Value)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, dto)
}
