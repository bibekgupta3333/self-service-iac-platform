package httpapi

import (
	"net/http"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// Authenticator resolves the caller of an HTTP request.
//
// Implementations: DevAuthenticator (local development). GitHub OAuth sessions
// (WBS 4.4) and GitHub OIDC JWTs for /v1/internal/* (WBS 4.10) plug in here.
type Authenticator interface {
	Authenticate(r *http.Request) (identity.Principal, error)
}

// DevAuthenticator treats every caller as one fixed local user. Only for local development.
type DevAuthenticator struct{ Principal identity.Principal }

// Authenticate implements Authenticator.
func (a DevAuthenticator) Authenticate(*http.Request) (identity.Principal, error) {
	return a.Principal, nil
}

// requireAuth rejects unauthenticated requests and stores the Principal in the context.
func requireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := auth.Authenticate(r)
			if err != nil {
				writeError(w, r, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated", Message: err.Error()})
				return
			}
			next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
		})
	}
}
