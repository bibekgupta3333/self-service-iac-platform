package recovery

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// TopicApplyRetry is the outbox topic of a scheduled apply retry: the Dispatcher starts
// apply.yml again for the request once the backoff has passed.
const TopicApplyRetry = "recovery.apply_retry"

// ApplyWorkflow is the workflow a retry dispatches.
const ApplyWorkflow = "apply.yml"

// Decisions recorded for a classified failure (apply_failures.decision).
const (
	DecisionRetryScheduled     = "retry_scheduled"
	DecisionNotTransient       = "not_transient"
	DecisionStageNotRetryable  = "stage_not_retryable"
	DecisionRequestNotApplying = "request_not_applying"
	DecisionBudgetExhausted    = "retry_budget_exhausted"
	DecisionLockLost           = "lock_lost"
	DecisionRetriesDisabled    = "retries_disabled"
)

// FailureRetryNotRun is the failure_reason of a request whose scheduled retry could not
// run (the stack lock went to another request, or the dispatch failed).
const (
	FailureRetryNotRun = "retry_not_run"
)

// retryStages are the stages a transient failure may be retried from: before or during
// terraform apply. The lock claim is one of them (state-lock contention), the smoke test
// and the reports are not (they run after the change and a retry would apply nothing new).
var retryStages = []string{"lock", "plan", "apply", "destroy"}

// Backoff steps (docs/06 §5): 2 minutes, then 10 minutes. Eventual consistency (an IAM role
// that is not assumable yet) retries sooner the first time: 60 seconds.
const (
	firstRetryDelay       = 2 * time.Minute
	retryGrowth           = 5
	eventualConsistencyAt = time.Minute
	maxRetryDelay         = 30 * time.Minute
)

// Backoff is the delay before retry number n (1-based) of a failure with reason. It grows
// exponentially (2 min × 5^(n-1)) and is capped at 30 minutes. Pure.
func Backoff(n int, reason string) time.Duration {
	if n < 1 {
		n = 1
	}
	if n == 1 && reason == "eventual_consistency" {
		return eventualConsistencyAt
	}
	d := firstRetryDelay
	for i := 1; i < n; i++ {
		d *= retryGrowth
		if d >= maxRetryDelay {
			return maxRetryDelay
		}
	}
	return d
}

// RetryPlan is the outcome of PlanRetry.
type RetryPlan struct {
	Retry    bool
	Decision string        // DecisionRetryScheduled, or why there is no retry
	Number   int           // retry number (retry_count after it), when Retry
	Delay    time.Duration // backoff before the dispatch, when Retry
}

// PlanRetry decides whether a classified failure of a request in snapshot s is retried
// automatically. Only transient failures of a retry stage of an applying request are, and
// only while the state machine's retry budget lasts: requests.Next(s, apply_retry) is the
// single source of the cap (MaxApplyRetries). Pure.
func PlanRetry(c Classification, stage string, s requests.Snapshot) RetryPlan {
	switch {
	case c.Class != ClassTransient:
		return RetryPlan{Decision: DecisionNotTransient}
	case !slices.Contains(retryStages, stage):
		return RetryPlan{Decision: DecisionStageNotRetryable}
	case s.Status != requests.StatusApplying:
		return RetryPlan{Decision: DecisionRequestNotApplying}
	}
	next, err := requests.Next(s, requests.EventApplyRetry)
	if err != nil {
		return RetryPlan{Decision: DecisionBudgetExhausted}
	}
	return RetryPlan{Retry: true, Decision: DecisionRetryScheduled, Number: next.RetryCount,
		Delay: Backoff(next.RetryCount, c.Reason)}
}

// RetryPayload is the body of a recovery.apply_retry outbox job.
type RetryPayload struct {
	RequestID   uuid.UUID `json:"request_id"`
	RetryNumber int       `json:"retry_number"`
}

// RetryDedupeKey keeps one pending dispatch per request and retry number. Pure.
func RetryDedupeKey(id uuid.UUID, n int) string { return fmt.Sprintf("apply-retry:%s:%d", id, n) }

// RetryMessage is the outbox job that dispatches retry n of request id at at. Pure.
func RetryMessage(id uuid.UUID, n int, at time.Time) (outbox.Message, error) {
	payload, err := json.Marshal(RetryPayload{RequestID: id, RetryNumber: n})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode retry job: %w", err)
	}
	return outbox.Message{Topic: TopicApplyRetry, AggregateID: &id, DedupeKey: RetryDedupeKey(id, n), Payload: payload,
		AvailableAt: at}, nil
}

var planHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DispatchInputs are the apply.yml workflow_dispatch inputs of a retry. The approved plan
// hash goes along, so the re-plan must match what was approved: a retry after a failure
// that changed nothing applies, one after a partial apply fails the hash check and needs
// a new approval (docs/06 §5). Malformed values are left out. Pure.
func DispatchInputs(t Target) map[string]string {
	in := map[string]string{
		"stack_path": t.StackPath,
		"stack_id":   t.StackID.String(),
		"request_id": t.ID.String(),
	}
	if t.Kind == requests.KindDestroy {
		in["destroy"] = "true"
	}
	if planHashRe.MatchString(t.ApprovedPlanHash) {
		in["approved_plan_hash"] = t.ApprovedPlanHash
	}
	return in
}
