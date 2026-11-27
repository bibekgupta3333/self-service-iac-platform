package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// StackHolder is a stack and what holds its platform lock: the read model behind
// GET /v1/internal/requests/by-path.
type StackHolder struct {
	StackID   uuid.UUID
	StackPath string
	Lock      *locks.Lock // nil when the stack is not locked
	Request   *Request    // the request named by Lock; nil when there is no lock
	// RequesterLogin is the GitHub login of the user who created Request (empty when unknown).
	RequesterLogin string
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
	// Environment is the stack's environment (from its path); the pre-apply gate and the
	// production caller check (CheckApplyCaller) read it.
	Environment string
	// RequestedBy is the GitHub login of the request's author: the pre-apply approval gate
	// never counts the author's own review (policies/opa/prod_approval.rego).
	RequestedBy string
	// PRNumber is the request's pull request, whose reviews the pre-apply gate reads (0 when
	// none is recorded).
	PRNumber int
	// AllowStatefulDestroy is the request's allow_stateful_destroy.
	AllowStatefulDestroy bool
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
//   - the holder is not approved or applying: request_not_approved (409);
//   - no merge commit is recorded for the holder: merge_not_recorded (409, retryable). The
//     lock credential and the approved hash are only handed out for a change that is merged
//     to main; until the pr_merged webhook records the commit (it also moves the request to
//     applying) the PR may still be open, and an apply that released the lock of an open,
//     unmerged request would promote the next waiter over it. The caller retries briefly to
//     cover the webhook lag;
//   - head is a 40-character SHA that differs from the recorded merge commit:
//     merge_commit_mismatch (409).
//
// head is optional (empty skips only the comparison); its shape is validated by the caller. Pure.
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
	if r.MergeCommitSHA == "" {
		return ApplyTarget{}, apperr.Conflict("merge_not_recorded",
			fmt.Sprintf("request %s is not merged yet (no merge commit recorded); retry shortly", r.ShortID))
	}
	if head != "" && head != r.MergeCommitSHA {
		return ApplyTarget{}, apperr.Conflict("merge_commit_mismatch",
			fmt.Sprintf("request %s was merged as %s, not %s", r.ShortID, r.MergeCommitSHA, head))
	}
	t := ApplyTarget{StackID: h.StackID, StackPath: h.StackPath, RequestID: r.ID, RequestShortID: r.ShortID,
		Kind: r.Kind, Status: r.Status, FencingToken: h.Lock.FencingToken, ApprovedPlanHash: r.ApprovedPlanHash,
		MergeCommitSHA: r.MergeCommitSHA, RequestedBy: h.RequesterLogin,
		AllowStatefulDestroy: specAllowsStatefulDestroy(r.Spec)}
	if ref, err := budgets.ParseStackPath(h.StackPath); err == nil {
		t.Environment = ref.Environment
	}
	if r.PR != nil {
		t.PRNumber = r.PR.Number
	}
	return t, nil
}

// specAllowsStatefulDestroy reads allow_stateful_destroy from a stored spec; an unreadable
// spec never allows it (fail closed). Pure.
func specAllowsStatefulDestroy(spec json.RawMessage) bool {
	var s struct {
		AllowStatefulDestroy bool `json:"allow_stateful_destroy"`
	}
	return json.Unmarshal(spec, &s) == nil && s.AllowStatefulDestroy
}

// ProductionEnvironment is the GitHub Environment (and platform environment) whose
// required reviewers gate a production apply.
const ProductionEnvironment = "production"

// CheckApplyCaller decides whether the caller of GET /v1/internal/requests/by-path may
// receive the lock credential and approved hash of a stack in env (WBS 8.8). Pure.
//
//   - production (and any environment the platform does not know, fail closed) needs a
//     GitHub OIDC token from a job that ran in the GitHub Environment "production": the
//     token only carries that environment claim after the Environment's required reviewers
//     approved the job. The shared internal token is refused (oidc_required), and a job
//     outside the Environment gets production_environment_required;
//   - in development and staging a job bound to a GitHub Environment must be bound to the
//     stack's own (environment_mismatch); a job with no Environment is still accepted there.
func CheckApplyCaller(env string, wf *identity.Workflow) error {
	forbidden := func(code, msg string) error {
		return &apperr.Error{Kind: apperr.KindForbidden, Code: code, Message: msg}
	}
	if rbac.IsProduction(env) {
		switch {
		case wf == nil:
			return forbidden("oidc_required",
				"a production stack is applied only by apply.yml with a GitHub OIDC token, never the shared token")
		case wf.Environment != ProductionEnvironment:
			return forbidden("production_environment_required",
				fmt.Sprintf("a production stack is applied only from a job in the %q GitHub Environment (its required reviewers gate the apply); this job ran in %q",
					ProductionEnvironment, wf.Environment))
		}
		return nil
	}
	if wf != nil && wf.Environment != "" && wf.Environment != env {
		return forbidden("environment_mismatch",
			fmt.Sprintf("a job in the %q GitHub Environment cannot apply a %s stack", wf.Environment, env))
	}
	return nil
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
