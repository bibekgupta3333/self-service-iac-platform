package requests

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
)

// Status is the lifecycle state of a request (enum request_status).
type Status string

// Request statuses (docs/03 §4.1).
const (
	StatusDraft      Status = "draft"
	StatusPROpen     Status = "pr_open"
	StatusValidating Status = "validating"
	StatusPlanReady  Status = "plan_ready"
	StatusApproved   Status = "approved"
	StatusApplying   Status = "applying"
	StatusApplied    Status = "applied"
	StatusFailed     Status = "failed"
	StatusRejected   Status = "rejected"
	StatusCancelled  Status = "cancelled"
)

// Event is something that happened to a request and may move it to another status.
type Event string

// Request events.
const (
	EventPROpened          Event = "pr_opened"          // GitHub App opened the PR
	EventValidationStarted Event = "validation_started" // pr-validate.yml in progress
	EventChecksPassed      Event = "checks_passed"      // summary callback passed=true
	EventChecksFailed      Event = "checks_failed"      // summary callback passed=false
	EventApproved          Event = "approved"           // required reviews + plan hash pinned
	EventNewCommit         Event = "new_commit"         // pull_request.synchronize
	EventPRClosed          Event = "pr_closed"          // closed unmerged or review rejected
	EventCancel            Event = "cancel"             // user cancel / queued too long
	EventApplyStarted      Event = "apply_started"      // merged, apply.yml in progress
	EventApplySucceeded    Event = "apply_succeeded"    // apply ok + outputs reported
	EventApplyFailed       Event = "apply_failed"       // non-transient error / plan mismatch
	EventApplyRetry        Event = "apply_retry"        // transient error, auto-retry
)

type edge struct {
	from  Status
	event Event
}

// transitions is the complete request state machine. Anything not listed is invalid.
var transitions = map[edge]Status{
	{StatusDraft, EventPROpened}: StatusPROpen,
	{StatusDraft, EventCancel}:   StatusCancelled,

	{StatusPROpen, EventValidationStarted}: StatusValidating,
	{StatusPROpen, EventPRClosed}:          StatusRejected,
	{StatusPROpen, EventCancel}:            StatusCancelled,

	{StatusValidating, EventChecksPassed}: StatusPlanReady,
	{StatusValidating, EventChecksFailed}: StatusFailed,
	{StatusValidating, EventNewCommit}:    StatusValidating,
	{StatusValidating, EventPRClosed}:     StatusRejected,

	{StatusPlanReady, EventApproved}:  StatusApproved,
	{StatusPlanReady, EventNewCommit}: StatusValidating,
	{StatusPlanReady, EventPRClosed}:  StatusRejected,
	{StatusPlanReady, EventCancel}:    StatusCancelled,

	{StatusApproved, EventNewCommit}:    StatusValidating,
	{StatusApproved, EventPRClosed}:     StatusRejected,
	{StatusApproved, EventApplyStarted}: StatusApplying,

	{StatusApplying, EventApplySucceeded}: StatusApplied,
	{StatusApplying, EventApplyFailed}:    StatusFailed,
	{StatusApplying, EventApplyRetry}:     StatusApplying,
}

// terminal statuses have completed_at set and accept no events.
var terminal = []Status{StatusApplied, StatusFailed, StatusRejected, StatusCancelled}

// AllStatuses lists every status in lifecycle order.
var AllStatuses = []Status{
	StatusDraft, StatusPROpen, StatusValidating, StatusPlanReady, StatusApproved,
	StatusApplying, StatusApplied, StatusFailed, StatusRejected, StatusCancelled,
}

// IsTerminal reports whether s is a final status.
func (s Status) IsTerminal() bool { return slices.Contains(terminal, s) }

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return slices.Contains(AllStatuses, s) }

// Transition returns the status reached by applying event in status from.
// It is a pure lookup in the transition table.
func Transition(from Status, event Event) (Status, error) {
	to, ok := transitions[edge{from, event}]
	if !ok {
		return from, &apperr.Error{
			Kind:    apperr.KindConflict,
			Code:    "invalid_transition",
			Message: fmt.Sprintf("event %q is not allowed in status %q", event, from),
		}
	}
	return to, nil
}

// AllowedEvents returns the events accepted in status from, sorted.
func AllowedEvents(from Status) []Event {
	edges := fp.Filter(sortedEdges(), func(e edge) bool { return e.from == from })
	return fp.Map(edges, func(e edge) Event { return e.event })
}

func sortedEdges() []edge {
	out := make([]edge, 0, len(transitions))
	for e := range transitions {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b edge) int {
		if c := cmp.Compare(a.from, b.from); c != 0 {
			return c
		}
		return cmp.Compare(a.event, b.event)
	})
	return out
}
