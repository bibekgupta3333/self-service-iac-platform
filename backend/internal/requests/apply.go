package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// StackApplied is the write of an apply's outcome onto the (locked) stack row.
type StackApplied struct {
	ID   uuid.UUID
	From StackStatus
	StackChange
	RequestID uuid.UUID
	CommitSHA string
	At        time.Time
}

// ApplyInput is one run report as the lifecycle sees it.
type ApplyInput struct {
	RequestID     uuid.UUID
	Progress      ApplyProgress
	By            identity.Principal
	CorrelationID string
}

// ApplyResult reports what applying a report did.
type ApplyResult struct {
	// Status is the request's status after the report.
	Status Status
	// Changed is true when the request or its stack moved.
	Changed bool
	// Ignored is the reason a report for a terminal request changed nothing.
	Ignored string
}

// Applier is the request and stack side of a run report, bound to the report's transaction
// (so the run row, the request, the stack, the lock and the audit chain commit together).
type Applier interface {
	Apply(ctx context.Context, in ApplyInput) (ApplyResult, error)
}

// ApplyDriver binds the lifecycle to a transaction. It is how the run service reaches the
// request service without owning a second connection.
type ApplyDriver struct {
	svc   *Service
	audit *audit.Writer
}

// NewApplyDriver returns an ApplyDriver writing audit events with w.
func NewApplyDriver(svc *Service, w *audit.Writer) *ApplyDriver {
	return &ApplyDriver{svc: svc, audit: w}
}

// ForTx returns the Applier that runs inside tx.
func (d *ApplyDriver) ForTx(tx pgx.Tx) Applier {
	return txApplier{svc: d.svc, st: &pgStore{tx: tx, audit: d.audit}}
}

type txApplier struct {
	svc *Service
	st  Store
}

func (a txApplier) Apply(ctx context.Context, in ApplyInput) (ApplyResult, error) {
	return a.svc.ApplyIn(ctx, a.st, in)
}

// ApplyIn applies one run report to a request and its stack inside st's transaction.
//
// The stacks row is locked first and the request second (the order every lock operation
// uses), the pure DecideApply says what changes, and every change is audited: each request
// event as request.transitioned (through commit, which also releases the stack lock and
// promotes the next waiter when the request became terminal), and the stack's move as
// stack.status_changed. Replays and late reports are no-ops by DecideApply.
func (s *Service) ApplyIn(ctx context.Context, st Store, in ApplyInput) (ApplyResult, error) {
	found, err := st.GetRequest(ctx, in.RequestID)
	if err != nil {
		return ApplyResult{}, err
	}
	target, err := st.LockStackTarget(ctx, found.Stack.ID)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("lock stack: %w", err)
	}
	r, err := st.LockRequest(ctx, in.RequestID)
	if err != nil {
		return ApplyResult{}, err
	}
	d, err := DecideApply(r.Status, r.Kind, target.Status, in.Progress)
	if err != nil {
		return ApplyResult{}, err
	}
	res := ApplyResult{Status: r.Status, Ignored: d.Ignore}
	run := map[string]any{"run": in.Progress.Run}
	switch {
	case d.Ignore != "":
		return res, s.audit(ctx, st, in.By, "request.run_report_ignored", r.ID, in.CorrelationID, map[string]any{
			"short_id": r.ShortID, "status": r.Status, "reason": d.Ignore, "run": in.Progress.Run, "phase": in.Progress.Phase})
	case d.ReleaseFailed:
		return res, s.auditReleaseFailure(ctx, st, in, r)
	}

	for i, ev := range d.Events {
		last := i == len(d.Events)-1
		if last {
			// The stack moves before the final commit: a terminal commit releases the lock
			// and promotes the next waiter, which must find the stack in its new state.
			if err := s.applyStack(ctx, st, in, r, target, d.Stack); err != nil {
				return ApplyResult{}, err
			}
			d.Stack = nil
			if in.Progress.Phase == PhaseFailed {
				r.FailureReason, r.FailureDetail = in.Progress.FailureReason, in.Progress.FailureDetail
			}
		}
		from := r.Status
		if err := r.Fire(ev, s.clock.Now()); err != nil {
			return ApplyResult{}, err
		}
		if err := s.commit(ctx, st, r, from, ev, in.By, in.CorrelationID, run); err != nil {
			return ApplyResult{}, err
		}
		res.Changed = true
	}
	if d.Stack != nil { // no event left to carry it: a started report on an applying request
		if err := s.applyStack(ctx, st, in, r, target, d.Stack); err != nil {
			return ApplyResult{}, err
		}
		res.Changed = true
	}
	res.Status = r.Status
	return res, nil
}

// applyStack writes the stack change of an apply and audits it. A nil change is a no-op.
func (s *Service) applyStack(ctx context.Context, st Store, in ApplyInput, r *Request, target StackTarget, c *StackChange) error {
	if c == nil {
		return nil
	}
	at := s.clock.Now()
	w := StackApplied{ID: target.ID, From: target.Status, StackChange: *c, RequestID: r.ID, At: at}
	if c.Applied {
		w.CommitSHA = in.Progress.Run.HeadSHA
	}
	if err := st.UpdateStackApplied(ctx, w); err != nil {
		return err
	}
	payload := map[string]any{"request_id": r.ID, "short_id": r.ShortID, "kind": r.Kind, "path": target.Path,
		"from": target.Status, "to": c.To, "run": in.Progress.Run}
	if c.Health != "" {
		payload["health"] = c.Health
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode audit payload: %w", err)
	}
	actorType, actorID := string(in.By.Type), in.By.AuditActorID()
	if actorType == "" {
		actorType, actorID = audit.ActorSystem, "iacp"
	}
	evs := []audit.Event{{OccurredAt: at, ActorType: actorType, ActorID: actorID, Action: "stack.status_changed",
		SubjectType: "stack", SubjectID: target.ID.String(), TeamID: &target.TeamID, CorrelationID: in.CorrelationID, Payload: body}}
	if c.Destroyed {
		evs = append(evs, audit.Event{OccurredAt: at, ActorType: actorType, ActorID: actorID, Action: "stack.destroyed",
			SubjectType: "stack", SubjectID: target.ID.String(), TeamID: &target.TeamID, CorrelationID: in.CorrelationID, Payload: body})
	}
	for _, e := range evs {
		if err := st.AppendAudit(ctx, e); err != nil {
			return fmt.Errorf("audit %s: %w", e.Action, err)
		}
	}
	return nil
}

// auditReleaseFailure records that apply.yml could not release the stack lock, with the
// lock as it stands now so an operator sees at once whether the stack is still held. It
// never changes the request: an applied request stays applied.
func (s *Service) auditReleaseFailure(ctx context.Context, st Store, in ApplyInput, r *Request) error {
	cur, err := st.Locks().Current(ctx, r.Stack.ID)
	if err != nil {
		return fmt.Errorf("read stack lock: %w", err)
	}
	payload := map[string]any{"short_id": r.ShortID, "status": r.Status, "error": in.Progress.ReleaseFailure,
		"run": in.Progress.Run, "lock_held": cur != nil}
	if cur != nil {
		payload["lock_request_id"], payload["lock_holder"], payload["lock_expires_at"] = cur.RequestID, cur.Holder, cur.ExpiresAt
		payload["held_by_this_request"] = cur.HeldBy(r.ID)
	}
	return s.audit(ctx, st, in.By, "request.lock_release_failed", r.ID, in.CorrelationID, payload)
}
