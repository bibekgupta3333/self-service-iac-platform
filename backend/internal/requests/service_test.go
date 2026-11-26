package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// memRepo is an in-memory Repository. InTx works on a copy and commits it only on
// success, so rollback semantics match PostgreSQL.
type memRepo struct{ state memState }

type memState struct {
	keys     map[string]memKey
	stacks   []NewStack
	requests []*Request
	events   []audit.Event
}

type memKey struct {
	rec IdempotencyRecord
	at  time.Time
}

// stepClock is a Clock tests can move forward.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

func TestIdempotencyKeyExpiresAfterWindow(t *testing.T) {
	repo := newMemRepo()
	clock := &stepClock{t: t0}
	svc := NewService(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleDeveloper}, &fakeLocker{}, clock, &seqIDs{})
	ctx := context.Background()

	first, err := svc.Create(ctx, dev, createCmd(validInput(), "key-expiry-01"))
	if err != nil {
		t.Fatal(err)
	}
	other := validInput()
	other.Stack = "order-api"

	clock.t = t0.Add(IdempotencyWindow - time.Minute)
	if _, err := svc.Create(ctx, dev, createCmd(other, "key-expiry-01")); !hasCode(err, "idempotency_key_reuse") {
		t.Fatalf("inside the window a different body must be rejected, got %v", err)
	}

	clock.t = t0.Add(IdempotencyWindow + time.Minute)
	res, err := svc.Create(ctx, dev, createCmd(other, "key-expiry-01"))
	if err != nil || res.Replayed || res.Request.ID == first.Request.ID {
		t.Fatalf("after the window the key must be reusable: %+v, %v", res, err)
	}
}

func TestNewIdempotencyClaim(t *testing.T) {
	c := NewIdempotencyClaim(dev.UserID, "k", "h", t0)
	if !c.At.Equal(t0) || !c.ExpiredBefore.Equal(t0.Add(-24*time.Hour)) {
		t.Fatalf("claim = %+v", c)
	}
}

func newMemRepo() *memRepo {
	return &memRepo{state: memState{keys: map[string]memKey{}}}
}

func (s memState) clone() memState {
	keys := make(map[string]memKey, len(s.keys))
	for k, v := range s.keys {
		keys[k] = v
	}
	return memState{keys: keys, stacks: slices.Clone(s.stacks), requests: slices.Clone(s.requests), events: slices.Clone(s.events)}
}

func (m *memRepo) InTx(_ context.Context, fn func(Store) error) error {
	st := &memStore{s: m.state.clone()}
	if err := fn(st); err != nil {
		return err
	}
	m.state = st.s
	return nil
}

func (m *memRepo) Get(_ context.Context, id uuid.UUID) (*Request, error) {
	for _, r := range m.state.requests {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, apperr.NotFound("request %s not found", id)
}

func (m *memRepo) List(_ context.Context, f ListFilter) ([]*Request, error) {
	rows := slices.Clone(m.state.requests)
	slices.SortFunc(rows, func(a, b *Request) int { return b.CreatedAt.Compare(a.CreatedAt) })
	var out []*Request
	for _, r := range rows {
		if f.After != nil && !r.CreatedAt.Before(f.After.CreatedAt) {
			continue
		}
		if (f.Status == "" || r.Status == f.Status) && (f.Kind == "" || r.Kind == f.Kind) && len(out) < f.Limit {
			out = append(out, r)
		}
	}
	return out, nil
}

type memStore struct{ s memState }

func key(u uuid.UUID, k string) string { return u.String() + "/" + k }

func (m *memStore) ClaimIdempotencyKey(_ context.Context, c IdempotencyClaim) (bool, error) {
	if cur, ok := m.s.keys[key(c.UserID, c.Key)]; ok {
		if !cur.at.Before(c.ExpiredBefore) {
			return false, nil
		}
	}
	m.s.keys[key(c.UserID, c.Key)] = memKey{rec: IdempotencyRecord{RequestHash: c.RequestHash}, at: c.At}
	return true, nil
}

func (m *memStore) GetIdempotencyKey(_ context.Context, u uuid.UUID, k string) (IdempotencyRecord, error) {
	return m.s.keys[key(u, k)].rec, nil
}

func (m *memStore) CompleteIdempotencyKey(_ context.Context, u uuid.UUID, k string, id uuid.UUID) error {
	cur := m.s.keys[key(u, k)]
	cur.rec.RequestID = id
	m.s.keys[key(u, k)] = cur
	return nil
}

var (
	teamID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	envID  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

func (m *memStore) TeamBySlug(_ context.Context, slug string) (Team, error) {
	if slug != "payments" {
		return Team{}, apperr.NotFound("team %q not found", slug)
	}
	return Team{ID: teamID, Slug: slug}, nil
}

func (m *memStore) EnvironmentByName(_ context.Context, name string) (Environment, error) {
	if name != "development" {
		return Environment{}, apperr.NotFound("environment %q not found", name)
	}
	ttl := 168
	return Environment{ID: envID, Name: name, DefaultRegion: "us-east-1", AllowedRegions: []string{"us-east-1"},
		AllowEphemeral: true, MaxTTLHours: &ttl}, nil
}

func (m *memStore) StackExists(_ context.Context, e, t uuid.UUID, name string) (bool, error) {
	return slices.ContainsFunc(m.s.stacks, func(s NewStack) bool {
		return s.EnvironmentID == e && s.TeamID == t && s.Name == name
	}), nil
}

func (m *memStore) InsertStack(_ context.Context, s NewStack) error {
	m.s.stacks = append(m.s.stacks, s)
	return nil
}

func (m *memStore) InsertRequest(_ context.Context, r *Request) error {
	m.s.requests = append(m.s.requests, r)
	return nil
}

func (m *memStore) GetRequest(_ context.Context, id uuid.UUID) (*Request, error) {
	for _, r := range m.s.requests {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, apperr.NotFound("request %s not found", id)
}

func (m *memStore) AppendAudit(_ context.Context, e audit.Event) error {
	m.s.events = append(m.s.events, e)
	return nil
}

func (m *memStore) Locks() locks.Store { return nil }

// fakeLocker grants every lock with an increasing token, or queues when queueBehind is set.
type fakeLocker struct {
	calls       []locks.AcquireCommand
	queueBehind *uuid.UUID
	err         error
}

func (f *fakeLocker) AcquireIn(_ context.Context, _ locks.Store, cmd locks.AcquireCommand) (locks.Result, error) {
	if f.err != nil {
		return locks.Result{}, f.err
	}
	f.calls = append(f.calls, cmd)
	if f.queueBehind != nil {
		return locks.Result{Outcome: locks.OutcomeQueued, Lock: locks.Lock{RequestID: *f.queueBehind, FencingToken: 1},
			QueuedBehind: f.queueBehind}, nil
	}
	return locks.Result{Outcome: locks.OutcomeGranted,
		Lock: locks.Lock{StackID: cmd.StackID, RequestID: cmd.RequestID, FencingToken: int64(40 + len(f.calls))}}, nil
}

// fakeVersions serves the sqs and lambda fixtures.
type fakeVersions struct{ err error }

func (f fakeVersions) GetVersion(_ context.Context, name, version string) (catalog.ModuleVersion, error) {
	if f.err != nil {
		return catalog.ModuleVersion{}, f.err
	}
	switch name + "@" + version {
	case "sqs@1.3.0":
		return catalog.ModuleVersion{ModuleName: "sqs", Version: catalog.SemVer{Major: 1, Minor: 3}, Status: "active",
			Outputs:      []catalog.Output{{Name: "queue_arn"}, {Name: "queue_url"}},
			InputsSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)}, nil
	case "lambda@2.0.1":
		return catalog.ModuleVersion{ModuleName: "lambda", Version: catalog.SemVer{Major: 2, Patch: 1}, Status: "active",
			InputsSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"queue_arn":{"type":"string"}}}`)}, nil
	}
	return catalog.ModuleVersion{}, apperr.NotFound("module %s has no version %s", name, version)
}

// seqIDs returns predictable ids.
type seqIDs struct{ n int }

func (s *seqIDs) NewID() uuid.UUID {
	s.n++
	return uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", s.n))
}

func (s *seqIDs) NewShortID(prefix string) string { s.n++; return fmt.Sprintf("%s-%06d", prefix, s.n) }

var t0 = time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

var dev = identity.Principal{UserID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Login: "dev", Type: identity.ActorUser}

// fakeMembers maps user id to role in teamID; unknown users have no role.
type fakeMembers map[uuid.UUID]Role

func (f fakeMembers) RoleInTeam(_ context.Context, team string, user uuid.UUID) (Role, error) {
	if team != "payments" {
		return RoleNone, apperr.NotFound("team %q not found", team)
	}
	return f[user], nil
}

func newSvc(repo Repository, v VersionLookup) *Service {
	return newSvcWithMembers(repo, v, fakeMembers{dev.UserID: RoleDeveloper, otherUser: RoleMaintainer})
}

var otherUser = uuid.MustParse("44444444-4444-4444-4444-444444444444")

func newSvcWithMembers(repo Repository, v VersionLookup, m Memberships) *Service {
	return NewService(repo, v, m, &fakeLocker{}, platform.FixedClock{T: t0}, &seqIDs{})
}

func TestCreateRequiresTeamRole(t *testing.T) {
	tests := []struct {
		role Role
		ok   bool
	}{
		{RoleNone, false}, {RoleViewer, false}, {RoleDeveloper, true}, {RoleMaintainer, true}, {RoleAdmin, true}, {"owner", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			repo := newMemRepo()
			svc := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{dev.UserID: tt.role})
			_, err := svc.Create(context.Background(), dev, createCmd(validInput(), "key-00000009"))
			if tt.ok != (err == nil) {
				t.Fatalf("role %q: err = %v", tt.role, err)
			}
			if !tt.ok && (!apperr.IsKind(err, apperr.KindForbidden) || len(repo.state.stacks) != 0 || len(repo.state.keys) != 0) {
				t.Fatalf("role %q: want forbidden with nothing persisted, got %v", tt.role, err)
			}
		})
	}
}

func TestRequiredRole(t *testing.T) {
	if RequiredRole(KindCreate, "production") != RoleDeveloper || RequiredRole(KindDestroy, "production") != RoleMaintainer ||
		RequiredRole(KindDestroy, "development") != RoleDeveloper {
		t.Fatal("RequiredRole does not match the docs/03 RBAC column")
	}
	if Role("bogus").AtLeast(RoleViewer) || !RoleAdmin.AtLeast(RoleDeveloper) || RoleViewer.AtLeast(RoleDeveloper) {
		t.Fatal("AtLeast ordering is wrong")
	}
}

func createCmd(in CreateInput, key string) CreateCommand {
	body, _ := json.Marshal(in)
	return CreateCommand{Input: in, IdempotencyKey: key, RequestHash: RequestHash("POST", "/v1/requests", body), CorrelationID: "corr-1"}
}

func TestCreate(t *testing.T) {
	repo := newMemRepo()
	svc := newSvc(repo, fakeVersions{})
	ctx := context.Background()
	in := validInput()
	in.Ephemeral = &Ephemeral{TTLHours: 72}

	res, err := svc.Create(ctx, dev, createCmd(in, "key-00000001"))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Request
	if res.Replayed || r.Status != StatusDraft || r.Kind != KindCreate || !strings.HasPrefix(r.ShortID, "req-") ||
		r.Stack.Path != "environments/development/payments/order-events" || r.CreatedBy != dev.UserID {
		t.Fatalf("request = %+v", r)
	}
	st := repo.state.stacks[0]
	if !st.IsEphemeral || !st.ExpiresAt.Equal(t0.Add(72*time.Hour)) || st.Region != "us-east-1" ||
		st.StateKey != "development/payments/order-events/terraform.tfstate" {
		t.Fatalf("stack = %+v", st)
	}
	var spec NormalizedSpec
	if err := json.Unmarshal(r.Spec, &spec); err != nil || spec.Resources[0].Name != "order_processor" {
		t.Fatalf("spec = %s (%v)", r.Spec, err)
	}
	ev := repo.state.events[0]
	if len(repo.state.events) != 1 || ev.Action != "request.created" || ev.SubjectID != r.ID.String() ||
		*ev.TeamID != teamID || ev.CorrelationID != "corr-1" || ev.ActorID != dev.UserID.String() {
		t.Fatalf("audit = %+v", repo.state.events)
	}
}

func TestCreateTakesTheStackLock(t *testing.T) {
	holder := uuid.MustParse("55555555-5555-4555-8555-555555555555")
	tests := []struct {
		name       string
		locker     *fakeLocker
		wantToken  *int64
		wantQueued *uuid.UUID
		wantErr    bool
	}{
		{name: "granted", locker: &fakeLocker{}, wantToken: ptr(int64(41))},
		{name: "queued", locker: &fakeLocker{queueBehind: &holder}, wantQueued: &holder},
		{name: "lock failure rolls back", locker: &fakeLocker{err: errors.New("db down")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMemRepo()
			svc := NewService(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleDeveloper}, tt.locker, platform.FixedClock{T: t0}, &seqIDs{})
			res, err := svc.Create(context.Background(), dev, createCmd(validInput(), "key-lock-0001"))
			if tt.wantErr {
				if err == nil || len(repo.state.requests) != 0 || len(repo.state.stacks) != 0 || len(repo.state.keys) != 0 {
					t.Fatalf("want error with nothing persisted, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r := res.Request
			if len(tt.locker.calls) != 1 || tt.locker.calls[0].RequestID != r.ID || tt.locker.calls[0].StackID != r.Stack.ID ||
				tt.locker.calls[0].By != dev || tt.locker.calls[0].CorrelationID != "corr-1" {
				t.Fatalf("locker calls = %+v", tt.locker.calls)
			}
			if !equalPtr(r.FencingToken, tt.wantToken) || !equalPtr(r.QueuedBehind, tt.wantQueued) || r.Status != StatusDraft {
				t.Fatalf("request token=%v queued=%v status=%s", r.FencingToken, r.QueuedBehind, r.Status)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func equalPtr[T comparable](a, b *T) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func TestCreateIdempotency(t *testing.T) {
	repo := newMemRepo()
	svc := newSvc(repo, fakeVersions{})
	ctx := context.Background()

	first, err := svc.Create(ctx, dev, createCmd(validInput(), "key-00000001"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Create(ctx, dev, createCmd(validInput(), "key-00000001"))
	if err != nil || !again.Replayed || again.Request.ID != first.Request.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if len(repo.state.requests) != 1 || len(repo.state.events) != 1 {
		t.Fatal("replay must not create a second request or audit event")
	}

	changed := validInput()
	changed.Justification = "different body"
	if _, err := svc.Create(ctx, dev, createCmd(changed, "key-00000001")); !hasCode(err, "idempotency_key_reuse") {
		t.Fatalf("want idempotency_key_reuse, got %v", err)
	}

	// Same key from another user is independent (and hits stack_exists here).
	other := dev
	other.UserID = otherUser
	if _, err := svc.Create(ctx, other, createCmd(validInput(), "key-00000001")); !hasCode(err, "stack_exists") {
		t.Fatalf("want stack_exists for a second create, got %v", err)
	}
}

func TestCreateFailuresRollBackTheKey(t *testing.T) {
	repo := newMemRepo()
	svc := newSvc(repo, fakeVersions{})
	ctx := context.Background()
	bad := validInput()
	bad.Resources[0].Inputs = json.RawMessage(`{}`)
	if _, err := svc.Create(ctx, dev, createCmd(bad, "key-00000002")); !hasCode(err, "validation_failed") {
		t.Fatalf("want validation_failed, got %v", err)
	}
	if len(repo.state.keys) != 0 || len(repo.state.stacks) != 0 {
		t.Fatal("failed create must not persist the key or stack")
	}
	// After fixing the body the same key works.
	if _, err := svc.Create(ctx, dev, createCmd(validInput(), "key-00000002")); err != nil {
		t.Fatalf("retry after fix: %v", err)
	}
}

func TestCreateErrors(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*CreateInput)
		key      string
		by       identity.Principal
		versions VersionLookup
		code     string
	}{
		{name: "missing key", key: "short", code: "bad_request"},
		{name: "no user", by: identity.Principal{Login: "wf", Type: identity.ActorGitHubActions}, code: "unauthenticated"},
		{name: "shape", mutate: func(c *CreateInput) { c.Stack = "" }, code: "validation_failed"},
		{name: "unknown team", mutate: func(c *CreateInput) { c.Team = "ghosts" }, code: "validation_failed"},
		{name: "unknown env", mutate: func(c *CreateInput) { c.Environment = "production" }, code: "validation_failed"},
		{name: "region", mutate: func(c *CreateInput) { c.Region = "eu-west-1" }, code: "policy_preflight_denied"},
		{name: "unknown version", mutate: func(c *CreateInput) { c.Resources[0].Version = "9.9.9" }, code: "module_version_unavailable"},
		{name: "bad ref", mutate: func(c *CreateInput) {
			c.Resources[1].Inputs = json.RawMessage(`{"name":"x","queue_arn":{"$ref":"orders_queue.nope"}}`)
		}, code: "validation_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			if tt.mutate != nil {
				tt.mutate(&in)
			}
			key := tt.key
			if key == "" {
				key = "key-00000003"
			}
			by := tt.by
			if by.Type == "" {
				by = dev
			}
			v := tt.versions
			if v == nil {
				v = fakeVersions{}
			}
			_, err := newSvc(newMemRepo(), v).Create(context.Background(), by, createCmd(in, key))
			if !hasCode(err, tt.code) {
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}

	// Infrastructure errors are wrapped, not turned into validation errors.
	_, err := newSvc(newMemRepo(), fakeVersions{err: errors.New("db down")}).
		Create(context.Background(), dev, createCmd(validInput(), "key-00000004"))
	if err == nil || apperr.IsKind(err, apperr.KindInvalid) || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("want wrapped internal error, got %v", err)
	}
}

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}

func TestListPagination(t *testing.T) {
	repo := newMemRepo()
	for i := range 5 {
		repo.state.requests = append(repo.state.requests, &Request{ID: uuid.New(), Status: StatusDraft, Kind: KindCreate,
			CreatedAt: t0.Add(time.Duration(i) * time.Minute)})
	}
	svc := newSvc(repo, fakeVersions{})
	ctx := context.Background()

	page, next, err := svc.List(ctx, ListFilter{Limit: 2})
	if err != nil || len(page) != 2 || next == nil || !page[0].CreatedAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("page1 = %v %v %v", page, next, err)
	}
	page, next, err = svc.List(ctx, ListFilter{Limit: 2, After: next})
	if err != nil || len(page) != 2 || next == nil || !page[0].CreatedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("page2 = %v %v %v", page, next, err)
	}
	page, next, err = svc.List(ctx, ListFilter{Limit: 2, After: next})
	if err != nil || len(page) != 1 || next != nil {
		t.Fatalf("page3 = %v %v %v", page, next, err)
	}
	if _, _, err := svc.List(ctx, ListFilter{Status: "nope"}); !hasCode(err, "bad_request") {
		t.Fatalf("want bad_request, got %v", err)
	}
}
