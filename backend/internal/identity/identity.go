// Package identity defines who is calling the platform. Authenticators (in httpapi)
// produce a Principal; services use it for ownership and audit.
package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ActorType mirrors audit_events.actor_type.
type ActorType string

// Actor types.
const (
	ActorUser          ActorType = "user"
	ActorSystem        ActorType = "system"
	ActorGitHubActions ActorType = "github_actions"
	ActorGitHubWebhook ActorType = "github_webhook"
)

// Principal is an authenticated caller.
type Principal struct {
	UserID uuid.UUID // users.id; uuid.Nil for non-user callers
	Login  string    // GitHub login, or workflow ref for Actions callers
	Type   ActorType
	// PlatformAdmin is true for platform-wide admins (users.is_platform_admin) and for the
	// fixed user of dev auth mode. It gates platform-scoped routes (rbac.ActionAuditViewAll).
	PlatformAdmin bool
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

// MainRef is the default branch every mutating workflow must run from.
const MainRef = "refs/heads/main"

// File is the workflow file name in JobWorkflowRef, e.g. "apply.yml" from
// "owner/repo/.github/workflows/apply.yml@refs/heads/main"; empty if malformed. Pure.
func (w Workflow) File() string {
	_, rest, ok := strings.Cut(w.JobWorkflowRef, "/.github/workflows/")
	if !ok {
		return ""
	}
	file, _, _ := strings.Cut(rest, "@")
	return file
}

// OnMain reports whether both the run's ref and the workflow definition are main. Pure.
func (w Workflow) OnMain() bool {
	return w.Ref == MainRef && strings.HasSuffix(w.JobWorkflowRef, "@"+MainRef)
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
