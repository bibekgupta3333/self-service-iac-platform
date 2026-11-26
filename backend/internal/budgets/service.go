package budgets

import (
	"context"
	"fmt"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// Repository reads what the policy context needs.
type Repository interface {
	// Environment returns the environment row by name (apperr NotFound when missing).
	Environment(ctx context.Context, name string) (Environment, error)
	// MonthlyBudget returns the team's budget for month, preferring a row for env over a
	// team-wide row; nil when the team has none (or the team is unknown).
	MonthlyBudget(ctx context.Context, team, env string, month time.Time) (*Budget, error)
	// YankedVersions lists every yanked module version, including all versions of a
	// yanked module.
	YankedVersions(ctx context.Context) ([]YankedVersion, error)
}

// Service builds the budget context for a stack.
type Service struct {
	repo  Repository
	clock platform.Clock
}

// NewService returns a Service.
func NewService(repo Repository, clock platform.Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

// Context returns the conftest --data document for the stack at path. The stack need
// not exist in the database yet: pr-validate asks for the context of a stack a pull
// request is about to create, so everything is derived from the path.
func (s *Service) Context(ctx context.Context, path string) (Document, error) {
	ref, err := ParseStackPath(path)
	if err != nil {
		return Document{}, err
	}
	env, err := s.repo.Environment(ctx, ref.Environment)
	if err != nil {
		return Document{}, err
	}
	now := s.clock.Now()
	budget, err := s.repo.MonthlyBudget(ctx, ref.Team, ref.Environment, MonthOf(now))
	if err != nil {
		return Document{}, fmt.Errorf("budget context: %w", err)
	}
	yanked, err := s.repo.YankedVersions(ctx)
	if err != nil {
		return Document{}, fmt.Errorf("budget context: %w", err)
	}
	return BuildContext(ref, env, budget, yanked, now), nil
}
