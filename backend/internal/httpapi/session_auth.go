package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

// SessionResolver validates a raw session id (session.Service).
type SessionResolver interface {
	Resolve(ctx context.Context, id string) (session.Record, error)
}

// SessionAuthenticator authenticates browser users by their iacp_session cookie. Every
// unsafe method (anything but GET, HEAD, OPTIONS) must also carry the session's CSRF token
// in X-CSRF-Token; the check lives here so no route can forget it.
type SessionAuthenticator struct {
	sessions SessionResolver
	cookie   string
}

// NewSessionAuthenticator returns a SessionAuthenticator.
// secure must match the handler's cookie setting, since it selects the cookie name.
func NewSessionAuthenticator(s SessionResolver, secure bool) *SessionAuthenticator {
	return &SessionAuthenticator{sessions: s, cookie: session.CookieNameFor(session.CookieName, secure)}
}

// errCSRF is the 403 for a missing or wrong CSRF token.
var errCSRF = &apperr.Error{Kind: apperr.KindForbidden, Code: "csrf_token_invalid",
	Message: "a valid X-CSRF-Token header is required for this request (read it from GET /v1/me)"}

// SessionChecker validates a session without sliding its idle window (session.Service).
type SessionChecker interface {
	Check(ctx context.Context, id string) (session.Record, error)
}

// StreamSession tells an open stream whether the session behind its request is still live.
type StreamSession interface {
	// Alive returns whether the session's user is a platform admin right now, and an error
	// once the session has been revoked, has expired, or its user is disabled (or the store
	// cannot say). The stream ends when the admin flag differs from the one the request was
	// authenticated with, because that flag decides which teams the stream may read.
	Alive(ctx context.Context, r *http.Request) (platformAdmin bool, err error)
}

// Alive implements StreamSession. It reads the session cookie of the original request again
// and checks it against the store; it does not slide the idle window, so a stream does not
// keep an idle session alive.
func (a *SessionAuthenticator) Alive(ctx context.Context, r *http.Request) (bool, error) {
	c, err := r.Cookie(a.cookie)
	if err != nil || c.Value == "" {
		return false, ErrUnauthenticated
	}
	check := a.sessions.Resolve
	if sc, ok := a.sessions.(SessionChecker); ok {
		check = sc.Check
	}
	rec, err := check(ctx, c.Value)
	if err != nil {
		return false, err
	}
	return rec.PlatformAdmin, nil
}

// Authenticate implements Authenticator.
func (a *SessionAuthenticator) Authenticate(r *http.Request) (identity.Principal, error) {
	c, err := r.Cookie(a.cookie)
	if err != nil || c.Value == "" {
		return identity.Principal{}, ErrUnauthenticated
	}
	rec, err := a.sessions.Resolve(r.Context(), c.Value)
	switch {
	case err == nil:
	case errors.Is(err, session.ErrNotFound), errors.Is(err, session.ErrExpired), errors.Is(err, session.ErrIdle),
		errors.Is(err, session.ErrRevoked), errors.Is(err, session.ErrDisabled):
		return identity.Principal{}, ErrUnauthenticated
	default:
		return identity.Principal{}, &apperr.Error{Kind: apperr.KindUnavailable, Code: "auth_unavailable",
			Message: "session store unavailable"}
	}
	if !session.SafeMethod(r.Method) && !session.ValidCSRF(c.Value, r.Header.Get(session.CSRFHeader)) {
		observability.Logger(r.Context()).WarnContext(r.Context(), "csrf token rejected",
			"user", rec.Login, "method", r.Method, "path", r.URL.Path)
		return identity.Principal{}, errCSRF
	}
	return rec.Principal(), nil
}
