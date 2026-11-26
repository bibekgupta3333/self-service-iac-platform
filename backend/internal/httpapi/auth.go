package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
)

// Authenticator resolves the caller of an HTTP request.
//
// Implementations: DevAuthenticator (local development), and for machine endpoints
// TokenAuthenticator (shared token) and OIDCAuthenticator (GitHub Actions OIDC), combined
// with AnyAuthenticator. GitHub OAuth sessions (WBS 4.4) plug in here without handler changes.
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
// compared in constant time. It is the fallback for machine endpoints next to GitHub OIDC.
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

// TokenVerifier verifies a GitHub Actions OIDC token (implemented by auth.Verifier).
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (auth.Claims, error)
}

// OIDCAuthenticator accepts "Authorization: Bearer <GitHub Actions OIDC JWT>". The
// Principal carries the verified workflow run, and its Login is the job_workflow_ref.
type OIDCAuthenticator struct{ verifier TokenVerifier }

// NewOIDCAuthenticator returns an OIDCAuthenticator.
func NewOIDCAuthenticator(v TokenVerifier) *OIDCAuthenticator { return &OIDCAuthenticator{verifier: v} }

// Authenticate implements Authenticator.
func (a *OIDCAuthenticator) Authenticate(r *http.Request) (identity.Principal, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.Count(raw, ".") != 2 {
		return identity.Principal{}, ErrUnauthenticated
	}
	claims, err := a.verifier.Verify(r.Context(), raw)
	if err != nil {
		return identity.Principal{}, err
	}
	wf, err := claims.Identity()
	if err != nil {
		return identity.Principal{}, err
	}
	return identity.Principal{Login: wf.JobWorkflowRef, Type: identity.ActorGitHubActions, Workflow: &wf}, nil
}

// AnyAuthenticator tries each authenticator in order and returns the first success.
// It is used to accept GitHub OIDC tokens and the shared internal token side by side.
// A Forbidden error (a genuine identity that is not allowed) wins over plain failures.
type AnyAuthenticator []Authenticator

// Authenticate implements Authenticator.
func (as AnyAuthenticator) Authenticate(r *http.Request) (identity.Principal, error) {
	errs := make([]error, 0, len(as))
	for _, a := range as {
		p, err := a.Authenticate(r)
		if err == nil {
			return p, nil
		}
		if apperr.IsKind(err, apperr.KindForbidden) {
			return identity.Principal{}, err
		}
		errs = append(errs, err)
	}
	return identity.Principal{}, errors.Join(append(errs, ErrUnauthenticated)...)
}

// requireRoute scopes an OIDC caller to a route class (auth.RoutePolicies). It runs
// after requireAuth; callers authenticated with the shared token carry no workflow and pass.
func requireRoute(class auth.RouteClass) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := identity.FromContext(r.Context())
			if err == nil {
				err = auth.AuthorizeRoute(class, p.Workflow)
			}
			if err != nil {
				writeError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireAuth rejects unauthenticated requests and stores the Principal in the context.
// The reason is logged, never returned: clients get a generic 401. The exception is a
// verified identity that is not allowed (OIDC workflow_not_allowed), which gets its 403.
func requireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := auth.Authenticate(r)
			if apperr.IsKind(err, apperr.KindForbidden) {
				writeError(w, r, err)
				return
			}
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
