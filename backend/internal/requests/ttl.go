package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

// TTLJustification is the justification of the destroy request the reaper opens.
const TTLJustification = "ttl expired"

// TTLActor is the audit actor of automatic teardowns.
const TTLActor = "system:ttl-reaper"

var _ ttl.Destroyer = (*Service)(nil)

// ttlStack maps a locked stack to the view the TTL rules use. Pure.
func ttlStack(t StackTarget) ttl.Stack {
	return ttl.Stack{ID: t.ID, TeamID: t.TeamID, Team: t.TeamSlug, Environment: t.Environment,
		Name: t.Name, Path: t.Path, Status: string(t.Status), IsEphemeral: t.IsEphemeral, ExpiresAt: t.ExpiresAt}
}

// OpenTTLDestroy opens a destroy request for an expired ephemeral stack on behalf of the
// platform (cmd.By is the system actor; the justification is "ttl expired"). Under the
// stack row lock it re-checks that the stack is ephemeral, outside production and past its
// expiry, so a late extension or a stale due list can never destroy a live stack. A destroy
// that is already open is reported as not created, which makes the reaper idempotent. The
// request then follows the normal path: lock or queue, PR, plan -destroy, apply.
func (s *Service) OpenTTLDestroy(ctx context.Context, cmd ttl.DestroyCommand) (ttl.DestroyResult, error) {
	var res ttl.DestroyResult
	err := s.repo.InTx(ctx, func(st Store) error {
		t, err := st.LockStackTarget(ctx, cmd.StackID)
		if err != nil {
			return stackLookupError(err, cmd.StackID)
		}
		now := s.clock.Now()
		view := ttlStack(t)
		if err := ttl.Eligible(view); err != nil {
			return err
		}
		if ttl.PhaseAt(view, now) != ttl.PhaseTeardown {
			return apperr.Conflict("ttl_not_due", fmt.Sprintf("stack %s has not expired (expires %s)", t.Path,
				t.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")))
		}
		open, err := st.OpenRequestKinds(ctx, t.ID)
		if err != nil {
			return fmt.Errorf("list open requests: %w", err)
		}
		if slices.Contains(open, KindDestroy) {
			res = ttl.DestroyResult{Created: false}
			return nil
		}
		if err := CheckStackAllowsKind(KindDestroy, t.Status, open); err != nil {
			return err
		}
		// Backoff: a TTL destroy that failed, was rejected or cancelled is not reopened every
		// hour. After MaxAttempts the stack is blocked: recorded once as ttl.blocked and left
		// to a human (extend, or fix and destroy by hand).
		attempts, err := st.TTLDestroyAttempts(ctx, t.ID)
		if err != nil {
			return err
		}
		view.Attempts = attempts
		switch state, until := s.ttl.Backoff(view, now); state {
		case ttl.BackoffWait:
			return apperr.Conflict("ttl_backoff", fmt.Sprintf("the last TTL destroy of %s did not succeed; the next attempt is not before %s",
				t.Path, until.UTC().Format("2006-01-02T15:04:05Z")))
		case ttl.BackoffBlocked:
			recorded, err := st.TTLBlockedSince(ctx, t.ID, *t.ExpiresAt)
			if err != nil {
				return err
			}
			if !recorded {
				payload, err := json.Marshal(map[string]any{"stack_path": t.Path, "expires_at": t.ExpiresAt.UTC(),
					"attempts": len(attempts)})
				if err != nil {
					return fmt.Errorf("encode audit payload: %w", err)
				}
				if err := st.AppendAudit(ctx, audit.Event{OccurredAt: now, ActorType: audit.ActorSystem, ActorID: TTLActor,
					Action: "ttl.blocked", SubjectType: "stack", SubjectID: t.ID.String(), TeamID: &t.TeamID,
					CorrelationID: cmd.CorrelationID, Payload: payload}); err != nil {
					return fmt.Errorf("audit ttl.blocked: %w", err)
				}
			}
			// The transaction commits so that the audit event survives; the caller turns
			// Blocked into the ttl_blocked conflict.
			res = ttl.DestroyResult{Blocked: true}
			return nil
		}
		in := CreateInput{Kind: KindDestroy, StackID: &t.ID, ConfirmStackName: t.Name, Justification: TTLJustification}
		req, err := s.record(ctx, st, cmd.By, CreateCommand{Input: in, CorrelationID: cmd.CorrelationID},
			StackRef{ID: t.ID, Path: t.Path, Status: string(t.Status)}, t.TeamID, NormalizeForStack(in, t))
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"request_id": req.ID, "short_id": req.ShortID,
			"stack_path": t.Path, "expires_at": t.ExpiresAt.UTC()})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		if err := st.AppendAudit(ctx, audit.Event{OccurredAt: now, ActorType: audit.ActorSystem, ActorID: TTLActor,
			Action: "ttl.expired", SubjectType: "stack", SubjectID: t.ID.String(), TeamID: &t.TeamID,
			CorrelationID: cmd.CorrelationID, Payload: payload}); err != nil {
			return fmt.Errorf("audit ttl.expired: %w", err)
		}
		if !req.IsQueued() {
			if err := s.enqueuePublish(ctx, st, req.ID, req.ShortID, cmd.By, cmd.CorrelationID); err != nil {
				return err
			}
		}
		res = ttl.DestroyResult{Created: true, RequestID: req.ID, ShortID: req.ShortID, Queued: req.IsQueued()}
		return nil
	})
	if err != nil {
		return ttl.DestroyResult{}, err
	}
	return res, nil
}
