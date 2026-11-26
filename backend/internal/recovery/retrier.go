package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Target is a request as recovery sees it, loaded with its stack row locked first and the
// request row second (the order every stack-lock operation uses).
type Target struct {
	ID               uuid.UUID
	ShortID          string
	Kind             requests.Kind
	Status           requests.Status
	RetryCount       int
	FencingToken     *int64
	ApprovedPlanHash string
	StackID          uuid.UUID
	StackPath        string
	TeamID           uuid.UUID
}

// Snapshot is the part of the target the state machine reasons about.
func (t Target) Snapshot() requests.Snapshot {
	return requests.Snapshot{Status: t.Status, RetryCount: t.RetryCount}
}

// RunRef identifies the workflow run a failure came from.
type RunRef struct {
	RunID   int64  `json:"run_id"`
	Attempt int    `json:"run_attempt"`
	HeadSHA string `json:"head_sha,omitempty"`
}

// FailedApply is a failed stage reported by apply.yml for a request.
type FailedApply struct {
	RequestShortID string
	StackPath      string
	FencingToken   *int64
	Run            RunRef
	Stage          string
	ExitCode       *int
	Message        string
	LogURL         string
	CorrelationID  string
}

// FailureRecord is one row of apply_failures: the first failed stage of a run, its class
// and what recovery decided. The (request, run id, run attempt) triple is unique, so a
// replayed report or a later failed stage of the same run is recorded once.
type FailureRecord struct {
	ID          uuid.UUID
	RequestID   uuid.UUID
	StackID     uuid.UUID
	Run         RunRef
	Stage       string
	ExitCode    *int
	Class       Classification
	Decision    string
	RetryNumber int        // 0 when no retry
	RetryAt     *time.Time // when the dispatch job becomes available
	At          time.Time
}

// RetryStore is the transactional port of the Retrier and the Dispatcher.
type RetryStore interface {
	// TargetByShortID / TargetByID lock the stack row, then the request row (FOR UPDATE);
	// apperr NotFound when the request does not exist.
	TargetByShortID(ctx context.Context, shortID string) (Target, error)
	TargetByID(ctx context.Context, id uuid.UUID) (Target, error)
	// CurrentLock is the stack's lock row (nil when unlocked).
	CurrentLock(ctx context.Context, stackID uuid.UUID) (*locks.Lock, error)
	// HasWaiters reports whether requests are queued for the stack's lock.
	HasWaiters(ctx context.Context, stackID uuid.UUID) (bool, error)
	// LockStore is the stack-lock store bound to the same transaction.
	LockStore() locks.Store
	// InsertFailure records a classified failure; false when the run was already recorded.
	InsertFailure(ctx context.Context, f FailureRecord) (bool, error)
	// IncrementRetry moves retry_count from → from+1 on a request that is still applying
	// with that count (apperr Conflict concurrent_update otherwise).
	IncrementRetry(ctx context.Context, requestID uuid.UUID, from int, at time.Time) error
	// ClaimDispatch moves the scheduled retry n of a request to dispatching; false when it is
	// not scheduled any more (dispatched, failed or skipped by an earlier claim).
	ClaimDispatch(ctx context.Context, requestID uuid.UUID, n int, at time.Time) (bool, error)
	// FinishDispatch records the dispatch outcome (dispatched, dispatch_failed or skipped).
	FinishDispatch(ctx context.Context, requestID uuid.UUID, n int, state, detail string, at time.Time) error
	Enqueue(ctx context.Context, m outbox.Message) (bool, error)
	AppendAudit(ctx context.Context, e audit.Event) error
}

// RetryRepository runs a function in one transaction.
type RetryRepository interface {
	InTx(ctx context.Context, fn func(RetryStore) error) error
}

// Outcome is what the Retrier did with a failure.
type Outcome struct {
	Classification
	Decision string
	// RetryNumber and RetryAt are set when a retry was scheduled.
	RetryNumber int
	RetryAt     *time.Time
	// Duplicate means the run's failure was already recorded; nothing changed.
	Duplicate bool
	// Ignored names why the report was not considered (unknown_request, stack_mismatch).
	Ignored string
}

// recoveryPrincipal is the actor of recovery's own transitions and audit events.
var recoveryPrincipal = identity.Principal{Login: "recovery", Type: identity.ActorSystem}

// Retrier classifies failed apply stages and schedules automatic retries of transient
// ones (docs/06 §5). It never dispatches: the dispatch is the Dispatcher's outbox job, so
// the retry decision, the retry_count increment, its audit and the job commit together.
type Retrier struct {
	repo     RetryRepository
	clock    platform.Clock
	ids      platform.IDGenerator
	disabled bool
}

// NewRetrier returns a Retrier.
func NewRetrier(repo RetryRepository, clock platform.Clock, ids platform.IDGenerator) *Retrier {
	return &Retrier{repo: repo, clock: clock, ids: ids}
}

// DisableRetries keeps classifying and recording failures but schedules no retry (decision
// retries_disabled). The API uses it when no publisher can dispatch workflows.
func (r *Retrier) DisableRetries() *Retrier {
	r.disabled = true
	return r
}

// OnApplyFailure handles one failed apply.yml stage. In one transaction it locks the stack
// and the request, classifies the failure, checks the stack lock and the retry budget
// (PlanRetry), records the failure once per run, audits the classification and, for a
// transient failure with budget left, fires apply_retry (retry_count + 1, audited as
// request.transitioned) and enqueues the dispatch job after the backoff.
//
// A retry needs the request to still hold its stack lock with the reported fencing token,
// or the lock to be free (apply.yml releases it when the run ends; the retried run claims
// it again). A lock held by another request means the stack moved on: no retry.
func (r *Retrier) OnApplyFailure(ctx context.Context, f FailedApply) (Outcome, error) {
	var out Outcome
	err := r.repo.InTx(ctx, func(st RetryStore) error {
		t, err := st.TargetByShortID(ctx, f.RequestShortID)
		if apperr.IsKind(err, apperr.KindNotFound) {
			out.Ignored = "unknown_request"
			return nil
		}
		if err != nil {
			return err
		}
		if t.StackPath != f.StackPath {
			out.Ignored = "stack_mismatch"
			return nil
		}
		c := Classify(Failure{Stage: f.Stage, ExitCode: f.ExitCode, Message: f.Message})
		plan := PlanRetry(c, f.Stage, t.Snapshot())
		if plan.Retry && r.disabled {
			plan = RetryPlan{Decision: DecisionRetriesDisabled}
		}
		if plan.Retry {
			lock, err := st.CurrentLock(ctx, t.StackID)
			if err != nil {
				return fmt.Errorf("read stack lock: %w", err)
			}
			if !LockAllowsRetry(lock, t, f.FencingToken) {
				plan = RetryPlan{Decision: DecisionLockLost}
			}
		}
		now := r.clock.Now()
		rec := FailureRecord{ID: r.ids.NewID(), RequestID: t.ID, StackID: t.StackID, Run: f.Run, Stage: f.Stage,
			ExitCode: f.ExitCode, Class: c, Decision: plan.Decision, At: now}
		if plan.Retry {
			at := now.Add(plan.Delay)
			rec.RetryNumber, rec.RetryAt = plan.Number, &at
		}
		inserted, err := st.InsertFailure(ctx, rec)
		if err != nil {
			return fmt.Errorf("record failure: %w", err)
		}
		out = Outcome{Classification: c, Decision: plan.Decision, RetryNumber: rec.RetryNumber, RetryAt: rec.RetryAt}
		if !inserted {
			out = Outcome{Classification: c, Decision: plan.Decision, Duplicate: true}
			return nil
		}
		if err := r.audit(ctx, st, now, t, "request.apply_failure_classified", f.CorrelationID, map[string]any{
			"short_id": t.ShortID, "class": c.Class, "reason": c.Reason, "stage": f.Stage, "exit_code": f.ExitCode,
			"run": f.Run, "log_url": f.LogURL, "decision": plan.Decision, "retry_count": t.RetryCount,
		}); err != nil {
			return err
		}
		if !plan.Retry {
			return nil
		}
		return r.schedule(ctx, st, now, t, plan, *rec.RetryAt, f)
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

// schedule fires apply_retry on the request and enqueues the dispatch job.
func (r *Retrier) schedule(ctx context.Context, st RetryStore, now time.Time, t Target, plan RetryPlan, at time.Time, f FailedApply) error {
	if err := st.IncrementRetry(ctx, t.ID, t.RetryCount, now); err != nil {
		return err
	}
	if err := r.audit(ctx, st, now, t, "request.transitioned", f.CorrelationID, map[string]any{
		"short_id": t.ShortID, "event": requests.EventApplyRetry, "from": t.Status, "to": requests.StatusApplying,
		"retry_count": plan.Number, "run": f.Run,
	}); err != nil {
		return err
	}
	m, err := RetryMessage(t.ID, plan.Number, at)
	if err != nil {
		return err
	}
	if _, err := st.Enqueue(ctx, m); err != nil {
		return fmt.Errorf("enqueue retry: %w", err)
	}
	return r.audit(ctx, st, now, t, "request.apply_retry_scheduled", f.CorrelationID, map[string]any{
		"short_id": t.ShortID, "retry_number": plan.Number, "max_retries": requests.MaxApplyRetries,
		"delay_seconds": int(plan.Delay.Seconds()), "available_at": at, "workflow": ApplyWorkflow,
	})
}

func (r *Retrier) audit(ctx context.Context, st RetryStore, at time.Time, t Target, action, correlationID string, payload map[string]any) error {
	return appendRequestAudit(ctx, st, at, t, action, correlationID, payload)
}

// LockAllowsRetry decides whether the stack lock lets request t be retried: the lock is
// free, or t holds it with the reported fencing token (or no token was reported). A lock
// held by another request, or a stale token, refuses. Pure.
func LockAllowsRetry(current *locks.Lock, t Target, token *int64) bool {
	if current == nil {
		return true
	}
	if !current.HeldBy(t.ID) {
		return false
	}
	return token == nil || locks.CheckToken(current, t.ID, *token) == nil
}

// appendRequestAudit appends one request-scoped audit event by the recovery actor.
func appendRequestAudit(ctx context.Context, st interface {
	AppendAudit(ctx context.Context, e audit.Event) error
}, at time.Time, t Target, action, correlationID string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode audit payload: %w", err)
	}
	teamID := t.TeamID
	e := audit.Event{OccurredAt: at, ActorType: string(recoveryPrincipal.Type), ActorID: recoveryPrincipal.Login,
		Action: action, SubjectType: "request", SubjectID: t.ID.String(), CorrelationID: correlationID, Payload: body}
	if teamID != uuid.Nil {
		e.TeamID = &teamID
	}
	if err := st.AppendAudit(ctx, e); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}
