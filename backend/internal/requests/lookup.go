package requests

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
)

// StackHolder is a stack and what holds its platform lock: the read model behind
// GET /v1/internal/requests/by-path.
type StackHolder struct {
	StackID   uuid.UUID
	StackPath string
	Lock      *locks.Lock // nil when the stack is not locked
	Request   *Request    // the request named by Lock; nil when there is no lock
}

// ApplyTarget is what apply.yml needs to claim the stack lock and verify its plan: the
// stack and request ids, the fencing token of the lease and the plan hash approved for
// the request.
type ApplyTarget struct {
	StackID          uuid.UUID
	StackPath        string
	RequestID        uuid.UUID
	RequestShortID   string
	Kind             Kind
	Status           Status
	FencingToken     int64
	ApprovedPlanHash string
	MergeCommitSHA   string
}

// ParseLookupPath canonicalises a stack path from a query string. It applies the same
// rules as every other stack-path input (budgets.ParseStackPath) and also rejects a
// trailing slash, so one stack has one spelling. Pure.
func ParseLookupPath(path string) (string, error) {
	ref, err := budgets.ParseStackPath(path)
	if err != nil {
		return "", err
	}
	canonical := fmt.Sprintf("environments/%s/%s/%s", ref.Environment, ref.Team, ref.Stack)
	if canonical != path {
		return "", apperr.Invalid("invalid_stack_path",
			fmt.Sprintf("stack_path %q is not canonical; want %s", path, canonical))
	}
	return canonical, nil
}

// ResolveApplyTarget decides whether h names a request apply.yml may act on, and builds
// its answer. It fails closed:
//   - no lock, or an expired lease: no_active_request (404);
//   - the holder is not approved or applying: request_not_approved (409). The webhook that
//     moves a merged request to applying can trail the push that starts apply.yml, so both
//     statuses pass; anything earlier means the change was not reviewed;
//   - head is a 40-character SHA that differs from the recorded merge commit:
//     merge_commit_mismatch (409). A request whose merge is not recorded yet passes.
//
// head is optional (empty skips the check); its shape is validated by the caller. Pure.
func ResolveApplyTarget(h StackHolder, head string, now time.Time) (ApplyTarget, error) {
	if h.Lock == nil || h.Request == nil || h.Lock.Expired(now) {
		return ApplyTarget{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "no_active_request",
			Message: fmt.Sprintf("no request holds the lock of stack %s", h.StackPath)}
	}
	r := h.Request
	if err := checkLockHolder(h); err != nil {
		return ApplyTarget{}, err
	}
	if r.Status != StatusApproved && r.Status != StatusApplying {
		return ApplyTarget{}, apperr.Conflict("request_not_approved",
			fmt.Sprintf("request %s holds the lock of %s but is %s, not approved", r.ShortID, h.StackPath, r.Status))
	}
	if head != "" && r.MergeCommitSHA != "" && head != r.MergeCommitSHA {
		return ApplyTarget{}, apperr.Conflict("merge_commit_mismatch",
			fmt.Sprintf("request %s was merged as %s, not %s", r.ShortID, r.MergeCommitSHA, head))
	}
	return ApplyTarget{StackID: h.StackID, StackPath: h.StackPath, RequestID: r.ID, RequestShortID: r.ShortID,
		Kind: r.Kind, Status: r.Status, FencingToken: h.Lock.FencingToken, ApprovedPlanHash: r.ApprovedPlanHash,
		MergeCommitSHA: r.MergeCommitSHA}, nil
}

// checkLockHolder guards against a read model that pairs a lock with a different request.
func checkLockHolder(h StackHolder) error {
	if h.Lock.RequestID != h.Request.ID || h.Request.Stack.ID != h.StackID {
		return fmt.Errorf("stack %s: lock names request %s but %s was loaded", h.StackPath, h.Lock.RequestID, h.Request.ID)
	}
	return nil
}

// ApplyTargetByPath resolves the request apply.yml should act on for a stack path. head
// is the pushed commit SHA, or empty.
func (s *Service) ApplyTargetByPath(ctx context.Context, path, head string) (ApplyTarget, error) {
	canonical, err := ParseLookupPath(path)
	if err != nil {
		return ApplyTarget{}, err
	}
	if head != "" && !shaRe.MatchString(head) {
		return ApplyTarget{}, apperr.Invalid("invalid_head_sha", "head_sha must be 40 lowercase hex characters")
	}
	h, err := s.repo.StackHolder(ctx, canonical)
	if err != nil {
		return ApplyTarget{}, err
	}
	return ResolveApplyTarget(h, head, s.clock.Now())
}
