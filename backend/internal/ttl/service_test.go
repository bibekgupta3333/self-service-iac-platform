package ttl

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// memRepo is an in-memory Repository; a transaction works on the live map and a failed one
// is rolled back by restoring a snapshot.
type memRepo struct {
	stacks      map[uuid.UUID]Stack
	openDestroy map[uuid.UUID]bool
	events      []audit.Event
	jobs        []outbox.Message
	botID       uuid.UUID
	prs         map[uuid.UUID]int
	auditErr    error
	// candidateView, when set for a stack, is what Candidates reports instead of the stored
	// stack: a stale read taken before a concurrent change.
	candidateView map[uuid.UUID]Stack
}

func newMemRepo(stacks ...Stack) *memRepo {
	r := &memRepo{stacks: map[uuid.UUID]Stack{}, openDestroy: map[uuid.UUID]bool{}, prs: map[uuid.UUID]int{},
		botID: uuid.MustParse("99999999-9999-4999-8999-999999999999")}
	for _, s := range stacks {
		r.stacks[s.ID] = s
	}
	return r
}

func (r *memRepo) InTx(_ context.Context, fn func(Store) error) error {
	snap := make(map[uuid.UUID]Stack, len(r.stacks))
	for k, v := range r.stacks {
		snap[k] = v
	}
	nEvents, nJobs := len(r.events), len(r.jobs)
	if err := fn(r); err != nil {
		r.stacks, r.events, r.jobs = snap, r.events[:nEvents], r.jobs[:nJobs]
		return err
	}
	return nil
}

func (r *memRepo) get(id uuid.UUID) (Stack, error) {
	s, ok := r.stacks[id]
	if !ok {
		return Stack{}, apperr.NotFound("stack %s not found", id)
	}
	return s, nil
}

func (r *memRepo) Stack(_ context.Context, id uuid.UUID) (Stack, error)     { return r.get(id) }
func (r *memRepo) LockStack(_ context.Context, id uuid.UUID) (Stack, error) { return r.get(id) }
func (r *memRepo) Candidates(_ context.Context, horizon time.Time, after *Cursor, limit int) ([]Stack, error) {
	var all []Stack
	for _, s := range r.stacks {
		if s.IsEphemeral && s.Environment != ProductionEnvironment && !s.ExpiresAt.After(horizon) && !r.openDestroy[s.ID] {
			if v, ok := r.candidateView[s.ID]; ok {
				s = v
			}
			all = append(all, s)
		}
	}
	slices.SortFunc(all, func(a, b Stack) int {
		if c := a.ExpiresAt.Compare(*b.ExpiresAt); c != 0 {
			return c
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	var out []Stack
	for _, s := range all {
		past := after == nil || s.ExpiresAt.After(after.ExpiresAt) ||
			(s.ExpiresAt.Equal(after.ExpiresAt) && bytes.Compare(s.ID[:], after.ID[:]) > 0)
		if past && len(out) < limit {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *memRepo) Attempts(_ context.Context, id uuid.UUID) ([]Attempt, error) {
	return r.stacks[id].Attempts, nil
}

func (r *memRepo) BlockedSince(_ context.Context, id uuid.UUID, since time.Time) (bool, error) {
	for _, e := range r.events {
		if e.Action == "ttl.blocked" && e.SubjectID == id.String() && !e.OccurredAt.Before(since) {
			return true, nil
		}
	}
	return false, nil
}
func (r *memRepo) SystemUserID(context.Context) (uuid.UUID, error) { return r.botID, nil }
func (r *memRepo) LatestPullRequest(_ context.Context, id uuid.UUID) (int, bool, error) {
	n, ok := r.prs[id]
	return n, ok, nil
}
func (r *memRepo) SetExpiry(_ context.Context, id uuid.UUID, e Extension) error {
	s := r.stacks[id]
	s.ExpiresAt, s.TTLExtendedHours, s.WarnedAt = &e.ExpiresAt, e.ExtendedHours, nil
	r.stacks[id] = s
	return nil
}
func (r *memRepo) MarkWarned(_ context.Context, id uuid.UUID, at time.Time) error {
	s := r.stacks[id]
	s.WarnedAt = &at
	r.stacks[id] = s
	return nil
}
func (r *memRepo) HasOpenDestroy(_ context.Context, id uuid.UUID) (bool, error) {
	return r.openDestroy[id], nil
}
func (r *memRepo) AppendAudit(_ context.Context, e audit.Event) error {
	if r.auditErr != nil && e.Action == "ttl.blocked" {
		return r.auditErr
	}
	r.events = append(r.events, e)
	return nil
}
func (r *memRepo) Enqueue(_ context.Context, m outbox.Message) (bool, error) {
	r.jobs = append(r.jobs, m)
	return true, nil
}

type fakeMembers map[uuid.UUID]rbac.Role

func (f fakeMembers) RoleInTeam(_ context.Context, _ string, u uuid.UUID) (rbac.Role, error) {
	return f[u], nil
}

type fakeDestroyer struct {
	got []DestroyCommand
	res DestroyResult
	err error
}

func (f *fakeDestroyer) OpenTTLDestroy(_ context.Context, c DestroyCommand) (DestroyResult, error) {
	f.got = append(f.got, c)
	return f.res, f.err
}

var (
	devUser    = identity.Principal{UserID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), Login: "dev", Type: identity.ActorUser}
	viewerUser = identity.Principal{UserID: uuid.MustParse("44444444-4444-4444-8444-444444444444"), Login: "viewer", Type: identity.ActorUser}
	strangerID = identity.Principal{UserID: uuid.MustParse("55555555-5555-4555-8555-555555555555"), Login: "stranger", Type: identity.ActorUser}
	members    = fakeMembers{devUser.UserID: rbac.RoleDeveloper, viewerUser.UserID: rbac.RoleViewer}
	policy     = Policy{MinHours: 1, MaxHours: 72, DefaultHours: 24, MaxExtensionHours: 48}
)

func newService(r *memRepo, d Destroyer) *Service {
	return NewService(r, members, d, platform.FixedClock{T: t0}, policy)
}

func TestServiceExtend(t *testing.T) {
	st := stack("development", t0.Add(10*time.Hour))
	st.Team = "payments"
	ctx := context.Background()

	t.Run("a developer extends, audited, and the warning resets", func(t *testing.T) {
		warned := t0.Add(-time.Hour)
		s := st
		s.WarnedAt = &warned
		repo := newMemRepo(s)
		res, err := newService(repo, nil).Extend(ctx, devUser, s.ID, 24, "corr-1")
		if err != nil {
			t.Fatal(err)
		}
		if !res.ExpiresAt.Equal(t0.Add(34*time.Hour)) || res.ExtendedHours != 24 || res.RemainingExtHours != 24 {
			t.Fatalf("result = %+v", res)
		}
		got := repo.stacks[s.ID]
		if !got.ExpiresAt.Equal(res.ExpiresAt) || got.WarnedAt != nil || got.TTLExtendedHours != 24 {
			t.Fatalf("stored = %+v", got)
		}
		if len(repo.events) != 1 || repo.events[0].Action != "ttl.extended" || repo.events[0].ActorID != devUser.UserID.String() ||
			repo.events[0].CorrelationID != "corr-1" || repo.events[0].SubjectID != s.ID.String() ||
			!strings.Contains(string(repo.events[0].Payload), `"hours":24`) {
			t.Fatalf("audit = %+v", repo.events)
		}
	})
	t.Run("the cap holds across calls", func(t *testing.T) {
		repo := newMemRepo(st)
		svc := newService(repo, nil)
		for i := 0; i < 2; i++ {
			if _, err := svc.Extend(ctx, devUser, st.ID, 24, "c"); err != nil {
				t.Fatalf("extension %d: %v", i, err)
			}
		}
		_, err := svc.Extend(ctx, devUser, st.ID, 1, "c")
		if e, ok := apperr.As(err); !ok || e.Code != "ttl_extension_cap" {
			t.Fatalf("third extension = %v", err)
		}
		if len(repo.events) != 2 {
			t.Fatalf("a refused extension must not be audited as extended: %d events", len(repo.events))
		}
	})
	t.Run("roles", func(t *testing.T) {
		repo := newMemRepo(st)
		svc := newService(repo, nil)
		if _, err := svc.Extend(ctx, viewerUser, st.ID, 1, "c"); !apperr.IsKind(err, apperr.KindForbidden) {
			t.Fatalf("viewer = %v, want forbidden", err)
		}
		_, err := svc.Extend(ctx, strangerID, st.ID, 1, "c")
		if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindNotFound || e.Code != "stack_not_found" {
			t.Fatalf("non-member = %v, want the same not-found as a missing stack", err)
		}
		if _, err := svc.Extend(ctx, identity.Principal{Type: identity.ActorSystem}, st.ID, 1, "c"); !apperr.IsKind(err, apperr.KindUnauthenticated) {
			t.Fatalf("non-user = %v", err)
		}
		if _, err := svc.Extend(ctx, devUser, uuid.New(), 1, "c"); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("missing stack = %v", err)
		}
	})
	t.Run("a destroy in progress blocks extension", func(t *testing.T) {
		repo := newMemRepo(st)
		repo.openDestroy[st.ID] = true
		_, err := newService(repo, nil).Extend(ctx, devUser, st.ID, 1, "c")
		if e, ok := apperr.As(err); !ok || e.Code != "destroy_pending" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("production and permanent stacks cannot be extended", func(t *testing.T) {
		prod := stack("production", t0.Add(time.Hour))
		prod.ID = sid(2)
		perm := Stack{ID: sid(3), Team: "payments", Environment: "development", Status: "active"}
		repo := newMemRepo(prod, perm)
		svc := newService(repo, nil)
		for _, id := range []uuid.UUID{prod.ID, perm.ID} {
			if _, err := svc.Extend(ctx, devUser, id, 1, "c"); !apperr.IsKind(err, apperr.KindConflict) && !apperr.IsKind(err, apperr.KindForbidden) {
				t.Fatalf("stack %s: %v", id, err)
			}
		}
	})
}

func TestDueListsWarningsAndTeardowns(t *testing.T) {
	a := stack("development", t0.Add(-time.Hour))
	a.ID = sid(1)
	b := stack("staging", t0.Add(5*time.Hour))
	b.ID = sid(2)
	c := stack("development", t0.Add(200*time.Hour))
	c.ID = sid(3)
	warned := t0
	d := stack("development", t0.Add(2*time.Hour))
	d.ID, d.WarnedAt = sid(4), &warned
	repo := newMemRepo(a, b, c, d)
	got, err := newService(repo, nil).Due(context.Background(), "corr-due")
	if err != nil || len(got) != 2 || got[0].Phase != PhaseTeardown || got[0].Stack.ID != a.ID ||
		got[1].Phase != PhaseWarn || got[1].Stack.ID != b.ID {
		t.Fatalf("Due = %+v, %v", got, err)
	}
}

func TestWarn(t *testing.T) {
	st := stack("development", t0.Add(5*time.Hour))
	ctx := context.Background()

	t.Run("records the audit event, stamps the stack and enqueues the comment once", func(t *testing.T) {
		repo := newMemRepo(st)
		svc := newService(repo, nil).EnableComments("https://portal/stacks")
		res, err := svc.Warn(ctx, st.ID, "c")
		if err != nil || !res.Warned {
			t.Fatalf("Warn = %+v, %v", res, err)
		}
		if repo.stacks[st.ID].WarnedAt == nil || len(repo.events) != 1 || repo.events[0].Action != "ttl.warned" ||
			repo.events[0].ActorID != Actor || repo.events[0].ActorType != audit.ActorSystem ||
			len(repo.jobs) != 1 || repo.jobs[0].Topic != outbox.TopicTTLWarning {
			t.Fatalf("state: stack=%+v events=%+v jobs=%+v", repo.stacks[st.ID], repo.events, repo.jobs)
		}
		again, err := svc.Warn(ctx, st.ID, "c")
		if err != nil || again.Warned || len(repo.events) != 1 || len(repo.jobs) != 1 {
			t.Fatalf("a second warning must be a no-op: %+v, %v, events=%d jobs=%d", again, err, len(repo.events), len(repo.jobs))
		}
	})
	t.Run("audit only without comments", func(t *testing.T) {
		repo := newMemRepo(st)
		if res, err := newService(repo, nil).Warn(ctx, st.ID, "c"); err != nil || !res.Warned || len(repo.jobs) != 0 || len(repo.events) != 1 {
			t.Fatalf("Warn = %+v, %v, jobs=%d", res, err, len(repo.jobs))
		}
	})
	t.Run("skips stacks that are not in the window", func(t *testing.T) {
		far := stack("development", t0.Add(100*time.Hour))
		expired := stack("development", t0.Add(-time.Hour))
		expired.ID = sid(2)
		prod := stack("production", t0.Add(time.Hour))
		prod.ID = sid(3)
		repo := newMemRepo(far, expired, prod)
		for _, id := range []uuid.UUID{far.ID, expired.ID, prod.ID} {
			res, err := newService(repo, nil).Warn(ctx, id, "c")
			if err != nil || res.Warned || res.Reason == "" {
				t.Fatalf("stack %s: %+v, %v", id, res, err)
			}
		}
		if len(repo.events) != 0 {
			t.Fatalf("events = %+v", repo.events)
		}
	})
	t.Run("missing stack", func(t *testing.T) {
		if _, err := newService(newMemRepo(), nil).Warn(ctx, uuid.New(), "c"); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTeardownActsAsTheSystemActor(t *testing.T) {
	repo := newMemRepo()
	d := &fakeDestroyer{res: DestroyResult{Created: true, ShortID: "req-1"}}
	id := uuid.New()
	res, err := newService(repo, d).Teardown(context.Background(), id, "corr")
	if err != nil || !res.Created || len(d.got) != 1 {
		t.Fatalf("Teardown = %+v, %v", res, err)
	}
	c := d.got[0]
	if c.StackID != id || c.By.UserID != repo.botID || c.By.Type != identity.ActorSystem || c.By.Login != Actor ||
		c.By.PlatformAdmin || c.CorrelationID != "corr" {
		t.Fatalf("command = %+v", c)
	}
	d.err = apperr.Conflict("ttl_not_due", "not expired")
	if _, err := newService(repo, d).Teardown(context.Background(), id, "c"); !apperr.IsKind(err, apperr.KindConflict) {
		t.Fatalf("destroyer errors pass through: %v", err)
	}
	d.err, d.res = nil, DestroyResult{Blocked: true}
	_, err = newService(repo, d).Teardown(context.Background(), id, "c")
	if e, ok := apperr.As(err); !ok || e.Code != "ttl_blocked" || e.Kind != apperr.KindConflict {
		t.Fatalf("a blocked stack = %v, want conflict ttl_blocked", err)
	}
}

type fakeCommenter struct {
	number int
	body   string
	err    error
}

func (f *fakeCommenter) CommentOnPullRequest(_ context.Context, n int, body string) error {
	f.number, f.body = n, body
	return f.err
}

func TestWarningNotifier(t *testing.T) {
	warned := t0
	st := stack("development", t0.Add(5*time.Hour))
	st.WarnedAt = &warned
	job := func(id uuid.UUID) outbox.Job {
		return outbox.Job{ID: 1, Topic: outbox.TopicTTLWarning, Payload: []byte(`{"stack_id":"` + id.String() + `"}`)}
	}
	ctx := context.Background()

	t.Run("comments on the stack's newest pull request", func(t *testing.T) {
		repo := newMemRepo(st)
		repo.prs[st.ID] = 42
		c := &fakeCommenter{}
		n := NewWarningNotifier(repo, c, platform.FixedClock{T: t0}, "https://portal/stacks")
		if err := n.Handle(ctx, job(st.ID)); err != nil {
			t.Fatal(err)
		}
		if c.number != 42 || !strings.Contains(c.body, "https://portal/stacks/"+st.ID.String()) || !strings.Contains(c.body, st.Path) {
			t.Fatalf("comment #%d: %s", c.number, c.body)
		}
	})
	t.Run("no pull request, extended since, or gone: skipped without error", func(t *testing.T) {
		unwarned := st
		unwarned.ID, unwarned.WarnedAt = sid(2), nil
		repo := newMemRepo(st, unwarned)
		repo.prs[unwarned.ID] = 7
		c := &fakeCommenter{}
		n := NewWarningNotifier(repo, c, platform.FixedClock{T: t0}, "")
		for _, id := range []uuid.UUID{st.ID, unwarned.ID} {
			if err := n.Handle(ctx, job(id)); err != nil || c.number != 0 {
				t.Fatalf("stack %s: err=%v commented=%d", id, err, c.number)
			}
		}
	})
	t.Run("permanent failures", func(t *testing.T) {
		n := NewWarningNotifier(newMemRepo(), &fakeCommenter{}, platform.FixedClock{T: t0}, "")
		for _, j := range []outbox.Job{job(uuid.New()), {ID: 2, Payload: []byte(`{`)}} {
			if err := n.Handle(ctx, j); !errors.Is(err, outbox.ErrPermanent) {
				t.Fatalf("err = %v, want permanent", err)
			}
		}
	})
	t.Run("a failed comment is retried", func(t *testing.T) {
		repo := newMemRepo(st)
		repo.prs[st.ID] = 1
		n := NewWarningNotifier(repo, &fakeCommenter{err: errors.New("github down")}, platform.FixedClock{T: t0}, "")
		if err := n.Handle(ctx, job(st.ID)); err == nil || errors.Is(err, outbox.ErrPermanent) {
			t.Fatalf("err = %v, want retryable", err)
		}
	})
}
