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

// Stack is a stack as the API shows it.
type Stack struct {
	ID             uuid.UUID
	Team           string
	Environment    string
	Name           string
	Path           string
	Region         string
	Status         requests.StackStatus
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

// Filter is a Query bound to the user whose teams bound the result.
type Filter struct {
	Query
	Viewer uuid.UUID
}

// Repository is the persistence port. Both methods return only stacks the viewer may see:
// a member of the stack's team (any role), or a platform admin.
type Repository interface {
	List(ctx context.Context, f Filter) ([]Stack, error)
	Get(ctx context.Context, viewer, id uuid.UUID) (Stack, error)
}

// Service implements the stack read use cases.
type Service struct{ repo Repository }

// NewService returns a Service.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

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

func viewerOf(by identity.Principal) (uuid.UUID, error) {
	if by.UserID == uuid.Nil {
		return uuid.Nil, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
			Message: "stacks are shown to users"}
	}
	return by.UserID, nil
}

// List returns the stacks by's teams own, newest first, and the cursor of the next page
// (nil at the end).
func (s *Service) List(ctx context.Context, by identity.Principal, q Query) ([]Stack, *requests.Cursor, error) {
	viewer, err := viewerOf(by)
	if err != nil {
		return nil, nil, err
	}
	q, err = ValidateQuery(q)
	if err != nil {
		return nil, nil, err
	}
	want := q.Limit
	q.Limit++ // one extra row tells whether another page exists
	rows, err := s.repo.List(ctx, Filter{Query: q, Viewer: viewer})
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
	viewer, err := viewerOf(by)
	if err != nil {
		return Stack{}, err
	}
	st, err := s.repo.Get(ctx, viewer, id)
	if err != nil {
		return Stack{}, fmt.Errorf("get stack %s: %w", id, err)
	}
	return st, nil
}
