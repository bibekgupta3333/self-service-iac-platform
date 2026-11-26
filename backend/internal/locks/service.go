package locks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// RequestInfo is the part of a request the lock service needs.
type RequestInfo struct {
	ID        uuid.UUID
	ShortID   string
	StackID   uuid.UUID
	Completed bool // terminal status (completed_at is set)
	// FencingToken is the token of the lease the request was last granted (it is kept after
	// the lease is released).
	FencingToken *int64
	// QueuedBehind is the request this one currently waits for, if any.
	QueuedBehind *uuid.UUID
}

// Grant is a new lease to write. The store assigns the fencing token.
type Grant struct {
	StackID    uuid.UUID
	RequestID  uuid.UUID
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

// Store is the transactional persistence port. Every method runs in one transaction.
type Store interface {
	// LockStack takes the stack row lock (SELECT … FOR UPDATE) that serialises every lock
	// operation on the stack, and returns the owning team. NotFound if the stack is missing.
	LockStack(ctx context.Context, stackID uuid.UUID) (teamID uuid.UUID, err error)
	Request(ctx context.Context, id uuid.UUID) (RequestInfo, error)
	// Current returns the lock row, or nil when the stack is not locked.
	Current(ctx context.Context, stackID uuid.UUID) (*Lock, error)
	// Waiters returns the open requests queued on the stack.
	Waiters(ctx context.Context, stackID uuid.UUID) ([]Waiter, error)
	// Grant writes the lease with a fresh fencing token (insert, or replace an expired
	// row), records the token on the request and clears its queued_behind.
	Grant(ctx context.Context, g Grant) (Lock, error)
	// Extend moves the lease expiry of the current row and keeps its token.
	Extend(ctx context.Context, stackID uuid.UUID, expiresAt time.Time) (Lock, error)
	Delete(ctx context.Context, stackID uuid.UUID) error
	// Queue sets requests.queued_behind of request.
	Queue(ctx context.Context, request, behind uuid.UUID) error
	// Requeue points every other waiter of the stack at behind (the new holder).
	Requeue(ctx context.Context, stackID, behind uuid.UUID) error
	AppendAudit(ctx context.Context, e audit.Event) error
	// Enqueue adds an outbox job in the same transaction (used by promotion hooks).
	Enqueue(ctx context.Context, m outbox.Message) (bool, error)
}

// PromotionHook runs inside the grant transaction whenever a queued request becomes the
// lock holder: on release, or when an expired lease is handed to the head of the queue.
// It is the single place follow-up work for a promoted request is scheduled.
type PromotionHook func(ctx context.Context, st Store, req RequestInfo, l Lock, by identity.Principal, correlationID string) error

// Repository is the persistence port of the lock service.
type Repository interface {
	InTx(ctx context.Context, fn func(Store) error) error
	// Get returns the lock of a stack, or nil when it is not locked.
	Get(ctx context.Context, stackID uuid.UUID) (*Lock, error)
}

// AcquireCommand asks for the lock of a stack on behalf of a request.
type AcquireCommand struct {
	StackID       uuid.UUID
	RequestID     uuid.UUID
	By            identity.Principal
	CorrelationID string
}

// TokenCommand identifies a lease by its holder and fencing token (heartbeat, release).
type TokenCommand struct {
	StackID       uuid.UUID
	RequestID     uuid.UUID
	FencingToken  int64
	By            identity.Principal
	CorrelationID string
}

// Result is the outcome of an acquire attempt.
type Result struct {
	Outcome Outcome
	// Lock is the stack's lock after the attempt: the caller's when held or granted,
	// otherwise the holder it waits behind.
	Lock Lock
	// QueuedBehind is set when Outcome is OutcomeQueued.
	QueuedBehind *uuid.UUID
}

// ReleaseResult is the outcome of a release.
type ReleaseResult struct {
	Released Lock
	// AlreadyReleased is set when the lease was already gone because the request reached a
	// terminal status (the backend releases it in the same transaction): the explicit
	// release is then a successful no-op and Released is empty.
	AlreadyReleased bool
	// Promoted is the lease given to the next queued request, if any.
	Promoted *Lock
}

// Service implements the lock use cases.
type Service struct {
	repo      Repository
	clock     platform.Clock
	lease     time.Duration
	onPromote PromotionHook
}

// OnPromote registers the hook run for every promoted request.
func (s *Service) OnPromote(h PromotionHook) *Service {
	s.onPromote = h
	return s
}

// NewService returns a Service. lease <= 0 means DefaultLease.
func NewService(repo Repository, clock platform.Clock, lease time.Duration) *Service {
	if lease <= 0 {
		lease = DefaultLease
	}
	return &Service{repo: repo, clock: clock, lease: lease}
}

// Acquire takes the stack lock for a request, or queues the request behind the holder.
func (s *Service) Acquire(ctx context.Context, cmd AcquireCommand) (Result, error) {
	var res Result
	err := s.repo.InTx(ctx, func(st Store) error {
		var err error
		res, err = s.AcquireIn(ctx, st, cmd)
		return err
	})
	return res, err
}

// AcquireIn is Acquire inside a caller's transaction (request creation takes the lock
// in the same transaction that inserts the request).
func (s *Service) AcquireIn(ctx context.Context, st Store, cmd AcquireCommand) (Result, error) {
	teamID, err := st.LockStack(ctx, cmd.StackID)
	if err != nil {
		return Result{}, err
	}
	req, err := st.Request(ctx, cmd.RequestID)
	if err != nil {
		return Result{}, err
	}
	switch {
	case req.StackID != cmd.StackID:
		return Result{}, apperr.Invalid("validation_failed", fmt.Sprintf("request %s does not target stack %s", req.ShortID, cmd.StackID),
			apperr.FieldError{Pointer: "/request_id", Code: "stack_mismatch", Message: "request belongs to another stack"})
	case req.Completed:
		return Result{}, apperr.Conflict("request_completed", fmt.Sprintf("request %s is complete and cannot take the lock", req.ShortID))
	}
	current, err := st.Current(ctx, cmd.StackID)
	if err != nil {
		return Result{}, fmt.Errorf("read lock: %w", err)
	}
	waiters, err := st.Waiters(ctx, cmd.StackID)
	if err != nil {
		return Result{}, fmt.Errorf("read queue: %w", err)
	}
	now := s.clock.Now()
	d := Decide(now, current, cmd.RequestID, waiters)
	a := auditor{st: st, by: cmd.By, correlationID: cmd.CorrelationID, stackID: cmd.StackID, teamID: teamID, at: now}

	switch d.Outcome {
	case OutcomeHeld:
		l, err := st.Extend(ctx, cmd.StackID, LeaseUntil(now, s.lease))
		if err != nil {
			return Result{}, fmt.Errorf("renew lease: %w", err)
		}
		return Result{Outcome: OutcomeHeld, Lock: l}, nil
	case OutcomeGranted:
		l, err := s.grant(ctx, st, a, req, d.Replaced, false)
		if err != nil {
			return Result{}, err
		}
		return Result{Outcome: OutcomeGranted, Lock: l}, nil
	}

	holder := current
	if d.Grantee != uuid.Nil { // the lock was free: the head of the queue gets it first
		head, err := st.Request(ctx, d.Grantee)
		if err != nil {
			return Result{}, err
		}
		l, err := s.grant(ctx, st, a, head, d.Replaced, true)
		if err != nil {
			return Result{}, err
		}
		holder = &l
	}
	// A waiter polling again behind the same holder changes nothing: no write, no audit.
	if !AlreadyQueuedBehind(req.QueuedBehind, d.Behind) {
		if err := st.Queue(ctx, cmd.RequestID, d.Behind); err != nil {
			return Result{}, fmt.Errorf("queue request: %w", err)
		}
		if err := a.append(ctx, "lock.queued", map[string]any{"request_id": req.ID, "short_id": req.ShortID,
			"queued_behind": d.Behind}); err != nil {
			return Result{}, err
		}
	}
	behind := d.Behind
	return Result{Outcome: OutcomeQueued, Lock: *holder, QueuedBehind: &behind}, nil
}

// grant gives req a new lease, points the rest of the queue at it and audits it.
func (s *Service) grant(ctx context.Context, st Store, a auditor, req RequestInfo, replaced *Lock, promoted bool) (Lock, error) {
	l, err := st.Grant(ctx, Grant{StackID: a.stackID, RequestID: req.ID, Holder: HolderFor(req.ShortID),
		AcquiredAt: a.at, ExpiresAt: LeaseUntil(a.at, s.lease)})
	if err != nil {
		return Lock{}, fmt.Errorf("grant lock: %w", err)
	}
	if err := st.Requeue(ctx, a.stackID, req.ID); err != nil {
		return Lock{}, fmt.Errorf("requeue waiters: %w", err)
	}
	payload := map[string]any{"request_id": req.ID, "short_id": req.ShortID, "fencing_token": l.FencingToken,
		"expires_at": l.ExpiresAt, "promoted": promoted}
	if replaced != nil {
		payload["replaced_request_id"], payload["replaced_fencing_token"] = replaced.RequestID, replaced.FencingToken
	}
	if err := a.append(ctx, "lock.acquired", payload); err != nil {
		return Lock{}, err
	}
	if promoted && s.onPromote != nil {
		if err := s.onPromote(ctx, st, req, l, a.by, a.correlationID); err != nil {
			return Lock{}, fmt.Errorf("promotion hook for %s: %w", req.ShortID, err)
		}
	}
	return l, nil
}

// Heartbeat renews the lease of the current holder. The token must be current.
func (s *Service) Heartbeat(ctx context.Context, cmd TokenCommand) (Lock, error) {
	var out Lock
	err := s.repo.InTx(ctx, func(st Store) error {
		current, _, err := s.holderCheck(ctx, st, cmd)
		if err != nil {
			return err
		}
		out, err = st.Extend(ctx, current.StackID, LeaseUntil(s.clock.Now(), s.lease))
		if err != nil {
			return fmt.Errorf("renew lease: %w", err)
		}
		return nil
	})
	return out, err
}

// Release frees the lock held by cmd.RequestID with its current token and promotes the
// oldest queued request, in one transaction.
//
// Release is idempotent for a request that is already terminal: when the lease is gone
// because the request ended (apply reports release it with the final transition) and the
// caller presents the token the request was granted, the answer is success with
// AlreadyReleased set, not stale_fencing_token. That keeps the always() release step of
// apply.yml green after the API released the lease itself.
func (s *Service) Release(ctx context.Context, cmd TokenCommand) (ReleaseResult, error) {
	var res ReleaseResult
	err := s.repo.InTx(ctx, func(st Store) error {
		var err error
		res, err = s.ReleaseIn(ctx, st, cmd)
		if !apperr.IsKind(err, apperr.KindConflict) {
			return err
		}
		// ReleaseIn failed before writing anything and holds the stacks row, so the lease and
		// the request can be read consistently.
		current, cerr := st.Current(ctx, cmd.StackID)
		if cerr != nil {
			return fmt.Errorf("read lock: %w", cerr)
		}
		req, rerr := st.Request(ctx, cmd.RequestID)
		if rerr != nil {
			return err // the original conflict is the better answer
		}
		if AlreadyReleased(req, current, cmd.StackID, cmd.FencingToken) {
			res = ReleaseResult{AlreadyReleased: true}
			return nil
		}
		return err
	})
	return res, err
}

// AlreadyReleased reports whether an explicit release of token for req finds nothing left
// to do: req is terminal, belongs to the stack, was granted exactly this token, and no
// longer holds the lease (current is nil or another request's). Pure.
func AlreadyReleased(req RequestInfo, current *Lock, stackID uuid.UUID, token int64) bool {
	return req.Completed && req.StackID == stackID && req.FencingToken != nil && *req.FencingToken == token &&
		(current == nil || !current.HeldBy(req.ID))
}

// ReleaseIn is Release inside a caller's transaction (a request reaching a terminal status
// releases its lock in the same transaction as the transition). The caller must not have
// locked rows of requests on the stack before this call: it takes the stacks row first.
func (s *Service) ReleaseIn(ctx context.Context, st Store, cmd TokenCommand) (ReleaseResult, error) {
	var res ReleaseResult
	current, teamID, err := s.holderCheck(ctx, st, cmd)
	if err != nil {
		return res, err
	}
	if err := st.Delete(ctx, cmd.StackID); err != nil {
		return res, fmt.Errorf("delete lock: %w", err)
	}
	now := s.clock.Now()
	a := auditor{st: st, by: cmd.By, correlationID: cmd.CorrelationID, stackID: cmd.StackID, teamID: teamID, at: now}
	if err := a.append(ctx, "lock.released", map[string]any{"request_id": current.RequestID,
		"fencing_token": current.FencingToken}); err != nil {
		return res, err
	}
	res.Released = *current

	waiters, err := st.Waiters(ctx, cmd.StackID)
	if err != nil {
		return res, fmt.Errorf("read queue: %w", err)
	}
	next, ok := NextInQueue(waiters, cmd.RequestID)
	if !ok {
		return res, nil
	}
	req, err := st.Request(ctx, next.RequestID)
	if err != nil {
		return res, err
	}
	l, err := s.grant(ctx, st, a, req, nil, true)
	if err != nil {
		return res, err
	}
	res.Promoted = &l
	return res, nil
}

func (s *Service) holderCheck(ctx context.Context, st Store, cmd TokenCommand) (*Lock, uuid.UUID, error) {
	teamID, err := st.LockStack(ctx, cmd.StackID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	current, err := st.Current(ctx, cmd.StackID)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("read lock: %w", err)
	}
	if err := CheckToken(current, cmd.RequestID, cmd.FencingToken); err != nil {
		return nil, uuid.Nil, err
	}
	return current, teamID, nil
}

// Get returns the lock of a stack; NotFound when the stack is not locked.
func (s *Service) Get(ctx context.Context, stackID uuid.UUID) (Lock, error) {
	l, err := s.repo.Get(ctx, stackID)
	if err != nil {
		return Lock{}, fmt.Errorf("get lock: %w", err)
	}
	if l == nil {
		return Lock{}, apperr.NotFound("stack %s is not locked", stackID)
	}
	return *l, nil
}

// auditor writes lock audit events for one stack within a transaction.
type auditor struct {
	st            Store
	by            identity.Principal
	correlationID string
	stackID       uuid.UUID
	teamID        uuid.UUID
	at            time.Time
}

func (a auditor) append(ctx context.Context, action string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode audit payload: %w", err)
	}
	actorType := string(a.by.Type)
	if actorType == "" {
		actorType = audit.ActorSystem
	}
	actorID := a.by.AuditActorID()
	if actorID == "" {
		actorID = "iacp"
	}
	teamID := a.teamID
	if err := a.st.AppendAudit(ctx, audit.Event{OccurredAt: a.at, ActorType: actorType, ActorID: actorID, Action: action,
		SubjectType: "stack", SubjectID: a.stackID.String(), TeamID: &teamID, CorrelationID: a.correlationID,
		Payload: body}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}
