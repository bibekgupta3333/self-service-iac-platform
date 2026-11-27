package recovery

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// StackTargets loads an existing stack (requests.PostgresRepository).
type StackTargets interface {
	StackTarget(ctx context.Context, id uuid.UUID) (requests.StackTarget, error)
}

// Memberships resolves the caller's role in a team (requests.PostgresRepository).
type Memberships interface {
	RoleInTeam(ctx context.Context, teamSlug string, userID uuid.UUID) (rbac.Role, error)
}

// History reads a stack's request history (PostgresRepository).
type History interface {
	// OpenRequests counts the stack's requests that are not terminal.
	OpenRequests(ctx context.Context, stackID uuid.UUID) (int, error)
	// AppliedSpecs lists up to limit applied create and update requests of the stack,
	// newest first.
	AppliedSpecs(ctx context.Context, stackID uuid.UUID, limit int) ([]AppliedSpec, error)
}

// RequestCreator is the normal request path (requests.Service).
type RequestCreator interface {
	Create(ctx context.Context, by identity.Principal, cmd requests.CreateCommand) (requests.CreateResult, error)
	Replay(ctx context.Context, by identity.Principal, key, requestHash string) (*requests.Request, bool, error)
}

// RollbackCommand is a validated-at-the-edge POST /v1/stacks/{id}/rollback.
type RollbackCommand struct {
	StackID        uuid.UUID
	Reason         string
	IdempotencyKey string
	RequestHash    string
	CorrelationID  string
}

// RollbackService opens rollback requests: an update whose spec is the stack's last
// successfully applied configuration, through the normal request path (docs/06 §5).
type RollbackService struct {
	stacks  StackTargets
	members Memberships
	history History
	creator RequestCreator
}

// NewRollbackService returns a RollbackService.
func NewRollbackService(stacks StackTargets, members Memberships, history History, creator RequestCreator) *RollbackService {
	return &RollbackService{stacks: stacks, members: members, history: history, creator: creator}
}

// Rollback opens the rollback request for a stack.
//
// The caller needs the retry/rollback permission of the rbac matrix (maintainer or higher)
// in the stack's team; a caller with no role there gets the same 404 as for a stack that
// does not exist. A retried call with the same Idempotency-Key and body returns the
// request it created. Otherwise the stack must not be destroyed or busy, and must have a
// configuration to restore (SelectRollbackTarget). The request itself is created by
// requests.Service.Create, so idempotency, the catalog and input checks, the stack lock,
// the audit trail and publishing behave exactly as for any update.
func (s *RollbackService) Rollback(ctx context.Context, by identity.Principal, cmd RollbackCommand) (requests.CreateResult, error) {
	if err := ValidateReason(cmd.Reason); err != nil {
		return requests.CreateResult{}, err
	}
	if by.UserID == uuid.Nil {
		return requests.CreateResult{}, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
			Message: "a rollback must be asked for by a user"}
	}
	t, err := s.stacks.StackTarget(ctx, cmd.StackID)
	if err != nil {
		if apperr.IsKind(err, apperr.KindNotFound) {
			return requests.CreateResult{}, stackNotFound(cmd.StackID)
		}
		return requests.CreateResult{}, fmt.Errorf("lookup stack %s: %w", cmd.StackID, err)
	}
	role, err := s.members.RoleInTeam(ctx, t.TeamSlug, by.UserID)
	if err != nil {
		return requests.CreateResult{}, fmt.Errorf("lookup membership of team %s: %w", t.TeamSlug, err)
	}
	if role == rbac.RoleNone {
		return requests.CreateResult{}, stackNotFound(cmd.StackID)
	}
	if d := rbac.Decide(role, rbac.ActionRetry, rbac.Resource{Environment: t.Environment}); !d.Allowed {
		return requests.CreateResult{}, apperr.Forbidden(fmt.Sprintf("rolling back a stack of team %s needs the %s role or higher", t.TeamSlug, d.Need))
	}

	if r, found, err := s.creator.Replay(ctx, by, cmd.IdempotencyKey, cmd.RequestHash); err != nil {
		return requests.CreateResult{}, err
	} else if found {
		return requests.CreateResult{Request: r, Replayed: true}, nil
	}

	open, err := s.history.OpenRequests(ctx, t.ID)
	if err != nil {
		return requests.CreateResult{}, fmt.Errorf("count open requests: %w", err)
	}
	applied, err := s.history.AppliedSpecs(ctx, t.ID, RollbackHistory)
	if err != nil {
		return requests.CreateResult{}, fmt.Errorf("load applied configurations: %w", err)
	}
	target, err := SelectRollbackTarget(t.Status, open, applied)
	if err != nil {
		return requests.CreateResult{}, err
	}
	in, err := RollbackInput(t.ID, target, cmd.Reason)
	if err != nil {
		return requests.CreateResult{}, err
	}
	res, err := s.creator.Create(ctx, by, requests.CreateCommand{Input: in, IdempotencyKey: cmd.IdempotencyKey,
		RequestHash: cmd.RequestHash, CorrelationID: cmd.CorrelationID})
	if err != nil {
		return requests.CreateResult{}, YankedTargetError(err, target)
	}
	return res, nil
}

// YankedTargetError explains a create refused because the restored configuration pins a
// module version the catalog no longer offers (yanked, or gone): rollback_target_yanked,
// pointing at a roll-forward. Any other error is returned unchanged. Pure.
func YankedTargetError(err error, target AppliedSpec) error {
	e, ok := apperr.As(err)
	if !ok || e.Kind != apperr.KindInvalid {
		return err
	}
	yanked := e.Code == "module_version_unavailable"
	for _, f := range e.Fields {
		yanked = yanked || f.Code == "module_version_unavailable"
	}
	if !yanked {
		return err
	}
	return apperr.Invalid("rollback_target_yanked",
		fmt.Sprintf("the configuration applied by %s pins a module version that is no longer usable (yanked); "+
			"roll forward with an update to a published version instead", target.ShortID), e.Fields...)
}

func stackNotFound(id uuid.UUID) error {
	return &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found", Message: fmt.Sprintf("stack %s not found", id)}
}
