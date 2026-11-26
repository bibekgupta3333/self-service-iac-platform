package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Dispatch states of a scheduled retry (apply_failures.dispatch_state).
const (
	DispatchScheduled   = "scheduled"
	DispatchDispatching = "dispatching"
	DispatchDispatched  = "dispatched"
	DispatchFailed      = "dispatch_failed"
	DispatchSkipped     = "skipped"
)

// Dispatcher is the outbox handler of recovery.apply_retry jobs: it starts apply.yml again
// for a request through the publisher's DispatchWorkflow.
//
// A dispatch is never retried. Re-sending workflow_dispatch after an ambiguous failure (a
// timeout whose request did reach GitHub) could start two applies, so the retry row is
// claimed (scheduled → dispatching) in a transaction before GitHub is called, a failure
// is recorded as dispatch_failed and the job ends as dead (outbox.ErrPermanent), and a job
// that runs again after a crash finds the row already claimed and does nothing.
type Dispatcher struct {
	repo  RetryRepository
	pub   publish.Publisher
	clock platform.Clock
	ref   string // branch the workflow runs on (main)
}

// NewDispatcher returns a Dispatcher dispatching on ref (the publish base branch, main).
func NewDispatcher(repo RetryRepository, pub publish.Publisher, clock platform.Clock, ref string) *Dispatcher {
	if ref == "" {
		ref = "main"
	}
	return &Dispatcher{repo: repo, pub: pub, clock: clock, ref: ref}
}

var _ outbox.Handler = (*Dispatcher)(nil)

// SkipReason decides whether a scheduled retry n may still be dispatched for request t
// under the stack lock current: the request must still be applying with retry_count n
// (otherwise it moved on or another retry superseded this one), and the lock must still
// let it run (LockAllowsRetry, without a token: the request's own token is checked by the
// lock claim of the dispatched run). Empty means dispatch. Pure.
func SkipReason(t Target, n int, current *locks.Lock) string {
	switch {
	case t.Status != requests.StatusApplying:
		return DecisionRequestNotApplying
	case t.RetryCount != n:
		return "superseded"
	case current != nil && !current.HeldBy(t.ID):
		return DecisionLockLost
	}
	return ""
}

// Handle implements outbox.Handler.
func (d *Dispatcher) Handle(ctx context.Context, j outbox.Job) error {
	var p RetryPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.RequestID == uuid.Nil || p.RetryNumber < 1 {
		return fmt.Errorf("decode retry job %d: %w", j.ID, outbox.ErrPermanent)
	}
	correlationID := fmt.Sprintf("outbox-%d", j.ID)
	t, claimed, err := d.claim(ctx, p, correlationID)
	if err != nil {
		if apperr.IsKind(err, apperr.KindNotFound) {
			return fmt.Errorf("retry %s: %w: %w", p.RequestID, err, outbox.ErrPermanent)
		}
		return err // nothing was claimed yet: the worker may try again
	}
	if !claimed {
		return nil
	}
	dispatch := publish.Dispatch{Workflow: ApplyWorkflow, Ref: d.ref, Inputs: DispatchInputs(t)}
	derr := d.pub.DispatchWorkflow(ctx, dispatch)
	if rerr := d.finish(ctx, t, p.RetryNumber, dispatch, derr, correlationID); rerr != nil {
		if derr != nil {
			return fmt.Errorf("dispatch %s: %w (recording it: %w): %w", ApplyWorkflow, derr, rerr, outbox.ErrPermanent)
		}
		// The workflow started; only the bookkeeping failed. Never dispatch again.
		observability.Logger(ctx).ErrorContext(ctx, "apply retry dispatched but not recorded", "request", t.ShortID,
			"retry", p.RetryNumber, "error", rerr)
		return fmt.Errorf("record dispatch of %s: %w: %w", t.ShortID, rerr, outbox.ErrPermanent)
	}
	if derr != nil {
		return fmt.Errorf("dispatch %s for %s: %w: %w", ApplyWorkflow, t.ShortID, derr, outbox.ErrPermanent)
	}
	observability.Logger(ctx).InfoContext(ctx, "apply retry dispatched", "request", t.ShortID, "retry", p.RetryNumber)
	return nil
}

// claim checks the retry is still due and moves it to dispatching, or records why it is
// skipped. claimed is false when there is nothing to dispatch.
func (d *Dispatcher) claim(ctx context.Context, p RetryPayload, correlationID string) (Target, bool, error) {
	var (
		t       Target
		claimed bool
	)
	err := d.repo.InTx(ctx, func(st RetryStore) error {
		var err error
		if t, err = st.TargetByID(ctx, p.RequestID); err != nil {
			return err
		}
		now := d.clock.Now()
		ok, err := st.ClaimDispatch(ctx, t.ID, p.RetryNumber, now)
		if err != nil {
			return fmt.Errorf("claim retry: %w", err)
		}
		if !ok {
			return nil // already dispatched, failed or skipped by an earlier run of this job
		}
		lock, err := st.CurrentLock(ctx, t.StackID)
		if err != nil {
			return fmt.Errorf("read stack lock: %w", err)
		}
		if reason := SkipReason(t, p.RetryNumber, lock); reason != "" {
			if err := st.FinishDispatch(ctx, t.ID, p.RetryNumber, DispatchSkipped, reason, now); err != nil {
				return err
			}
			return appendRequestAudit(ctx, st, now, t, "request.apply_retry_skipped", correlationID, map[string]any{
				"short_id": t.ShortID, "retry_number": p.RetryNumber, "reason": reason, "status": t.Status,
				"retry_count": t.RetryCount,
			})
		}
		claimed = true
		return nil
	})
	return t, claimed, err
}

// finish records the dispatch outcome and audits it. It runs on a fresh context, since the
// handler's may be what expired, so a dispatch that happened is not left unrecorded.
func (d *Dispatcher) finish(ctx context.Context, t Target, n int, dispatch publish.Dispatch, derr error, correlationID string) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	state, action, detail := DispatchDispatched, "request.apply_retry_dispatched", ""
	if derr != nil {
		state, action, detail = DispatchFailed, "request.apply_retry_dispatch_failed", truncate(derr.Error(), 2000)
	}
	return d.repo.InTx(fctx, func(st RetryStore) error {
		now := d.clock.Now()
		if err := st.FinishDispatch(fctx, t.ID, n, state, detail, now); err != nil {
			return err
		}
		payload := map[string]any{"short_id": t.ShortID, "retry_number": n, "workflow": dispatch.Workflow,
			"ref": dispatch.Ref, "inputs": dispatch.Inputs, "publisher": d.pub.Name()}
		if detail != "" {
			payload["error"] = detail
		}
		return appendRequestAudit(fctx, st, now, t, action, correlationID, payload)
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
