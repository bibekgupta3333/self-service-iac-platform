package requests

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

// StackStatus is the lifecycle state of a stack (enum stack_status, docs/03 §4.2).
type StackStatus string

// Stack statuses.
const (
	StackProvisioning StackStatus = "provisioning"
	StackActive       StackStatus = "active"
	StackDrifted      StackStatus = "drifted"
	StackUpdating     StackStatus = "updating"
	StackDestroying   StackStatus = "destroying"
	StackDestroyed    StackStatus = "destroyed"
	StackFailed       StackStatus = "failed"
)

// AllStackStatuses lists every stack status in lifecycle order.
var AllStackStatuses = []StackStatus{
	StackProvisioning, StackActive, StackDrifted, StackUpdating, StackDestroying, StackDestroyed, StackFailed,
}

// Valid reports whether s is a known stack status.
func (s StackStatus) Valid() bool { return slices.Contains(AllStackStatuses, s) }

// supportedKinds are the kinds the API accepts today. reconcile and rollback are
// platform-initiated and not built yet.
var supportedKinds = []Kind{KindCreate, KindUpdate, KindUpgrade, KindDestroy}

// Supported reports whether POST /v1/requests accepts the kind.
func (k Kind) Supported() bool { return slices.Contains(supportedKinds, k) }

// TargetsExistingStack reports whether the kind changes a stack that already exists, so
// the request names it by stack_id instead of by team, environment and name.
func (k Kind) TargetsExistingStack() bool { return k.Valid() && k != KindCreate }

// RequiresResources reports whether the request carries the desired resource list. A
// destroy renders none: the stack's root is emptied.
func (k Kind) RequiresResources() bool { return k == KindCreate || k == KindUpdate }

// editableStatuses are the stack statuses an update or destroy may start from. A failed
// stack is open to remediation (fix-forward or destroy), a drifted one to codify or
// destroy (docs/06 §1, §5).
var editableStatuses = []StackStatus{StackActive, StackDrifted, StackFailed}

// CheckStackAllowsKind decides whether a request of kind may be created for a stack in
// status, given the kinds of the stack's other open (non-terminal) requests. Pure.
//
//   - destroyed stacks accept nothing (stack_destroyed);
//   - a stack that is provisioning, updating or destroying is mid-flight (stack_busy):
//     the request must wait until the apply in flight finishes;
//   - a stack with an open destroy request accepts no further update or destroy
//     (destroy_pending), because whatever it queued behind would meet an empty stack;
//   - an upgrade is refused (stack_busy) while another create, update or upgrade is open:
//     it re-pins the stack's newest spec, and copying a spec that is still under review
//     would carry that request's pending changes into a "version only" pull request.
//
// Anything else queues behind the request that holds the stack lock.
func CheckStackAllowsKind(kind Kind, status StackStatus, open []Kind) error {
	switch {
	case kind == KindCreate:
		return apperr.Conflict("stack_exists", "the stack already exists; use an update request")
	case !kind.TargetsExistingStack():
		return apperr.Invalid("unsupported_kind", fmt.Sprintf("kind %q does not change an existing stack", kind))
	case status == StackDestroyed:
		return apperr.Conflict("stack_destroyed", fmt.Sprintf("the stack was destroyed; a %s request cannot target it", kind))
	case !slices.Contains(editableStatuses, status):
		return apperr.Conflict("stack_busy",
			fmt.Sprintf("the stack is %s; wait for the request in flight to finish before submitting a %s request", status, kind))
	case slices.Contains(open, KindDestroy):
		return apperr.Conflict("destroy_pending",
			fmt.Sprintf("a destroy request is open for the stack; a %s request cannot follow it", kind))
	case kind == KindUpgrade && slices.ContainsFunc(open, carriesSpec):
		return apperr.Conflict("stack_busy",
			"another create, update or upgrade is open for the stack; wait until it is applied or closed, then upgrade from the result")
	}
	return nil
}

// carriesSpec reports whether a request of kind sets the stack's resource list. Pure.
func carriesSpec(k Kind) bool { return k == KindCreate || k == KindUpdate || k == KindUpgrade }

// StackStatusWhileApplying is the stack status while a request of kind is applying
// (docs/03 §4.2). Pure.
func StackStatusWhileApplying(kind Kind) StackStatus {
	switch kind {
	case KindCreate:
		return StackProvisioning
	case KindDestroy:
		return StackDestroying
	default:
		return StackUpdating
	}
}

// StackStatusAfterApply is the stack status once a request of kind finished applying:
// failed on failure, destroyed after a destroy, active otherwise. Pure.
func StackStatusAfterApply(kind Kind, succeeded bool) StackStatus {
	switch {
	case !succeeded:
		return StackFailed
	case kind == KindDestroy:
		return StackDestroyed
	default:
		return StackActive
	}
}

// StackTarget is an existing stack as an update or destroy request sees it.
type StackTarget struct {
	ID            uuid.UUID
	TeamID        uuid.UUID
	TeamSlug      string
	EnvironmentID uuid.UUID
	Environment   string
	Name          string
	Path          string
	Region        string
	Status        StackStatus
	IsEphemeral   bool
	ExpiresAt     *time.Time // set for an ephemeral stack
}

// TeamFromStackPath returns the team slug of a stack path (environments/<env>/<team>/<stack>);
// false when the path has another shape. Pure.
func TeamFromStackPath(path string) (string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "environments" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", false
	}
	return parts[2], true
}
