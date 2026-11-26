// Package requests implements change requests: the domain entity and its state
// machine, input validation, the RequestService and its PostgreSQL repository.
package requests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

// Kind is the type of change a request makes (enum request_kind).
type Kind string

// Request kinds.
const (
	KindCreate    Kind = "create"
	KindUpdate    Kind = "update"
	KindUpgrade   Kind = "upgrade"
	KindDestroy   Kind = "destroy"
	KindReconcile Kind = "reconcile"
	KindRollback  Kind = "rollback"
)

// AllKinds lists every request kind.
var AllKinds = []Kind{KindCreate, KindUpdate, KindUpgrade, KindDestroy, KindReconcile, KindRollback}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return slices.Contains(AllKinds, k) }

var planHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// StackRef is the stack a request targets, as shown in API responses.
type StackRef struct {
	ID     uuid.UUID `json:"id"`
	Path   string    `json:"path"`
	Status string    `json:"status"`
}

// PRLink is the branch and pull request that carry a request's generated files.
type PRLink struct {
	Branch string `json:"branch"`
	Number int    `json:"pr_number"`
	URL    string `json:"pr_url"`
}

// Request is a change request against one stack. Status changes go through Fire and
// Approve so the state machine and its invariants hold.
type Request struct {
	ID               uuid.UUID
	ShortID          string
	Stack            StackRef
	Kind             Kind
	Status           Status
	Spec             json.RawMessage
	Justification    string
	QueuedBehind     *uuid.UUID // set while the request waits for the stack lock
	FencingToken     *int64     // token of the stack lock lease granted to this request
	ApprovedPlanHash string
	ApprovedBy       *uuid.UUID
	ApprovedAt       *time.Time
	RetryCount       int     // automatic apply retries used (requests.retry_count)
	PR               *PRLink // set once the publisher opened the PR
	RenderedTreeSHA  string  // git tree of the published commit
	MergeCommitSHA   string
	FailureReason    string
	FailureDetail    json.RawMessage
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
}

// NewDraft returns a request in the draft status.
func NewDraft(id uuid.UUID, shortID string, stack StackRef, kind Kind, spec json.RawMessage,
	justification string, createdBy uuid.UUID, at time.Time) *Request {
	return &Request{
		ID: id, ShortID: shortID, Stack: stack, Kind: kind, Status: StatusDraft, Spec: spec,
		Justification: justification, CreatedBy: createdBy, CreatedAt: at, UpdatedAt: at,
	}
}

// IsQueued reports whether the request waits for another request's stack lock.
func (r *Request) IsQueued() bool { return r.QueuedBehind != nil }

// Fire applies event at time at. Approval must go through Approve, which pins the plan hash.
// A queued request cannot open its PR: it stays draft until it holds the stack lock.
func (r *Request) Fire(event Event, at time.Time) error {
	if event == EventApproved {
		return apperr.Invalid("approval_requires_plan_hash", "use Approve to approve a request")
	}
	if event == EventPROpened && r.IsQueued() {
		return apperr.Conflict("stack_locked",
			fmt.Sprintf("request %s is queued behind %s and does not hold the stack lock", r.ShortID, *r.QueuedBehind))
	}
	return r.move(event, at)
}

// Approve moves a plan_ready request to approved, pinning the reviewed plan hash.
func (r *Request) Approve(planHash string, by uuid.UUID, at time.Time) error {
	if !planHashRe.MatchString(planHash) {
		return apperr.Invalid("validation_failed", "plan hash must be 64 lowercase hex characters")
	}
	if err := r.move(EventApproved, at); err != nil {
		return err
	}
	r.ApprovedPlanHash, r.ApprovedBy, r.ApprovedAt = planHash, &by, &at
	return nil
}

func (r *Request) move(event Event, at time.Time) error {
	next, err := Next(Snapshot{Status: r.Status, RetryCount: r.RetryCount}, event)
	if err != nil {
		return err
	}
	// A new commit or a dismissed review invalidates the reviewed plan.
	if event == EventNewCommit || event == EventReviewDismissed {
		r.ApprovedPlanHash, r.ApprovedBy, r.ApprovedAt = "", nil, nil
	}
	r.Status, r.RetryCount, r.UpdatedAt = next.Status, next.RetryCount, at
	if next.Status.IsTerminal() {
		r.CompletedAt = &at
	}
	return nil
}
