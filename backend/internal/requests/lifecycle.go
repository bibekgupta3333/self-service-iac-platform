package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// PublishPayload is the body of a request.publish outbox job.
type PublishPayload struct {
	RequestID uuid.UUID `json:"request_id"`
}

// PublishDedupeKey keeps one pending publish job per request. Pure.
func PublishDedupeKey(id uuid.UUID) string { return "publish:" + id.String() }

// PublishMessage is the request.publish outbox job for a request. Pure.
func PublishMessage(id uuid.UUID, at time.Time) (outbox.Message, error) {
	payload, err := json.Marshal(PublishPayload{RequestID: id})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode publish job: %w", err)
	}
	return outbox.Message{Topic: outbox.TopicRequestPublish, AggregateID: &id, DedupeKey: PublishDedupeKey(id),
		Payload: payload, AvailableAt: at}, nil
}

// requestEvent builds a request-scoped audit event. Pure.
func requestEvent(at time.Time, by identity.Principal, action string, requestID uuid.UUID, correlationID string,
	payload map[string]any) (audit.Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return audit.Event{}, fmt.Errorf("encode audit payload: %w", err)
	}
	actorType, actorID := string(by.Type), by.AuditActorID()
	if actorType == "" {
		actorType = audit.ActorSystem
	}
	if actorID == "" {
		actorID = "iacp"
	}
	return audit.Event{OccurredAt: at, ActorType: actorType, ActorID: actorID, Action: action, SubjectType: "request",
		SubjectID: requestID.String(), CorrelationID: correlationID, Payload: body}, nil
}

// jobQueue is the transactional surface both requests.Store and locks.Store offer.
type jobQueue interface {
	Enqueue(ctx context.Context, m outbox.Message) (bool, error)
	AppendAudit(ctx context.Context, e audit.Event) error
}

// schedulePublish enqueues the publish job for a lock holder and audits it; a job already
// pending for the request is left alone. It is the one path for both new lock holders
// (Create) and promoted waiters (PublishOnPromote).
func schedulePublish(ctx context.Context, q jobQueue, at time.Time, id uuid.UUID, shortID string, by identity.Principal,
	correlationID string) error {
	m, err := PublishMessage(id, at)
	if err != nil {
		return err
	}
	queued, err := q.Enqueue(ctx, m)
	if err != nil {
		return fmt.Errorf("enqueue publish: %w", err)
	}
	if !queued {
		return nil
	}
	e, err := requestEvent(at, by, "request.publish_enqueued", id, correlationID, map[string]any{"short_id": shortID})
	if err != nil {
		return err
	}
	if err := q.AppendAudit(ctx, e); err != nil {
		return fmt.Errorf("audit request.publish_enqueued: %w", err)
	}
	return nil
}

// PublishOnPromote is the locks.PromotionHook that schedules publishing for a request
// promoted to lock holder, whether by release (DELETE …/lock, a terminal transition) or
// by an expired lease handed to the head of the queue. Register it with
// locks.Service.OnPromote when a publisher is configured.
func PublishOnPromote(clock platform.Clock) locks.PromotionHook {
	return func(ctx context.Context, st locks.Store, req locks.RequestInfo, _ locks.Lock, by identity.Principal, correlationID string) error {
		return schedulePublish(ctx, st, clock.Now(), req.ID, req.ShortID, by, correlationID)
	}
}

// enqueuePublish schedules publishing for a request that just took the lock, when
// publishing is enabled.
func (s *Service) enqueuePublish(ctx context.Context, st Store, id uuid.UUID, shortID string, by identity.Principal, correlationID string) error {
	if !s.publishing {
		return nil
	}
	return schedulePublish(ctx, st, s.clock.Now(), id, shortID, by, correlationID)
}

// audit appends one request-scoped audit event.
func (s *Service) audit(ctx context.Context, st Store, by identity.Principal, action string, requestID uuid.UUID,
	correlationID string, payload map[string]any) error {
	e, err := requestEvent(s.clock.Now(), by, action, requestID, correlationID, payload)
	if err != nil {
		return err
	}
	if err := st.AppendAudit(ctx, e); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// lockForTransition takes the stack row lock first (the order every lock operation uses,
// so a transition that releases the lock cannot deadlock with lock acquisition or run
// reports) and then loads the request FOR UPDATE.
func (s *Service) lockForTransition(ctx context.Context, st Store, id uuid.UUID) (*Request, error) {
	r, err := st.GetRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := st.Locks().LockStack(ctx, r.Stack.ID); err != nil {
		return nil, fmt.Errorf("lock stack: %w", err)
	}
	return st.LockRequest(ctx, id)
}

// commit persists a transition of r from status from, audits it as request.transitioned,
// and when r became terminal releases its stack lock (promoting the next waiter; the
// lock service's promotion hook schedules its publish job).
func (s *Service) commit(ctx context.Context, st Store, r *Request, from Status, event Event, by identity.Principal,
	correlationID string, extra map[string]any) error {
	if err := st.UpdateRequest(ctx, r, from); err != nil {
		return err
	}
	payload := map[string]any{"short_id": r.ShortID, "event": event, "from": from, "to": r.Status}
	for k, v := range extra {
		payload[k] = v
	}
	if err := s.audit(ctx, st, by, "request.transitioned", r.ID, correlationID, payload); err != nil {
		return err
	}
	if !r.Status.IsTerminal() || r.FencingToken == nil {
		return nil
	}
	_, err := s.locker.ReleaseIn(ctx, st.Locks(), locks.TokenCommand{StackID: r.Stack.ID, RequestID: r.ID,
		FencingToken: *r.FencingToken, By: by, CorrelationID: correlationID})
	if apperr.IsKind(err, apperr.KindConflict) {
		return nil // the lease was already released or taken over: nothing of ours to free
	}
	if err != nil {
		return fmt.Errorf("release stack lock: %w", err)
	}
	return nil
}

// PublishedPR is what the publisher produced for a request.
type PublishedPR struct {
	Publisher    string // adapter name
	Branch       string
	CommitSHA    string
	TreeSHA      string
	Number       int
	URL          string
	FencingToken int64 // token the files were rendered with
}

// publisherPrincipal is the actor of publisher-driven transitions.
func publisherPrincipal(name string) identity.Principal {
	return identity.Principal{Login: "publisher:" + name, Type: identity.ActorSystem}
}

// RecordPublished fires pr_opened and stores the branch and PR (second step of the
// publish flow; the first, outside any transaction, wrote to the repository). Recording
// the same PR again is a no-op, so a retried job is safe. The request must still hold
// the lock with the token the files were rendered with.
func (s *Service) RecordPublished(ctx context.Context, id uuid.UUID, p PublishedPR, correlationID string) (*Request, error) {
	var out *Request
	err := s.repo.InTx(ctx, func(st Store) error {
		r, err := s.lockForTransition(ctx, st, id)
		if err != nil {
			return err
		}
		if r.PR != nil && r.PR.Number == p.Number && r.Status != StatusDraft {
			out = r
			return nil
		}
		if r.Status != StatusDraft {
			return apperr.Conflict("invalid_transition", fmt.Sprintf("request %s is %s, not draft", r.ShortID, r.Status))
		}
		// requests.fencing_token is not cleared on release or takeover, so check the current
		// lease itself. The stacks row is locked, so the lease cannot change before commit.
		if err := holdsLock(ctx, st, r, p.FencingToken); err != nil {
			return err
		}
		from := r.Status
		if err := r.Fire(EventPROpened, s.clock.Now()); err != nil {
			return err
		}
		head := ""
		if shaRe.MatchString(p.CommitSHA) {
			head = p.CommitSHA
		}
		r.PR, r.RenderedTreeSHA = &PRLink{Branch: p.Branch, Number: p.Number, URL: p.URL, HeadSHA: head}, p.TreeSHA
		if err := s.commit(ctx, st, r, from, EventPROpened, publisherPrincipal(p.Publisher), correlationID, map[string]any{
			"publisher": p.Publisher, "branch": p.Branch, "commit_sha": p.CommitSHA, "tree_sha": p.TreeSHA,
			"pr_number": p.Number, "pr_url": p.URL, "fencing_token": p.FencingToken,
		}); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PublishFailure is one failed publish attempt.
type PublishFailure struct {
	Publisher string
	Attempt   int
	Error     string
	// Final means the worker gives up: the request fires publish_failed and releases its lock.
	Final bool
	// Cleanup describes the best-effort withdrawal of an unrecorded branch/PR (final only).
	Cleanup string
}

// RecordPublishFailure audits a failed publish attempt; on the final attempt a draft
// request moves to failed (failure_reason publish_failed). A request that already left
// draft is only audited.
func (s *Service) RecordPublishFailure(ctx context.Context, id uuid.UUID, f PublishFailure, correlationID string) error {
	return s.repo.InTx(ctx, func(st Store) error {
		r, err := s.lockForTransition(ctx, st, id)
		if err != nil {
			return err
		}
		by := publisherPrincipal(f.Publisher)
		if err := s.audit(ctx, st, by, "request.publish_failed", r.ID, correlationID, map[string]any{
			"short_id": r.ShortID, "publisher": f.Publisher, "attempt": f.Attempt, "error": f.Error, "final": f.Final,
			"cleanup": f.Cleanup,
		}); err != nil {
			return err
		}
		if !f.Final || r.Status != StatusDraft {
			return nil
		}
		from := r.Status
		if err := r.Fire(EventPublishFailed, s.clock.Now()); err != nil {
			return err
		}
		detail, err := json.Marshal(map[string]any{"publisher": f.Publisher, "attempts": f.Attempt, "error": f.Error})
		if err != nil {
			return fmt.Errorf("encode failure detail: %w", err)
		}
		r.FailureReason, r.FailureDetail = "publish_failed", detail
		return s.commit(ctx, st, r, from, EventPublishFailed, by, correlationID, nil)
	})
}

// GitHubEvent is a webhook-driven event for one request.
type GitHubEvent struct {
	RequestShortID string
	PRNumber       int
	Event          Event
	MergeCommitSHA string // pr_merged only
	DeliveryID     string // X-GitHub-Delivery, recorded in the audit payload
	Action         string // webhook action, for the audit trail
	HeadSHA        string // pull_request.head.sha (after the push, for synchronize)
	BeforeSHA      string // synchronize only: the head before the push
}

// ErrNotYetPublished means a webhook names a request whose PR is not recorded yet (the
// event raced RecordPublished). It is returned as an error so the outbox retries it.
var ErrNotYetPublished = errors.New("pull request not recorded yet")

// HeadCheck decides whether a push (new_commit) continues the PR head the platform knows:
// its before SHA must be the known head. ok is false for a replayed or out-of-order push,
// whose replay could otherwise clear an approval. Every other event passes: a close or a
// merge is a real, status-guarded event (rejected is terminal), and dropping it because
// an earlier push was lost would leave the request holding its stack lock forever. An
// unknown known head (published before head tracking) accepts everything. Pure.
func HeadCheck(known string, e GitHubEvent) (ok bool) {
	if known == "" || e.Event != EventNewCommit {
		return true
	}
	return e.BeforeSHA == known
}

// EventOutcome reports what ApplyGitHubEvent did.
type EventOutcome struct {
	Applied bool
	Reason  string // why it was ignored: unknown_request, pr_mismatch, stale_head, or the transition error code
	From    Status
	To      Status
	// KnownHead is the PR head the platform had when the event was applied; with
	// Reason stale_head the caller can re-read the real head and resync from it.
	KnownHead string
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// webhookPrincipal is the actor of webhook-driven changes.
var webhookPrincipal = identity.Principal{Login: "github", Type: identity.ActorGitHubWebhook}

// ApplyGitHubEvent fires a webhook-driven transition. It is idempotent: an event that is
// not allowed in the current status (a redelivery, or a merge of an unapproved PR) is
// audited as request.github_event_ignored and reported, not returned as an error.
func (s *Service) ApplyGitHubEvent(ctx context.Context, e GitHubEvent) (EventOutcome, error) {
	var out EventOutcome
	err := s.repo.InTx(ctx, func(st Store) error {
		found, err := st.GetRequestByShortID(ctx, e.RequestShortID)
		if apperr.IsKind(err, apperr.KindNotFound) {
			out.Reason = "unknown_request"
			return nil
		}
		if err != nil {
			return err
		}
		r, err := s.lockForTransition(ctx, st, found.ID)
		if err != nil {
			return err
		}
		out.From, out.To = r.Status, r.Status
		ignore := func(reason string) error {
			out.Reason = reason
			return s.audit(ctx, st, webhookPrincipal, "request.github_event_ignored", r.ID, e.DeliveryID, map[string]any{
				"short_id": r.ShortID, "event": e.Event, "action": e.Action, "status": r.Status, "reason": reason,
				"delivery_id": e.DeliveryID, "pr_number": e.PRNumber, "head_sha": e.HeadSHA,
			})
		}
		if r.PR == nil && r.Status == StatusDraft {
			return fmt.Errorf("%s: %w", r.ShortID, ErrNotYetPublished) // retried by the outbox
		}
		if r.PR == nil || r.PR.Number != e.PRNumber {
			return ignore("pr_mismatch")
		}
		out.KnownHead = r.PR.HeadSHA
		if !HeadCheck(r.PR.HeadSHA, e) {
			return ignore("stale_head")
		}
		from := r.Status
		if e.Event == EventNewCommit && shaRe.MatchString(e.HeadSHA) {
			r.PR.HeadSHA = e.HeadSHA
		}
		if err := r.Fire(e.Event, s.clock.Now()); err != nil {
			if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindConflict {
				// The push still moved the head: keep tracking it so later events match.
				if e.Event == EventNewCommit {
					if err := st.UpdateRequest(ctx, r, from); err != nil {
						return err
					}
				}
				return ignore(ae.Code)
			}
			return err
		}
		if e.Event == EventPRMerged && shaRe.MatchString(e.MergeCommitSHA) {
			r.MergeCommitSHA = e.MergeCommitSHA
		}
		if err := s.commit(ctx, st, r, from, e.Event, webhookPrincipal, e.DeliveryID, map[string]any{
			"action": e.Action, "delivery_id": e.DeliveryID, "pr_number": e.PRNumber, "merge_commit_sha": r.MergeCommitSHA,
		}); err != nil {
			return err
		}
		out = EventOutcome{Applied: true, From: from, To: r.Status, KnownHead: out.KnownHead}
		return nil
	})
	if err != nil {
		return EventOutcome{}, err
	}
	return out, nil
}

// WorkflowHint is a completed workflow run seen through a webhook. It does not move the
// request (run reports on POST /v1/runs do, WBS 5.2/5.3); it is recorded for the timeline.
type WorkflowHint struct {
	RequestShortID string
	Workflow       string // workflow file path, e.g. .github/workflows/pr-validate.yml
	RunID          int64
	Conclusion     string
	HeadSHA        string
	URL            string
	DeliveryID     string
}

// RecordWorkflowHint audits a completed workflow run against its request. Unknown requests
// are ignored (ok=false).
func (s *Service) RecordWorkflowHint(ctx context.Context, h WorkflowHint) (bool, error) {
	ok := false
	err := s.repo.InTx(ctx, func(st Store) error {
		r, err := st.GetRequestByShortID(ctx, h.RequestShortID)
		if apperr.IsKind(err, apperr.KindNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = true
		return s.audit(ctx, st, webhookPrincipal, "request.workflow_run_completed", r.ID, h.DeliveryID, map[string]any{
			"short_id": r.ShortID, "status": r.Status, "workflow": h.Workflow, "run_id": h.RunID,
			"conclusion": h.Conclusion, "head_sha": h.HeadSHA, "url": h.URL, "delivery_id": h.DeliveryID,
		})
	})
	return ok, err
}

// ErrNotPublishable is returned by PublishTarget for requests the publisher must skip.
var ErrNotPublishable = errors.New("request is not publishable")

// PublishTarget loads a request for publishing. It returns ErrNotPublishable (wrapped)
// when the request left draft, waits behind another request, or does not hold the
// current lease of its stack (released, or taken over after expiry).
func (s *Service) PublishTarget(ctx context.Context, id uuid.UUID) (*Request, error) {
	var out *Request
	err := s.repo.InTx(ctx, func(st Store) error {
		r, err := st.GetRequest(ctx, id)
		if err != nil {
			return err
		}
		out = r
		switch {
		case r.Status != StatusDraft:
			return fmt.Errorf("%w: %s is %s", ErrNotPublishable, r.ShortID, r.Status)
		case r.IsQueued():
			return fmt.Errorf("%w: %s is queued behind %s", ErrNotPublishable, r.ShortID, *r.QueuedBehind)
		case r.FencingToken == nil:
			return fmt.Errorf("%w: %s holds no stack lock", ErrNotPublishable, r.ShortID)
		}
		if err := holdsLock(ctx, st, r, *r.FencingToken); err != nil {
			return fmt.Errorf("%w: %w", ErrNotPublishable, err)
		}
		return nil
	})
	return out, err
}

// holdsLock checks that r holds the current lease of its stack with token (pure check
// over the lock row read in the caller's transaction).
func holdsLock(ctx context.Context, st Store, r *Request, token int64) error {
	current, err := st.Locks().Current(ctx, r.Stack.ID)
	if err != nil {
		return fmt.Errorf("read stack lock: %w", err)
	}
	return locks.CheckToken(current, r.ID, token)
}
