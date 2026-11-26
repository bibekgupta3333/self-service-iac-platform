package recovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func specJSON(names ...string) json.RawMessage {
	res := make([]map[string]any, 0, len(names))
	for _, n := range names {
		res = append(res, map[string]any{"name": n, "module": "sqs", "version": "1.3.0", "inputs": map[string]any{"name": n}})
	}
	b, _ := json.Marshal(map[string]any{"team": "payments", "environment": "development", "stack": "orders",
		"region": "us-east-1", "resources": res, "depends_on": []string{}})
	return b
}

var (
	newest = AppliedSpec{RequestID: uuid.New(), ShortID: "req-newest1", Spec: specJSON("queue", "dlq"),
		CompletedAt: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	older = AppliedSpec{RequestID: uuid.New(), ShortID: "req-older01", Spec: specJSON("queue"),
		CompletedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
)

func code(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Code
	}
	return ""
}

func TestSelectRollbackTarget(t *testing.T) {
	both := []AppliedSpec{newest, older}
	cases := []struct {
		name    string
		status  requests.StackStatus
		open    int
		applied []AppliedSpec
		want    string // short id, or the error code
	}{
		{"failed stack returns to the last good configuration", requests.StackFailed, 0, both, "req-newest1"},
		{"active stack undoes the newest change", requests.StackActive, 0, both, "req-older01"},
		{"drifted stack undoes the newest change", requests.StackDrifted, 0, both, "req-older01"},
		{"active stack with one configuration", requests.StackActive, 0, both[:1], "no_rollback_target"},
		{"failed stack never applied", requests.StackFailed, 0, nil, "no_rollback_target"},
		{"destroyed", requests.StackDestroyed, 0, both, "stack_destroyed"},
		{"updating", requests.StackUpdating, 0, both, "stack_busy"},
		{"provisioning", requests.StackProvisioning, 0, both, "stack_busy"},
		{"destroying", requests.StackDestroying, 0, both, "stack_busy"},
		{"open request", requests.StackActive, 1, both, "stack_busy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectRollbackTarget(tc.status, tc.open, tc.applied)
			if err != nil {
				if code(err) != tc.want || !apperr.IsKind(err, apperr.KindConflict) {
					t.Fatalf("err %v, want %s", err, tc.want)
				}
				return
			}
			if got.ShortID != tc.want {
				t.Fatalf("target %s, want %s", got.ShortID, tc.want)
			}
		})
	}
}

func TestValidateReason(t *testing.T) {
	if err := ValidateReason("  error rates doubled after req-newest1  "); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"", "   \n", strings.Repeat("x", MaxReasonLength+1)} {
		if err := ValidateReason(r); code(err) != "validation_failed" {
			t.Errorf("reason of %d chars: %v", len(r), err)
		}
	}
}

func TestRollbackInput(t *testing.T) {
	stack := uuid.New()
	in, err := RollbackInput(stack, older, " error rates doubled ")
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != requests.KindUpdate || *in.StackID != stack || !in.AllowRemovals || in.RollbackOf != "req-older01" ||
		len(in.Resources) != 1 || in.Resources[0].Name != "queue" {
		t.Fatalf("input %+v", in)
	}
	want := "Rollback to the configuration applied by req-older01 (completed 2026-11-10T12:00:00Z).\n\nReason: error rates doubled"
	if in.Justification != want {
		t.Fatalf("justification %q", in.Justification)
	}
	// The input passes the request shape checks an update must pass.
	if errs := requests.ValidateShape(in); len(errs) > 0 {
		t.Fatalf("shape errors %v", errs)
	}
	// The longest reason still fits the request's justification limit.
	in, err = RollbackInput(stack, older, strings.Repeat("r", MaxReasonLength))
	if err != nil || len(requests.ValidateShape(in)) > 0 {
		t.Fatalf("long reason: %v %v", err, requests.ValidateShape(in))
	}
	if _, err := RollbackInput(stack, AppliedSpec{ShortID: "req-x", Spec: json.RawMessage(`{"resources":[]}`)}, "r"); code(err) != "no_rollback_target" {
		t.Fatalf("empty spec: %v", err)
	}
	if _, err := RollbackInput(stack, AppliedSpec{ShortID: "req-x", Spec: json.RawMessage(`{`)}, "r"); err == nil {
		t.Fatal("a corrupt spec must fail")
	}
}

// --- service --------------------------------------------------------------------------

type fakeStacks map[uuid.UUID]requests.StackTarget

func (f fakeStacks) StackTarget(_ context.Context, id uuid.UUID) (requests.StackTarget, error) {
	t, ok := f[id]
	if !ok {
		return requests.StackTarget{}, apperr.NotFound("stack %s not found", id)
	}
	return t, nil
}

type fakeMembers map[uuid.UUID]rbac.Role

func (f fakeMembers) RoleInTeam(_ context.Context, _ string, user uuid.UUID) (rbac.Role, error) {
	return f[user], nil
}

type fakeHistory struct {
	open    int
	applied []AppliedSpec
}

func (f fakeHistory) OpenRequests(context.Context, uuid.UUID) (int, error) { return f.open, nil }
func (f fakeHistory) AppliedSpecs(_ context.Context, _ uuid.UUID, limit int) ([]AppliedSpec, error) {
	return f.applied[:min(limit, len(f.applied))], nil
}

type fakeCreator struct {
	created []requests.CreateCommand
	replay  *requests.Request
}

func (f *fakeCreator) Create(_ context.Context, _ identity.Principal, cmd requests.CreateCommand) (requests.CreateResult, error) {
	f.created = append(f.created, cmd)
	return requests.CreateResult{Request: &requests.Request{ShortID: "req-rollbk1", Kind: cmd.Input.Kind}}, nil
}

func (f *fakeCreator) Replay(context.Context, identity.Principal, string, string) (*requests.Request, bool, error) {
	return f.replay, f.replay != nil, nil
}

func TestRollbackService(t *testing.T) {
	stackID := uuid.New()
	maintainer, developer, outsider := uuid.New(), uuid.New(), uuid.New()
	stacks := fakeStacks{stackID: {ID: stackID, TeamSlug: "payments", Environment: "production", Name: "orders",
		Path: "environments/production/payments/orders", Status: requests.StackFailed}}
	members := fakeMembers{maintainer: rbac.RoleMaintainer, developer: rbac.RoleDeveloper}
	cmd := RollbackCommand{StackID: stackID, Reason: "error rates doubled", IdempotencyKey: "rollback-key-1", RequestHash: "h1", CorrelationID: "c1"}
	user := func(id uuid.UUID) identity.Principal { return identity.Principal{UserID: id, Type: identity.ActorUser} }
	ctx := context.Background()

	creator := &fakeCreator{}
	svc := NewRollbackService(stacks, members, fakeHistory{applied: []AppliedSpec{newest, older}}, creator)
	res, err := svc.Rollback(ctx, user(maintainer), cmd)
	if err != nil || res.Request.ShortID != "req-rollbk1" {
		t.Fatalf("rollback: %+v, %v", res, err)
	}
	got := creator.created[0]
	if got.IdempotencyKey != "rollback-key-1" || got.RequestHash != "h1" || got.CorrelationID != "c1" ||
		got.Input.RollbackOf != "req-newest1" || got.Input.Kind != requests.KindUpdate {
		t.Fatalf("create command %+v", got)
	}

	cases := []struct {
		name string
		by   identity.Principal
		cmd  RollbackCommand
		hist fakeHistory
		kind apperr.Kind
		code string
	}{
		{"developer", user(developer), cmd, fakeHistory{applied: []AppliedSpec{newest}}, apperr.KindForbidden, "forbidden"},
		{"no role is not found", user(outsider), cmd, fakeHistory{applied: []AppliedSpec{newest}}, apperr.KindNotFound, "stack_not_found"},
		{"unknown stack", user(maintainer), RollbackCommand{StackID: uuid.New(), Reason: "r"}, fakeHistory{}, apperr.KindNotFound, "stack_not_found"},
		{"no reason", user(maintainer), RollbackCommand{StackID: stackID}, fakeHistory{}, apperr.KindInvalid, "validation_failed"},
		{"busy", user(maintainer), cmd, fakeHistory{open: 1, applied: []AppliedSpec{newest}}, apperr.KindConflict, "stack_busy"},
		{"nothing applied", user(maintainer), cmd, fakeHistory{}, apperr.KindConflict, "no_rollback_target"},
		{"no user", identity.Principal{Type: identity.ActorSystem}, cmd, fakeHistory{}, apperr.KindUnauthenticated, "unauthenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCreator{}
			_, err := NewRollbackService(stacks, members, tc.hist, c).Rollback(ctx, tc.by, tc.cmd)
			if !apperr.IsKind(err, tc.kind) || (tc.code != "forbidden" && code(err) != tc.code) {
				t.Fatalf("err %v, want %s", err, tc.code)
			}
			if len(c.created) != 0 {
				t.Fatal("nothing may be created")
			}
		})
	}

	// A retried call returns the request it created, even though that request now makes
	// the stack busy.
	replayed := &requests.Request{ShortID: "req-rollbk1"}
	c := &fakeCreator{replay: replayed}
	res, err = NewRollbackService(stacks, members, fakeHistory{open: 1}, c).Rollback(ctx, user(maintainer), cmd)
	if err != nil || !res.Replayed || res.Request != replayed || len(c.created) != 0 {
		t.Fatalf("replay: %+v, %v", res, err)
	}
	// A replay still needs the role.
	if _, err := NewRollbackService(stacks, members, fakeHistory{}, c).Rollback(ctx, user(developer), cmd); !apperr.IsKind(err, apperr.KindForbidden) {
		t.Fatalf("replay by a developer: %v", err)
	}
}
