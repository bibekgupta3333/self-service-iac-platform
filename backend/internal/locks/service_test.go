package locks

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// memRepo is an in-memory Repository. InTx works on a copy and commits it on success.
type memRepo struct{ s memState }

type memRequest struct {
	info         RequestInfo
	createdAt    time.Time
	queuedBehind *uuid.UUID
	token        *int64
}

type memState struct {
	stacks   map[uuid.UUID]uuid.UUID // stack -> team
	requests map[uuid.UUID]memRequest
	locks    map[uuid.UUID]Lock
	seq      int64
	events   []audit.Event
}

func (s memState) clone() memState {
	return memState{stacks: maps.Clone(s.stacks), requests: maps.Clone(s.requests), locks: maps.Clone(s.locks),
		seq: s.seq, events: slices.Clone(s.events)}
}

func newMemRepo() *memRepo {
	return &memRepo{s: memState{stacks: map[uuid.UUID]uuid.UUID{stackA: teamA}, requests: map[uuid.UUID]memRequest{},
		locks: map[uuid.UUID]Lock{}, seq: 100}}
}

var teamA = uuid.MustParse("eeeeeeee-0000-4000-8000-00000000000e")

func (m *memRepo) addRequest(id uuid.UUID, short string, at time.Time) {
	m.s.requests[id] = memRequest{info: RequestInfo{ID: id, ShortID: short, StackID: stackA}, createdAt: at}
}

func (m *memRepo) InTx(_ context.Context, fn func(Store) error) error {
	st := &memStore{s: m.s.clone()}
	if err := fn(st); err != nil {
		return err
	}
	m.s = st.s
	return nil
}

func (m *memRepo) Get(_ context.Context, stackID uuid.UUID) (*Lock, error) {
	if l, ok := m.s.locks[stackID]; ok {
		return &l, nil
	}
	return nil, nil
}

type memStore struct{ s memState }

func (m *memStore) LockStack(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	team, ok := m.s.stacks[id]
	if !ok {
		return uuid.Nil, apperr.NotFound("stack %s not found", id)
	}
	return team, nil
}

func (m *memStore) Request(_ context.Context, id uuid.UUID) (RequestInfo, error) {
	r, ok := m.s.requests[id]
	if !ok {
		return RequestInfo{}, apperr.NotFound("request %s not found", id)
	}
	return r.info, nil
}

func (m *memStore) Current(_ context.Context, id uuid.UUID) (*Lock, error) {
	if l, ok := m.s.locks[id]; ok {
		return &l, nil
	}
	return nil, nil
}

func (m *memStore) Waiters(_ context.Context, id uuid.UUID) ([]Waiter, error) {
	var out []Waiter
	for _, r := range m.s.requests {
		if r.info.StackID == id && r.queuedBehind != nil && !r.info.Completed {
			out = append(out, Waiter{RequestID: r.info.ID, CreatedAt: r.createdAt})
		}
	}
	return out, nil
}

func (m *memStore) Grant(_ context.Context, g Grant) (Lock, error) {
	m.s.seq++
	l := Lock{StackID: g.StackID, RequestID: g.RequestID, Holder: g.Holder, FencingToken: m.s.seq, AcquiredAt: g.AcquiredAt, ExpiresAt: g.ExpiresAt}
	m.s.locks[g.StackID] = l
	r := m.s.requests[g.RequestID]
	tok := l.FencingToken
	r.token, r.queuedBehind = &tok, nil
	m.s.requests[g.RequestID] = r
	return l, nil
}

func (m *memStore) Extend(_ context.Context, id uuid.UUID, exp time.Time) (Lock, error) {
	l := m.s.locks[id]
	l.ExpiresAt = exp
	m.s.locks[id] = l
	return l, nil
}

func (m *memStore) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.s.locks, id)
	return nil
}

func (m *memStore) Queue(_ context.Context, request, behind uuid.UUID) error {
	r := m.s.requests[request]
	r.queuedBehind = &behind
	m.s.requests[request] = r
	return nil
}

func (m *memStore) Requeue(_ context.Context, stackID, behind uuid.UUID) error {
	for id, r := range m.s.requests {
		if r.info.StackID == stackID && id != behind && r.queuedBehind != nil && !r.info.Completed {
			b := behind
			r.queuedBehind = &b
			m.s.requests[id] = r
		}
	}
	return nil
}

func (m *memStore) AppendAudit(_ context.Context, e audit.Event) error {
	m.s.events = append(m.s.events, e)
	return nil
}

type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

var bot = identity.Principal{Login: "apply.yml", Type: identity.ActorGitHubActions}

func acquire(t *testing.T, svc *Service, req uuid.UUID) Result {
	t.Helper()
	res, err := svc.Acquire(context.Background(), AcquireCommand{StackID: stackA, RequestID: req, By: bot, CorrelationID: "c1"})
	if err != nil {
		t.Fatalf("acquire %s: %v", req, err)
	}
	return res
}

func actions(events []audit.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Action
	}
	return out
}

func TestAcquireQueueReleasePromote(t *testing.T) {
	repo := newMemRepo()
	repo.addRequest(reqA, "req-a", t0)
	repo.addRequest(reqB, "req-b", t0.Add(time.Minute))
	repo.addRequest(reqC, "req-c", t0.Add(2*time.Minute))
	clock := &stepClock{t: t0}
	svc := NewService(repo, clock, time.Hour)
	ctx := context.Background()

	a := acquire(t, svc, reqA)
	if a.Outcome != OutcomeGranted || a.Lock.RequestID != reqA || a.Lock.Holder != "request:req-a" ||
		a.Lock.FencingToken != 101 || !a.Lock.ExpiresAt.Equal(t0.Add(time.Hour)) || *repo.s.requests[reqA].token != 101 {
		t.Fatalf("first acquire = %+v", a)
	}
	// Re-acquiring renews and keeps the token.
	clock.t = t0.Add(10 * time.Minute)
	if again := acquire(t, svc, reqA); again.Outcome != OutcomeHeld || again.Lock.FencingToken != 101 ||
		!again.Lock.ExpiresAt.Equal(clock.t.Add(time.Hour)) {
		t.Fatalf("re-acquire = %+v", again)
	}

	for _, id := range []uuid.UUID{reqC, reqB} {
		res := acquire(t, svc, id)
		if res.Outcome != OutcomeQueued || *res.QueuedBehind != reqA || res.Lock.RequestID != reqA {
			t.Fatalf("acquire %s = %+v", id, res)
		}
	}

	// A heartbeat with a stale token or from a waiter is rejected.
	for _, cmd := range []TokenCommand{{StackID: stackA, RequestID: reqA, FencingToken: 100}, {StackID: stackA, RequestID: reqB, FencingToken: 101}} {
		if _, err := svc.Heartbeat(ctx, cmd); !hasCode(err, "stale_fencing_token") {
			t.Fatalf("heartbeat %+v: want stale_fencing_token, got %v", cmd, err)
		}
	}
	clock.t = t0.Add(30 * time.Minute)
	hb, err := svc.Heartbeat(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: 101})
	if err != nil || !hb.ExpiresAt.Equal(clock.t.Add(time.Hour)) {
		t.Fatalf("heartbeat = %+v, %v", hb, err)
	}

	// Releasing promotes the oldest waiter (B, created before C) with a higher token.
	if _, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: 99}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("release with stale token: %v", err)
	}
	rel, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: 101, By: bot})
	if err != nil || rel.Released.FencingToken != 101 || rel.Promoted == nil || rel.Promoted.RequestID != reqB || rel.Promoted.FencingToken != 102 {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	if repo.s.requests[reqB].queuedBehind != nil || *repo.s.requests[reqC].queuedBehind != reqB {
		t.Fatalf("queue after promotion: B behind %v, C behind %v", repo.s.requests[reqB].queuedBehind, repo.s.requests[reqC].queuedBehind)
	}
	// A second release of the old lease is stale.
	if _, err := svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: 101}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("double release: %v", err)
	}

	rel, err = svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqB, FencingToken: 102})
	if err != nil || rel.Promoted == nil || rel.Promoted.RequestID != reqC || rel.Promoted.FencingToken != 103 {
		t.Fatalf("second release = %+v, %v", rel, err)
	}
	rel, err = svc.Release(ctx, TokenCommand{StackID: stackA, RequestID: reqC, FencingToken: 103})
	if err != nil || rel.Promoted != nil {
		t.Fatalf("last release = %+v, %v", rel, err)
	}
	if _, err := svc.Get(ctx, stackA); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("get unlocked stack: %v", err)
	}

	want := []string{"lock.acquired", "lock.queued", "lock.queued", "lock.released", "lock.acquired",
		"lock.released", "lock.acquired", "lock.released"}
	if got := actions(repo.s.events); !slices.Equal(got, want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	if e := repo.s.events[0]; e.SubjectType != "stack" || e.SubjectID != stackA.String() || *e.TeamID != teamA ||
		e.ActorType != "github_actions" || e.ActorID != "apply.yml" || e.CorrelationID != "c1" {
		t.Fatalf("audit event = %+v", e)
	}
}

func TestAcquireExpiredLease(t *testing.T) {
	repo := newMemRepo()
	repo.addRequest(reqA, "req-a", t0)
	repo.addRequest(reqB, "req-b", t0.Add(time.Minute))
	repo.addRequest(reqC, "req-c", t0.Add(2*time.Minute))
	clock := &stepClock{t: t0}
	svc := NewService(repo, clock, time.Hour)

	acquire(t, svc, reqA)
	acquire(t, svc, reqB) // queued behind A

	// A's lease runs out. A newcomer (C) cannot jump the queue: B gets the lock and C waits behind B.
	clock.t = t0.Add(2 * time.Hour)
	res := acquire(t, svc, reqC)
	if res.Outcome != OutcomeQueued || *res.QueuedBehind != reqB || res.Lock.RequestID != reqB || res.Lock.FencingToken != 102 {
		t.Fatalf("acquire after expiry = %+v", res)
	}
	// A's late release is stale; its token no longer counts.
	if _, err := svc.Release(context.Background(), TokenCommand{StackID: stackA, RequestID: reqA, FencingToken: 101}); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("release by expired holder: %v", err)
	}
	replaced := slices.ContainsFunc(repo.s.events, func(e audit.Event) bool {
		return e.Action == "lock.acquired" && strings.Contains(string(e.Payload), `"replaced_fencing_token":101`)
	})
	if !replaced {
		t.Fatalf("the takeover must be audited with the replaced token: %v", repo.s.events)
	}
}

func TestAcquireErrors(t *testing.T) {
	repo := newMemRepo()
	repo.addRequest(reqA, "req-a", t0)
	repo.s.requests[reqB] = memRequest{info: RequestInfo{ID: reqB, ShortID: "req-b", StackID: stackA, Completed: true}}
	other := uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000002")
	repo.s.requests[reqC] = memRequest{info: RequestInfo{ID: reqC, ShortID: "req-c", StackID: other}}
	svc := NewService(repo, &stepClock{t: t0}, 0)
	if svc.lease != DefaultLease {
		t.Fatal("lease <= 0 must mean DefaultLease")
	}
	ctx := context.Background()
	tests := []struct {
		name  string
		cmd   AcquireCommand
		check func(error) bool
	}{
		{"unknown stack", AcquireCommand{StackID: other, RequestID: reqA}, func(err error) bool { return apperr.IsKind(err, apperr.KindNotFound) }},
		{"unknown request", AcquireCommand{StackID: stackA, RequestID: reqD}, func(err error) bool { return apperr.IsKind(err, apperr.KindNotFound) }},
		{"completed request", AcquireCommand{StackID: stackA, RequestID: reqB}, func(err error) bool { return hasCode(err, "request_completed") }},
		{"request of another stack", AcquireCommand{StackID: stackA, RequestID: reqC}, func(err error) bool { return hasCode(err, "validation_failed") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Acquire(ctx, tt.cmd); !tt.check(err) {
				t.Fatalf("got %v", err)
			}
			if len(repo.s.locks) != 0 || len(repo.s.events) != 0 {
				t.Fatal("a failed acquire must not persist anything")
			}
		})
	}
}

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}
