package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
)

// Authenticator resolves the caller of an HTTP request.
//
// Implementations: DevAuthenticator (local development) and TokenAuthenticator
// (/v1/internal/*). GitHub OAuth sessions (WBS 4.4) and the GitHub OIDC verifier
// (WBS 4.10) plug in here without handler changes.
type Authenticator interface {
	Authenticate(r *http.Request) (identity.Principal, error)
}

// ErrUnauthenticated is returned by authenticators that reject a request.
var ErrUnauthenticated = errors.New("unauthenticated")

// DevAuthenticator treats every caller as one fixed local user. It is only used when
// IACP_AUTH_MODE=dev is set explicitly.
type DevAuthenticator struct{ Principal identity.Principal }

// Authenticate implements Authenticator.
func (a DevAuthenticator) Authenticate(*http.Request) (identity.Principal, error) {
	return a.Principal, nil
}

// DenyAuthenticator rejects every request.
type DenyAuthenticator struct{}

// Authenticate implements Authenticator.
func (DenyAuthenticator) Authenticate(*http.Request) (identity.Principal, error) {
	return identity.Principal{}, ErrUnauthenticated
}

// TokenAuthenticator accepts "Authorization: Bearer <token>" matching a shared secret,
// compared in constant time. It guards /v1/internal/* until GitHub OIDC (WBS 4.10).
// With an empty token it rejects everything.
type TokenAuthenticator struct {
	digest    [sha256.Size]byte
	principal identity.Principal
	enabled   bool
}

// NewTokenAuthenticator returns a TokenAuthenticator for token; callers authenticate as p.
func NewTokenAuthenticator(token string, p identity.Principal) *TokenAuthenticator {
	return &TokenAuthenticator{digest: sha256.Sum256([]byte(token)), principal: p, enabled: token != ""}
}

// Authenticate implements Authenticator.
func (a *TokenAuthenticator) Authenticate(r *http.Request) (identity.Principal, error) {
	presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !a.enabled || !ok || presented == "" {
		return identity.Principal{}, ErrUnauthenticated
	}
	// Hashing first makes the comparison constant-time regardless of token length.
	got := sha256.Sum256([]byte(presented))
	if subtle.ConstantTimeCompare(got[:], a.digest[:]) != 1 {
		return identity.Principal{}, ErrUnauthenticated
	}
	return a.principal, nil
}

// requireAuth rejects unauthenticated requests and stores the Principal in the context.
// The reason is logged, never returned: clients get a generic 401.
func requireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := auth.Authenticate(r)
			if err != nil {
				observability.Logger(r.Context()).InfoContext(r.Context(), "authentication failed", "error", err)
				writeError(w, r, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
					Message: "authentication required"})
				return
			}
			next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
		})
	}
}
