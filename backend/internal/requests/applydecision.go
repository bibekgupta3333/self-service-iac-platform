package requests

import (
	"encoding/json"
	"fmt"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

// StackHealth is what the post-apply smoke test says about a stack (stacks.health). It is
// independent of the stack's lifecycle status: a degraded stack is still active.
type StackHealth string

// Stack health values.
const (
	HealthUnknown  StackHealth = "unknown"
	HealthHealthy  StackHealth = "healthy"
	HealthDegraded StackHealth = "degraded"
	// HealthDrifted: a drift scan found the live stack differs from its configuration. An apply
	// re-asserts the configuration and so replaces it (docs/06 §4, ADR-0013).
	HealthDrifted StackHealth = "drifted"
)

// ApplyPhase is what the apply pipeline has told the platform about one request so far.
type ApplyPhase string

// Apply phases.
const (
	// PhaseStarted: apply.yml holds the lock and is working; nothing final yet.
	PhaseStarted ApplyPhase = "started"
	// PhaseSucceeded: the run finished and the infrastructure matches the merged change.
	PhaseSucceeded ApplyPhase = "succeeded"
	// PhaseFailed: the run finished without applying the change (or failed while doing so).
	PhaseFailed ApplyPhase = "failed"
	// PhaseObserved: a report that carries no verdict of its own (a failed lock release is
	// the only one today); it is audited and never moves the request or the stack.
	PhaseObserved ApplyPhase = "observed"
)

// Failure reasons recorded in requests.failure_reason by run reports.
const (
	FailureApplyFailed     = "apply_failed"
	FailureApplyCancelled  = "apply_cancelled"
	FailurePlanFailed      = "plan_failed"
	FailureApplyIncomplete = "apply_incomplete"
)

// RunRef names the workflow run and stage a progress report came from (for the audit trail).
type RunRef struct {
	RunID   int64  `json:"run_id"`
	Attempt int    `json:"run_attempt"`
	HeadSHA string `json:"head_sha"`
	Stage   string `json:"stage"`
}

// ApplyProgress is the pipeline's view of a request, derived from run reports by
// runs.Progress. It is a value: DecideApply turns it into state changes.
type ApplyProgress struct {
	Phase ApplyPhase
	Run   RunRef
	// Health and Outputs are set for PhaseSucceeded. Outputs are the redacted outputs of
	// the apply (a JSON object); empty means none.
	Health  StackHealth
	Outputs json.RawMessage
	// FailureReason and FailureDetail are set for PhaseFailed.
	FailureReason string
	FailureDetail json.RawMessage
	// ReleaseFailure is the error message of a failed lock release (PhaseObserved).
	ReleaseFailure string
}

// StackChange is what an apply does to its stack row.
type StackChange struct {
	To StackStatus
	// Health is written when not empty.
	Health StackHealth
	// Outputs replaces the stored outputs when not nil (an empty object clears them).
	Outputs json.RawMessage
	// Applied records the run's commit and the request as the stack's last applied state.
	Applied bool
	// Destroyed stamps destroyed_at.
	Destroyed bool
}

// ApplyDecision is the outcome of DecideApply.
type ApplyDecision struct {
	// Events are fired on the request in order.
	Events []Event
	// Stack is nil when the stack row stays as it is.
	Stack *StackChange
	// Ignore, when set, means the report is recorded in its run but changes nothing; the
	// value is the reason (request_terminal).
	Ignore string
	// ReleaseFailed marks a failed lock release to audit (no state change).
	ReleaseFailed bool
}

// applicable statuses: a request the pipeline may report on.
func (s Status) acceptsApplyReports() bool {
	return s == StatusApproved || s == StatusApplying || s.IsTerminal()
}

// DecideApply decides what a run's progress does to a request in status (a request of kind
// whose stack is in stack). It is the single place the apply pipeline moves requests and
// stacks, and it is idempotent by construction: replaying the same progress against the
// state it produced yields no events, and a terminal request is never touched.
//
//   - A failed lock release is audited whatever the request's status and moves nothing.
//   - A request that is not approved, applying or terminal has no business with apply.yml
//     (it was never approved, or merged outside the platform's knowledge): conflict
//     request_not_applying, and nothing is recorded.
//   - approved moves to applying first (apply_started), so a report that arrives before the
//     merge webhook was processed still works; applying stays applying.
//   - PhaseSucceeded fires apply_succeeded and sets the stack to StackStatusAfterApply
//     (active, or destroyed after a destroy) with the outputs and last applied commit.
//   - PhaseFailed fires apply_failed and sets the stack failed (docs/03 §4.2), which keeps
//     it open to fix-forward or destroy requests.
//   - A stack that is already destroyed is never changed. Pure.
func DecideApply(status Status, kind Kind, stack StackStatus, p ApplyProgress) (ApplyDecision, error) {
	if !status.acceptsApplyReports() {
		return ApplyDecision{}, apperr.Conflict("request_not_applying",
			fmt.Sprintf("a %s request in status %s cannot receive apply reports; it must be approved first", kind, status))
	}
	if p.Phase == PhaseObserved {
		// Even for a terminal request: the backend releases the lock when the request ends,
		// so a failed release is typically reported after the verdict.
		return ApplyDecision{ReleaseFailed: p.ReleaseFailure != ""}, nil
	}
	if status.IsTerminal() {
		return ApplyDecision{Ignore: "request_terminal"}, nil
	}
	var d ApplyDecision
	if status == StatusApproved {
		d.Events = append(d.Events, EventApplyStarted)
	}
	if stack == StackDestroyed {
		// Nothing can change a destroyed stack; only the request moves.
		switch p.Phase {
		case PhaseSucceeded:
			d.Events = append(d.Events, EventApplySucceeded)
		case PhaseFailed:
			d.Events = append(d.Events, EventApplyFailed)
		}
		return d, nil
	}
	switch p.Phase {
	case PhaseStarted:
		if to := StackStatusWhileApplying(kind); stack != to {
			d.Stack = &StackChange{To: to}
		}
	case PhaseSucceeded:
		d.Events = append(d.Events, EventApplySucceeded)
		change := &StackChange{To: StackStatusAfterApply(kind, true), Health: p.Health, Applied: true}
		if kind == KindDestroy {
			change.Health, change.Outputs, change.Destroyed = HealthUnknown, json.RawMessage(`{}`), true
		} else {
			change.Outputs = outputsOrEmpty(p.Outputs)
			if change.Health == "" {
				change.Health = HealthUnknown
			}
		}
		d.Stack = change
	case PhaseFailed:
		d.Events = append(d.Events, EventApplyFailed)
		d.Stack = &StackChange{To: StackStatusAfterApply(kind, false)}
	default:
		return ApplyDecision{}, fmt.Errorf("unknown apply phase %q", p.Phase)
	}
	return d, nil
}

func outputsOrEmpty(o json.RawMessage) json.RawMessage {
	if len(o) == 0 || string(o) == "null" {
		return json.RawMessage(`{}`)
	}
	return o
}
