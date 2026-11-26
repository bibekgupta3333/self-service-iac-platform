package locks

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

func TestAlreadyReleased(t *testing.T) {
	req, other := uuid.New(), uuid.New()
	tok, otherTok := int64(7), int64(9)
	done := RequestInfo{ID: req, StackID: stackA, Completed: true, FencingToken: &tok}
	held := &Lock{StackID: stackA, RequestID: req, FencingToken: tok}
	next := &Lock{StackID: stackA, RequestID: other, FencingToken: otherTok}
	tests := []struct {
		name    string
		req     RequestInfo
		current *Lock
		stack   uuid.UUID
		token   int64
		want    bool
	}{
		{"terminal, lease gone", done, nil, stackA, tok, true},
		{"terminal, the next request holds the stack", done, next, stackA, tok, true},
		{"terminal but still holding the lease: the normal release applies", done, held, stackA, tok, false},
		{"not terminal", RequestInfo{ID: req, StackID: stackA, FencingToken: &tok}, nil, stackA, tok, false},
		{"another token", done, nil, stackA, tok + 1, false},
		{"never granted a lease", RequestInfo{ID: req, StackID: stackA, Completed: true}, nil, stackA, tok, false},
		{"another stack", done, nil, uuid.New(), tok, false},
	}
	for _, tt := range tests {
		if got := AlreadyReleased(tt.req, tt.current, tt.stack, tt.token); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Releasing again after the request ended (the API released the lease with its final
// transition) is a successful no-op for the request's own token, and nothing else.
func TestReleaseIsIdempotentForATerminalRequest(t *testing.T) {
	repo := newMemRepo()
	clock := &stepClock{t: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)}
	svc := NewService(repo, clock, time.Hour)
	repo.addRequest(reqA, "req-aaaaaaaaaa", clock.t)
	repo.addRequest(reqB, "req-bbbbbbbbbb", clock.t.Add(time.Second))
	granted := acquire(t, svc, reqA)
	token := granted.Lock.FencingToken
	ctx := context.Background()

	if _, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: token, By: bot}); err != nil {
		t.Fatal(err)
	}
	// Still a live request: a second release is a real stale one.
	if _, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: token}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("release of a live request whose lease is gone: %v", err)
	}
	// The request ends, as the lifecycle does after releasing its lease.
	r := repo.s.requests[reqA]
	r.info.Completed = true
	repo.s.requests[reqA] = r

	before := len(repo.s.events)
	res, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: token})
	if err != nil || !res.AlreadyReleased || res.Promoted != nil {
		t.Fatalf("repeat release of a terminal request: %+v, %v", res, err)
	}
	if len(repo.s.events) != before {
		t.Fatal("a no-op release must not write audit events")
	}
	// The token must be the request's own, and a request that never held the lease gets nothing.
	if _, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: token + 1}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("foreign token: %v", err)
	}
	if _, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqB, FencingToken: token}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("a request that never held the lease: %v", err)
	}
}

// A failure after ReleaseIn started writing (the promotion hook) must roll everything back,
// even for a terminal request that still holds its own lease: the idempotent fallback is
// decided before any write and never papers over partial work.
func TestReleaseDoesNotFallBackAfterPartialWork(t *testing.T) {
	repo := newMemRepo()
	clock := &stepClock{t: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)}
	boom := apperr.Conflict("stale_fencing_token", "hook conflict")
	svc := NewService(repo, clock, time.Hour).OnPromote(func(context.Context, Store, RequestInfo, Lock, identity.Principal, string) error {
		return boom
	})
	repo.addRequest(reqA, "req-aaaaaaaaaa", clock.t)
	repo.addRequest(reqB, "req-bbbbbbbbbb", clock.t.Add(time.Second))
	token := acquire(t, svc, reqA).Lock.FencingToken
	if res := acquire(t, svc, reqB); res.Outcome != OutcomeQueued {
		t.Fatalf("b must queue: %+v", res)
	}
	r := repo.s.requests[reqA]
	r.info.Completed = true // terminal but still holding the lease
	repo.s.requests[reqA] = r

	res, err := svc.Release(context.Background(), TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: token})
	if err == nil || res.AlreadyReleased {
		t.Fatalf("a failing promotion must fail the release: %+v %v", res, err)
	}
	if l, ok := repo.s.locks[stackA]; !ok || l.RequestID != reqA {
		t.Fatal("partial work was committed: the lease moved")
	}
}
