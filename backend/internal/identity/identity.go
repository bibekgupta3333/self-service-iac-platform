// Package identity defines who is calling the platform. Authenticators (in httpapi)
// produce a Principal; services use it for ownership and audit.
package identity

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ActorType mirrors audit_events.actor_type.
type ActorType string

// Actor types.
const (
	ActorUser          ActorType = "user"
	ActorSystem        ActorType = "system"
	ActorGitHubActions ActorType = "github_actions"
)

// Principal is an authenticated caller.
type Principal struct {
	UserID uuid.UUID // users.id; uuid.Nil for non-user callers
	Login  string    // GitHub login, or workflow ref for Actions callers
	Type   ActorType
	// Workflow is set when the caller proved a GitHub Actions OIDC identity. Callers
	// using the shared internal token have none.
	Workflow *Workflow
}

// Workflow is the verified GitHub Actions run behind an OIDC token.
type Workflow struct {
	Repository     string // owner/name
	Ref            string // refs/heads/main, refs/pull/812/merge
	JobWorkflowRef string // owner/name/.github/workflows/apply.yml@refs/heads/main
	SHA            string
	EventName      string
	RunID          int64
	RunAttempt     int
}

// AuditActorID is the actor_id recorded in audit events.
func (p Principal) AuditActorID() string {
	if p.UserID != uuid.Nil {
		return p.UserID.String()
	}
	return p.Login
}

type ctxKey struct{}

// ErrNoPrincipal is returned when a context carries no authenticated caller.
var ErrNoPrincipal = errors.New("no authenticated principal in context")

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the Principal stored in ctx.
func FromContext(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	if !ok {
		return Principal{}, ErrNoPrincipal
	}
	return p, nil
}
