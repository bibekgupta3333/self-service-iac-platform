package recovery

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// AppliedSpec is the configuration of a create or update request that applied
// successfully: a known-good state of its stack.
type AppliedSpec struct {
	RequestID   uuid.UUID
	ShortID     string
	Spec        json.RawMessage // requests.NormalizedSpec
	CompletedAt time.Time
}

// MaxReasonLength bounds the reason a rollback is asked for with.
const MaxReasonLength = 1000

// RollbackHistory is how many applied configurations SelectRollbackTarget looks at.
const RollbackHistory = 2

// busyStatuses are the stack statuses of an apply in flight.
var busyStatuses = []requests.StackStatus{requests.StackProvisioning, requests.StackUpdating, requests.StackDestroying}

// SelectRollbackTarget picks the configuration a rollback restores, from the stack's
// applied create and update requests, newest first (at least RollbackHistory of them when
// they exist), its status and the number of its open requests. Pure.
//
//   - a destroyed stack cannot be rolled back (stack_destroyed);
//   - a stack with an apply in flight or any open request is busy (stack_busy): a rollback
//     must not queue behind a change whose outcome is not known yet;
//   - a failed stack returns to its newest applied configuration (the last good one, from
//     before the change that failed);
//   - an active or drifted stack runs its newest applied configuration, so the rollback
//     restores the one before it (undoing a harmful change that applied successfully);
//   - without such a configuration there is nothing to restore (no_rollback_target).
func SelectRollbackTarget(status requests.StackStatus, open int, applied []AppliedSpec) (AppliedSpec, error) {
	switch {
	case status == requests.StackDestroyed:
		return AppliedSpec{}, apperr.Conflict("stack_destroyed", "the stack was destroyed; there is nothing to roll back")
	case slices.Contains(busyStatuses, status):
		return AppliedSpec{}, apperr.Conflict("stack_busy",
			fmt.Sprintf("the stack is %s; wait for the change in flight to finish before rolling back", status))
	case open > 0:
		return AppliedSpec{}, apperr.Conflict("stack_busy",
			fmt.Sprintf("the stack has %d open request(s); finish or cancel them before rolling back", open))
	}
	idx := 1 // active or drifted: the newest applied configuration is the current one
	if status == requests.StackFailed {
		idx = 0
	}
	if len(applied) <= idx {
		msg := "the stack has no earlier applied configuration to roll back to"
		if idx == 0 {
			msg = "the stack has no applied configuration to roll back to"
		}
		return AppliedSpec{}, apperr.Conflict("no_rollback_target", msg)
	}
	return applied[idx], nil
}

// ValidateReason checks the reason a rollback is asked for. Pure.
func ValidateReason(reason string) error {
	r := strings.TrimSpace(reason)
	switch {
	case r == "":
		return apperr.Invalid("validation_failed", "a rollback needs a reason",
			apperr.FieldError{Pointer: "/reason", Code: "required", Message: "say why the stack is rolled back"})
	case len(r) > MaxReasonLength:
		return apperr.Invalid("validation_failed", "the reason is too long",
			apperr.FieldError{Pointer: "/reason", Code: "maxLength", Message: fmt.Sprintf("must be at most %d characters", MaxReasonLength)})
	}
	return nil
}

// RollbackJustification is the justification of a rollback request: what it restores and
// why. Pure.
func RollbackJustification(target AppliedSpec, reason string) string {
	return fmt.Sprintf("Rollback to the configuration applied by %s (completed %s).\n\nReason: %s",
		target.ShortID, target.CompletedAt.UTC().Format(time.RFC3339), strings.TrimSpace(reason))
}

// RollbackInput is the update request that restores target on stack stackID: its full
// resource list (removals allowed, since resources added after it must go), the rollback
// source and the justification. It goes through the normal request path, so the catalog,
// RBAC, policy and approval checks all apply. Pure.
func RollbackInput(stackID uuid.UUID, target AppliedSpec, reason string) (requests.CreateInput, error) {
	var spec requests.NormalizedSpec
	if err := json.Unmarshal(target.Spec, &spec); err != nil {
		return requests.CreateInput{}, fmt.Errorf("decode the spec of %s: %w", target.ShortID, err)
	}
	if len(spec.Resources) == 0 {
		return requests.CreateInput{}, apperr.Conflict("no_rollback_target",
			fmt.Sprintf("the configuration applied by %s has no resources", target.ShortID))
	}
	id := stackID
	return requests.CreateInput{
		Kind: requests.KindUpdate, StackID: &id, Resources: slices.Clone(spec.Resources),
		AllowRemovals: true, Justification: RollbackJustification(target, reason), RollbackOf: target.ShortID,
	}, nil
}
