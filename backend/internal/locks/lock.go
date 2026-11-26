// Package locks implements the platform per-stack mutation lock (docs/06 §3): one lease
// per stack, a fencing token that only grows, and a FIFO queue of waiting requests.
//
// The decisions (who gets the lock, who waits, whether a token is current) are pure
// functions in this file. Service orchestrates them over a transactional Store, and
// PostgresRepository is the only code that does I/O.
package locks

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

// DefaultLease is how long a lock is held without a heartbeat (docs/06 §3: an abandoned
// PR releases its lock after 7 days).
const DefaultLease = 7 * 24 * time.Hour

// Lock is the current holder of a stack's platform lock (one row in locks).
type Lock struct {
	StackID      uuid.UUID
	RequestID    uuid.UUID
	Holder       string // "request:req-7k3p9x2m4q" or "run:<github run id>"
	FencingToken int64  // from lock_fencing_seq: strictly increasing across grants
	AcquiredAt   time.Time
	ExpiresAt    time.Time
}

// Expired reports whether the lease has run out at now. Pure.
func (l Lock) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }

// HeldBy reports whether request holds the lock. Pure.
func (l Lock) HeldBy(request uuid.UUID) bool { return l.RequestID == request }

// Waiter is a request queued for a stack (requests.queued_behind is set).
type Waiter struct {
	RequestID uuid.UUID
	CreatedAt time.Time
}

// HolderFor is the holder label of a request. Pure.
func HolderFor(shortID string) string { return "request:" + shortID }

// LeaseUntil is the expiry of a lease taken or renewed at now. Pure.
func LeaseUntil(now time.Time, lease time.Duration) time.Time { return now.Add(lease) }

// NextInQueue returns the oldest waiter (by creation time, then id) other than exclude. Pure.
func NextInQueue(waiters []Waiter, exclude uuid.UUID) (Waiter, bool) {
	eligible := slices.DeleteFunc(slices.Clone(waiters), func(w Waiter) bool { return w.RequestID == exclude })
	if len(eligible) == 0 {
		return Waiter{}, false
	}
	return slices.MinFunc(eligible, func(a, b Waiter) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.RequestID.String(), b.RequestID.String())
	}), true
}

// Outcome is the result of an acquire attempt.
type Outcome string

// Acquire outcomes.
const (
	// OutcomeHeld: the caller already holds the lock; the lease is renewed, the token kept.
	OutcomeHeld Outcome = "held"
	// OutcomeGranted: the caller gets the lock with a new fencing token.
	OutcomeGranted Outcome = "granted"
	// OutcomeQueued: the caller waits behind another request.
	OutcomeQueued Outcome = "queued"
)

// Decision is what Decide concluded for one acquire attempt.
type Decision struct {
	Outcome Outcome
	// Grantee receives a new lease: the caller when granted, or the head of the queue
	// when the lock was free but older requests were waiting. uuid.Nil when nobody does.
	Grantee uuid.UUID
	// Behind is the request the caller waits for (OutcomeQueued only).
	Behind uuid.UUID
	// Replaced is the expired lease the grant takes over, if any.
	Replaced *Lock
}

// Decide works out an acquire attempt by caller at now. Pure.
//
//   - The holder re-acquiring renews its lease (even an expired one nobody took over).
//   - A live lease held by someone else queues the caller behind the holder.
//   - A free or expired lock goes to the oldest waiter, so a newcomer cannot jump the
//     queue; the caller gets it only when nobody older is waiting.
func Decide(now time.Time, current *Lock, caller uuid.UUID, waiters []Waiter) Decision {
	if current != nil && current.HeldBy(caller) {
		return Decision{Outcome: OutcomeHeld}
	}
	if current != nil && !current.Expired(now) {
		return Decision{Outcome: OutcomeQueued, Behind: current.RequestID}
	}
	var replaced *Lock
	if current != nil {
		expired := *current
		replaced = &expired
	}
	head, ok := NextInQueue(waiters, uuid.Nil)
	if !ok || head.RequestID == caller {
		return Decision{Outcome: OutcomeGranted, Grantee: caller, Replaced: replaced}
	}
	return Decision{Outcome: OutcomeQueued, Grantee: head.RequestID, Behind: head.RequestID, Replaced: replaced}
}

// CheckToken verifies that request holds the lock with token. It rejects callers whose
// lease was released or taken over, so a late callback from a superseded run cannot act
// on the stack. An expired lease that nobody took over is still current. Pure.
func CheckToken(current *Lock, request uuid.UUID, token int64) error {
	switch {
	case current == nil:
		return staleToken("the stack is not locked")
	case !current.HeldBy(request):
		return staleToken(fmt.Sprintf("the lock is held by another request (token %d)", current.FencingToken))
	case current.FencingToken != token:
		return staleToken(fmt.Sprintf("fencing token %d is not current", token))
	}
	return nil
}

// CheckStackToken is CheckToken without a request: any holder, but the token must be current. Pure.
func CheckStackToken(current *Lock, token int64) error {
	if current == nil {
		return staleToken("the stack is not locked")
	}
	return CheckToken(current, current.RequestID, token)
}

func staleToken(msg string) error {
	return apperr.Conflict("stale_fencing_token", msg)
}
