// Package stacks is the read side of stacks: listing and showing the stacks a user may
// see. Stacks are only ever changed by requests (internal/requests), never directly.
package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RequestRef is the most recent request against a stack.
type RequestRef struct {
	ID        uuid.UUID
	ShortID   string
	Kind      requests.Kind
	Status    requests.Status
	CreatedAt time.Time
}

// LockInfo is the platform lock currently held on a stack (docs/06 §3). The fencing token
// is deliberately absent: it authorizes pipeline reports, so it is for the pipeline only.
type LockInfo struct {
	RequestID      uuid.UUID
	RequestShortID string
	Holder         string
	AcquiredAt     time.Time
	ExpiresAt      time.Time
}

// Stack is a stack as the API shows it.
type Stack struct {
	ID             uuid.UUID
	Team           string
	Environment    string
	Name           string
	Path           string
	Region         string
	Status         requests.StackStatus
	Health         requests.StackHealth // what the post-apply smoke test said; unknown before one ran
	Lock           *LockInfo            // nil when no request holds the stack
	IsEphemeral    bool
	ExpiresAt      *time.Time
	Outputs        json.RawMessage // the non-sensitive outputs of the last apply
	LastAppliedSHA string
	LatestRequest  *RequestRef // nil when the stack has no request (cannot happen for a created stack)
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Query is a list call as the HTTP layer parsed it.
type Query struct {
	Team        string
	Environment string
	Status      requests.StackStatus
	Limit       int
	After       *requests.Cursor
}

// Filter is a Query bound to the teams the caller may see (identity.Visibility, the same
// helper the request routes use, so one caller sees the same teams everywhere).
type Filter struct {
	Query
	Visibility identity.Visibility
}

// Repository is the persistence port. Both methods return only stacks of visible teams.
type Repository interface {
	List(ctx context.Context, f Filter) ([]Stack, error)
	Get(ctx context.Context, v identity.Visibility, id uuid.UUID) (Stack, error)
}

// TeamVisibility resolves the teams a caller may read (identity.UserStore).
type TeamVisibility interface {
	VisibleTeams(ctx context.Context, p identity.Principal) (identity.Visibility, error)
}

// Service implements the stack read use cases.
type Service struct {
	repo  Repository
	teams TeamVisibility
}

// NewService returns a Service. teams decides which stacks a caller sees.
func NewService(repo Repository, teams TeamVisibility) *Service {
	return &Service{repo: repo, teams: teams}
}

// MaxPageSize bounds List.
const MaxPageSize = 200

// DefaultPageSize is used when a query sets no limit.
const DefaultPageSize = 50

var environments = []string{"development", "staging", "production"}

var teamRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}$`)

// ValidateQuery checks a list query and applies the page size bounds. Pure.
func ValidateQuery(q Query) (Query, error) {
	invalid := func(format string, args ...any) error {
		return &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
	}
	switch {
	case q.Team != "" && !teamRe.MatchString(q.Team):
		return q, invalid("team must be a team slug")
	case q.Environment != "" && !slices.Contains(environments, q.Environment):
		return q, invalid("unknown environment %q", q.Environment)
	case q.Status != "" && !q.Status.Valid():
		return q, invalid("unknown status %q", q.Status)
	case q.Limit <= 0:
		q.Limit = DefaultPageSize
	case q.Limit > MaxPageSize:
		q.Limit = MaxPageSize
	}
	return q, nil
}

// visibilityOf resolves what by may see; callers that are not users see nothing.
func (s *Service) visibilityOf(ctx context.Context, by identity.Principal) (identity.Visibility, error) {
	if by.UserID == uuid.Nil {
		return identity.Visibility{}, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
			Message: "stacks are shown to users"}
	}
	v, err := s.teams.VisibleTeams(ctx, by)
	if err != nil {
		return identity.Visibility{}, fmt.Errorf("resolve visible teams: %w", err)
	}
	return v, nil
}

// List returns the stacks by's teams own, newest first, and the cursor of the next page
// (nil at the end).
func (s *Service) List(ctx context.Context, by identity.Principal, q Query) ([]Stack, *requests.Cursor, error) {
	v, err := s.visibilityOf(ctx, by)
	if err != nil {
		return nil, nil, err
	}
	q, err = ValidateQuery(q)
	if err != nil {
		return nil, nil, err
	}
	want := q.Limit
	q.Limit++ // one extra row tells whether another page exists
	rows, err := s.repo.List(ctx, Filter{Query: q, Visibility: v})
	if err != nil {
		return nil, nil, fmt.Errorf("list stacks: %w", err)
	}
	if len(rows) <= want {
		return rows, nil, nil
	}
	rows = rows[:want]
	last := rows[len(rows)-1]
	return rows, &requests.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
}

// Get returns one stack. A stack the caller may not see is reported as not found, so its
// existence is not revealed.
func (s *Service) Get(ctx context.Context, by identity.Principal, id uuid.UUID) (Stack, error) {
	v, err := s.visibilityOf(ctx, by)
	if err != nil {
		return Stack{}, err
	}
	st, err := s.repo.Get(ctx, v, id)
	if err != nil {
		return Stack{}, fmt.Errorf("get stack %s: %w", id, err)
	}
	return st, nil
}
