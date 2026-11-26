package requests

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

func TestKindHelpers(t *testing.T) {
	tests := []struct {
		kind                               Kind
		supported, existing, wantResources bool
	}{
		{KindCreate, true, false, true},
		{KindUpdate, true, true, true},
		{KindDestroy, true, true, false},
		{KindUpgrade, false, true, false},
		{KindReconcile, false, true, false},
		{KindRollback, false, true, false},
		{Kind("delete"), false, false, false},
	}
	for _, tt := range tests {
		if tt.kind.Supported() != tt.supported || tt.kind.TargetsExistingStack() != tt.existing ||
			tt.kind.RequiresResources() != tt.wantResources {
			t.Errorf("%s: supported=%v existing=%v resources=%v", tt.kind, tt.kind.Supported(),
				tt.kind.TargetsExistingStack(), tt.kind.RequiresResources())
		}
	}
}

func TestCheckStackAllowsKind(t *testing.T) {
	tests := []struct {
		name     string
		kind     Kind
		status   StackStatus
		open     []Kind
		wantCode string
	}{
		{name: "update an active stack", kind: KindUpdate, status: StackActive},
		{name: "update a drifted stack", kind: KindUpdate, status: StackDrifted},
		{name: "fix a failed stack forward", kind: KindUpdate, status: StackFailed},
		{name: "destroy an active stack", kind: KindDestroy, status: StackActive},
		{name: "destroy a drifted stack", kind: KindDestroy, status: StackDrifted},
		{name: "destroy a failed stack", kind: KindDestroy, status: StackFailed},
		{name: "queue behind an open update", kind: KindUpdate, status: StackActive, open: []Kind{KindUpdate}},
		{name: "queue a destroy behind an open update", kind: KindDestroy, status: StackActive, open: []Kind{KindUpdate, KindUpdate}},
		{name: "update while provisioning", kind: KindUpdate, status: StackProvisioning, wantCode: "stack_busy"},
		{name: "destroy while provisioning", kind: KindDestroy, status: StackProvisioning, wantCode: "stack_busy"},
		{name: "update while updating", kind: KindUpdate, status: StackUpdating, wantCode: "stack_busy"},
		{name: "destroy while updating", kind: KindDestroy, status: StackUpdating, wantCode: "stack_busy"},
		{name: "update while destroying", kind: KindUpdate, status: StackDestroying, wantCode: "stack_busy"},
		{name: "destroy while destroying", kind: KindDestroy, status: StackDestroying, wantCode: "stack_busy"},
		{name: "update a destroyed stack", kind: KindUpdate, status: StackDestroyed, wantCode: "stack_destroyed"},
		{name: "destroy a destroyed stack", kind: KindDestroy, status: StackDestroyed, wantCode: "stack_destroyed"},
		{name: "update behind an open destroy", kind: KindUpdate, status: StackActive, open: []Kind{KindDestroy}, wantCode: "destroy_pending"},
		{name: "second destroy", kind: KindDestroy, status: StackActive, open: []Kind{KindUpdate, KindDestroy}, wantCode: "destroy_pending"},
		{name: "create is not an existing-stack kind", kind: KindCreate, status: StackActive, wantCode: "stack_exists"},
		{name: "upgrade is unsupported here", kind: Kind("delete"), status: StackActive, wantCode: "unsupported_kind"},
		{name: "unknown status is busy", kind: KindUpdate, status: StackStatus("weird"), wantCode: "stack_busy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckStackAllowsKind(tt.kind, tt.status, tt.open)
			switch {
			case tt.wantCode == "" && err != nil:
				t.Fatalf("unexpected %v", err)
			case tt.wantCode != "" && !hasCode(err, tt.wantCode):
				t.Fatalf("err = %v, want %s", err, tt.wantCode)
			}
			if err != nil && !apperr.IsKind(err, apperr.KindConflict) && tt.wantCode != "unsupported_kind" {
				t.Fatalf("%s must be a conflict, got %v", tt.wantCode, err)
			}
		})
	}
}

// TestStackStatusFollowsTheKind pins the stack status table of docs/03 §4.2 that WBS 5.3 applies.
func TestStackStatusFollowsTheKind(t *testing.T) {
	tests := []struct {
		kind                          Kind
		applying, applied, failedWant StackStatus
	}{
		{KindCreate, StackProvisioning, StackActive, StackFailed},
		{KindUpdate, StackUpdating, StackActive, StackFailed},
		{KindUpgrade, StackUpdating, StackActive, StackFailed},
		{KindRollback, StackUpdating, StackActive, StackFailed},
		{KindReconcile, StackUpdating, StackActive, StackFailed},
		{KindDestroy, StackDestroying, StackDestroyed, StackFailed},
	}
	for _, tt := range tests {
		if got := StackStatusWhileApplying(tt.kind); got != tt.applying {
			t.Errorf("%s while applying: %s, want %s", tt.kind, got, tt.applying)
		}
		if got := StackStatusAfterApply(tt.kind, true); got != tt.applied {
			t.Errorf("%s applied: %s, want %s", tt.kind, got, tt.applied)
		}
		if got := StackStatusAfterApply(tt.kind, false); got != tt.failedWant {
			t.Errorf("%s failed: %s, want %s", tt.kind, got, tt.failedWant)
		}
	}
	for _, s := range AllStackStatuses {
		if !s.Valid() {
			t.Errorf("%s is listed but not valid", s)
		}
	}
	if StackStatus("weird").Valid() {
		t.Error("an unknown status must not be valid")
	}
}

func TestTeamFromStackPath(t *testing.T) {
	for path, want := range map[string]string{
		"environments/development/payments/order-events": "payments",
		"environments/production/a1/b1":                  "a1",
		"environments/development/payments":              "",
		"modules/s3/x/y":                                 "",
		"environments/development//order-events":         "",
		"":                                               "",
	} {
		got, ok := TeamFromStackPath(path)
		if got != want || ok != (want != "") {
			t.Errorf("TeamFromStackPath(%q) = %q, %v", path, got, ok)
		}
	}
}

var existingStack = uuid.MustParse("66666666-6666-4666-8666-666666666666")

func updateInput() CreateInput {
	in := validInput()
	return CreateInput{Kind: KindUpdate, StackID: &existingStack, Resources: in.Resources, Justification: "raise the timeout"}
}

func destroyInput() CreateInput {
	return CreateInput{Kind: KindDestroy, StackID: &existingStack, ConfirmStackName: "order-events", Justification: "feature retired"}
}

func TestValidateShapeUpdateAndDestroy(t *testing.T) {
	zero := uuid.Nil
	tests := []struct {
		name   string
		base   func() CreateInput
		mutate func(*CreateInput)
		want   []string
	}{
		{name: "valid update", base: updateInput, mutate: func(*CreateInput) {}},
		{name: "valid destroy", base: destroyInput, mutate: func(*CreateInput) {}},
		{name: "update without stack_id", base: updateInput, mutate: func(c *CreateInput) { c.StackID = nil }, want: []string{"/stack_id:required"}},
		{name: "update with a nil stack_id", base: updateInput, mutate: func(c *CreateInput) { c.StackID = &zero }, want: []string{"/stack_id:required"}},
		{name: "update naming the stack again", base: updateInput,
			mutate: func(c *CreateInput) { c.Team, c.Environment, c.Stack = "payments", "development", "order-events" },
			want:   []string{"/environment:not_allowed", "/stack:not_allowed", "/team:not_allowed"}},
		{name: "update cannot move regions", base: updateInput, mutate: func(c *CreateInput) { c.Region = "eu-west-1" }, want: []string{"/region:not_allowed"}},
		{name: "update cannot make a stack ephemeral", base: updateInput,
			mutate: func(c *CreateInput) { c.Ephemeral = &Ephemeral{TTLHours: 1} }, want: []string{"/ephemeral:not_allowed"}},
		{name: "update with no resources", base: updateInput, mutate: func(c *CreateInput) { c.Resources = nil }, want: []string{"/resources:minItems"}},
		{name: "update with duplicate resources", base: updateInput, mutate: func(c *CreateInput) { c.Resources[1].Name = "orders_queue" },
			want: []string{"/resources:duplicate"}},
		{name: "update with a bad version", base: updateInput, mutate: func(c *CreateInput) { c.Resources[0].Version = "latest" },
			want: []string{"/resources/0/version:pattern"}},
		{name: "update cannot confirm a name", base: updateInput, mutate: func(c *CreateInput) { c.ConfirmStackName = "x" },
			want: []string{"/confirm_stack_name:not_allowed"}},
		{name: "update with depends_on", base: updateInput, mutate: func(c *CreateInput) { c.DependsOn = []string{"x"} },
			want: []string{"/depends_on:unsupported"}},
		{name: "update needs no justification", base: updateInput, mutate: func(c *CreateInput) { c.Justification = "" }},
		{name: "destroy without confirmation", base: destroyInput, mutate: func(c *CreateInput) { c.ConfirmStackName = "" },
			want: []string{"/confirm_stack_name:required"}},
		{name: "destroy with a malformed confirmation", base: destroyInput, mutate: func(c *CreateInput) { c.ConfirmStackName = "Order Events" },
			want: []string{"/confirm_stack_name:required"}},
		{name: "destroy without justification", base: destroyInput, mutate: func(c *CreateInput) { c.Justification = "  " },
			want: []string{"/justification:required"}},
		{name: "destroy with resources", base: destroyInput, mutate: func(c *CreateInput) { c.Resources = validInput().Resources },
			want: []string{"/resources:not_allowed"}},
		{name: "destroy without stack_id", base: destroyInput, mutate: func(c *CreateInput) { c.StackID = nil }, want: []string{"/stack_id:required"}},
		{name: "destroy with a huge justification", base: destroyInput,
			mutate: func(c *CreateInput) { c.Justification = strings.Repeat("x", 2001) }, want: []string{"/justification:maxLength"}},
		{name: "create cannot carry stack_id", base: validInput, mutate: func(c *CreateInput) { c.StackID = &existingStack },
			want: []string{"/stack_id:not_allowed"}},
		{name: "create cannot confirm a name", base: validInput, mutate: func(c *CreateInput) { c.ConfirmStackName = "order-events" },
			want: []string{"/confirm_stack_name:not_allowed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.base()
			in.Resources = slices.Clone(in.Resources)
			tt.mutate(&in)
			got := pointers(ValidateShape(in))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeForStack(t *testing.T) {
	target := StackTarget{ID: existingStack, TeamSlug: "payments", Environment: "development", Name: "order-events", Region: "us-west-2"}
	up := NormalizeForStack(updateInput(), target)
	if up.Team != "payments" || up.Environment != "development" || up.Stack != "order-events" || up.Region != "us-west-2" ||
		len(up.Resources) != 2 || up.Resources[0].Name != "order_processor" || up.Ephemeral != nil {
		t.Fatalf("update spec = %+v", up)
	}
	down, _ := json.Marshal(NormalizeForStack(destroyInput(), target))
	if !strings.Contains(string(down), `"resources":[]`) || !strings.Contains(string(down), `"depends_on":[]`) {
		t.Fatalf("a destroy spec must carry empty lists, got %s", down)
	}
}

// existingStackFixture creates the stack through a create request, then puts it in status.
func existingStackFixture(t *testing.T, status StackStatus) (*memRepo, *Service, *Request) {
	t.Helper()
	repo := newMemRepo()
	svc := newSvc(repo, fakeVersions{})
	res, err := svc.Create(context.Background(), dev, createCmd(validInput(), "key-create-0001"))
	if err != nil {
		t.Fatal(err)
	}
	repo.setStackStatus(res.Request.Stack.ID, status)
	// The creating request is over: only the requests under test count as open.
	res.Request.Status = StatusApplied
	in := updateInput()
	id := res.Request.Stack.ID
	in.StackID = &id
	return repo, svc, res.Request
}

func stackInput(in CreateInput, id uuid.UUID) CreateInput {
	in.StackID = &id
	return in
}

func TestUpdateRequest(t *testing.T) {
	repo, svc, created := existingStackFixture(t, StackActive)
	ctx := context.Background()
	in := stackInput(updateInput(), created.Stack.ID)

	res, err := svc.Create(ctx, dev, createCmd(in, "key-update-0001"))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Request
	if r.Kind != KindUpdate || r.Status != StatusDraft || r.Stack.ID != created.Stack.ID || r.Stack.Status != "active" ||
		r.Stack.Path != created.Stack.Path || r.FencingToken == nil || r.QueuedBehind != nil || r.Justification != "raise the timeout" {
		t.Fatalf("request = %+v", r)
	}
	var spec NormalizedSpec
	if err := json.Unmarshal(r.Spec, &spec); err != nil || spec.Team != "payments" || spec.Stack != "order-events" ||
		spec.Region != "us-east-1" || len(spec.Resources) != 2 {
		t.Fatalf("spec = %s (%v)", r.Spec, err)
	}
	if len(repo.state.stacks) != 1 {
		t.Fatalf("an update must not insert a stack: %d", len(repo.state.stacks))
	}
	ev := repo.state.events[len(repo.state.events)-1]
	if ev.Action != "request.created" || ev.SubjectID != r.ID.String() || *ev.TeamID != teamID || ev.ActorID != dev.UserID.String() ||
		!strings.Contains(string(ev.Payload), `"kind":"update"`) {
		t.Fatalf("audit = %+v", ev)
	}

	// The same key and body replay; the same key with another body is refused.
	again, err := svc.Create(ctx, dev, createCmd(in, "key-update-0001"))
	if err != nil || !again.Replayed || again.Request.ID != r.ID {
		t.Fatalf("replay: %+v, %v", again, err)
	}
	other := in
	other.Justification = "another reason"
	if _, err := svc.Create(ctx, dev, createCmd(other, "key-update-0001")); !hasCode(err, "idempotency_key_reuse") {
		t.Fatalf("key reuse: %v", err)
	}
}

func TestRemovedResources(t *testing.T) {
	prev := NormalizedSpec{Resources: []ResourceInput{{Name: "b"}, {Name: "a"}, {Name: "c"}}}
	if got := RemovedResources(prev, []ResourceInput{{Name: "a"}, {Name: "new"}}); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("got %v", got)
	}
	if got := RemovedResources(prev, []ResourceInput{{Name: "c"}, {Name: "b"}, {Name: "a"}}); len(got) != 0 {
		t.Fatalf("same names in another order remove nothing: %v", got)
	}
	if got := RemovedResources(NormalizedSpec{}, nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

// An update replaces the whole resource list. A form built from an older spec would delete
// what a newer request added, so shrinking the list needs an explicit allow_removals.
func TestUpdateCannotSilentlyRemoveResources(t *testing.T) {
	ctx := context.Background()
	_, svc, created := existingStackFixture(t, StackActive)
	shrunk := stackInput(updateInput(), created.Stack.ID)
	shrunk.Resources = shrunk.Resources[:1] // drops one of the two resources of the create

	_, err := svc.Create(ctx, dev, createCmd(shrunk, "key-shrink-0001"))
	e, ok := apperr.As(err)
	if !ok || e.Code != "resources_removed" || e.Kind != apperr.KindInvalid || len(e.Fields) != 1 || e.Fields[0].Pointer != "/resources" {
		t.Fatalf("err = %v", err)
	}
	dropped := "order_processor"
	if shrunk.Resources[0].Name == dropped {
		dropped = "orders_queue"
	}
	if !strings.Contains(e.Message, dropped) {
		t.Fatalf("the message must name %s: %s", dropped, e.Message)
	}

	shrunk.AllowRemovals = true
	if _, err := svc.Create(ctx, dev, createCmd(shrunk, "key-shrink-0002")); err != nil {
		t.Fatalf("an explicit allow_removals must pass: %v", err)
	}
	// The newest spec is now the shrunk one, so a form built from it may add resources again.
	grown := stackInput(updateInput(), created.Stack.ID)
	if _, err := svc.Create(ctx, dev, createCmd(grown, "key-shrink-0003")); err != nil {
		t.Fatalf("growing the list needs no flag: %v", err)
	}
}

func TestRemovalCheckIgnoresSpecsThatNeverApplied(t *testing.T) {
	for _, dead := range []Status{StatusFailed, StatusRejected, StatusCancelled} {
		_, svc, created := existingStackFixture(t, StackActive)
		created.Status = dead // the only earlier spec never took effect
		in := stackInput(updateInput(), created.Stack.ID)
		in.Resources = in.Resources[:1]
		if _, err := svc.Create(context.Background(), dev, createCmd(in, "key-dead-spec-01")); err != nil {
			t.Fatalf("%s: %v", dead, err)
		}
	}
}

func TestAllowRemovalsIsForUpdatesOnly(t *testing.T) {
	c := validInput()
	c.AllowRemovals = true
	d := destroyInput()
	d.AllowRemovals = true
	for name, in := range map[string]CreateInput{"create": c, "destroy": d} {
		if !slices.Contains(pointers(ValidateShape(in)), "/allow_removals:not_allowed") {
			t.Errorf("%s accepted allow_removals", name)
		}
	}
	u := updateInput()
	u.AllowRemovals = true
	if len(ValidateShape(u)) != 0 {
		t.Fatalf("an update may carry it: %v", ValidateShape(u))
	}
}

func TestDestroyRequest(t *testing.T) {
	repo, svc, created := existingStackFixture(t, StackActive)
	ctx := context.Background()
	in := stackInput(destroyInput(), created.Stack.ID)

	res, err := svc.Create(ctx, dev, createCmd(in, "key-destroy-0001"))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Request
	if r.Kind != KindDestroy || r.Status != StatusDraft || r.FencingToken == nil {
		t.Fatalf("request = %+v", r)
	}
	var spec NormalizedSpec
	if err := json.Unmarshal(r.Spec, &spec); err != nil || len(spec.Resources) != 0 || spec.Stack != "order-events" {
		t.Fatalf("spec = %s (%v)", r.Spec, err)
	}
	if !strings.Contains(string(r.Spec), `"resources":[]`) {
		t.Fatalf("a destroy spec stores an empty resource list, got %s", r.Spec)
	}

	// While the destroy is open nothing else may follow it, and it cannot be repeated.
	for i, next := range []CreateInput{stackInput(updateInput(), created.Stack.ID), stackInput(destroyInput(), created.Stack.ID)} {
		if _, err := svc.Create(ctx, dev, createCmd(next, "key-after-destroy-"+string(rune('a'+i)))); !hasCode(err, "destroy_pending") {
			t.Fatalf("follow-up %d: %v", i, err)
		}
	}
	if n := len(repo.state.requests); n != 2 {
		t.Fatalf("refused requests must not persist: %d requests", n)
	}
}

func TestDestroyAuditRecordsWhy(t *testing.T) {
	repo, svc, created := existingStackFixture(t, StackActive)
	if _, err := svc.Create(context.Background(), dev, createCmd(stackInput(destroyInput(), created.Stack.ID), "key-destroy-0003")); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(repo.state.events[len(repo.state.events)-1].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["justification"] != "feature retired" || p["confirm_stack_name"] != "order-events" || p["kind"] != "destroy" ||
		p["stack_id"] != created.Stack.ID.String() {
		t.Fatalf("audit payload = %v", p)
	}
	// A create keeps its payload small, and an update records its justification.
	var first map[string]any
	_ = json.Unmarshal(repo.state.events[0].Payload, &first)
	if _, ok := first["justification"]; ok {
		t.Fatalf("create payload = %v", first)
	}
}

func TestDestroyNeedsTheStackName(t *testing.T) {
	repo, svc, created := existingStackFixture(t, StackActive)
	in := stackInput(destroyInput(), created.Stack.ID)
	in.ConfirmStackName = "other-stack"
	_, err := svc.Create(context.Background(), dev, createCmd(in, "key-destroy-0002"))
	if !hasCode(err, "validation_failed") {
		t.Fatalf("err = %v", err)
	}
	if e, _ := apperr.As(err); len(e.Fields) != 1 || e.Fields[0].Pointer != "/confirm_stack_name" || e.Fields[0].Code != "mismatch" {
		t.Fatalf("fields = %+v", e.Fields)
	}
	if len(repo.state.requests) != 1 {
		t.Fatal("nothing may be persisted")
	}
}

func TestUpdateAndDestroyNeedAStateThatTakesThem(t *testing.T) {
	tests := []struct {
		status   StackStatus
		wantCode string
	}{
		{StackActive, ""}, {StackDrifted, ""}, {StackFailed, ""},
		{StackProvisioning, "stack_busy"}, {StackUpdating, "stack_busy"}, {StackDestroying, "stack_busy"},
		{StackDestroyed, "stack_destroyed"},
	}
	for _, tt := range tests {
		for _, kind := range []Kind{KindUpdate, KindDestroy} {
			t.Run(string(tt.status)+"/"+string(kind), func(t *testing.T) {
				repo, svc, created := existingStackFixture(t, tt.status)
				in := stackInput(updateInput(), created.Stack.ID)
				if kind == KindDestroy {
					in = stackInput(destroyInput(), created.Stack.ID)
				}
				_, err := svc.Create(context.Background(), dev, createCmd(in, "key-state-00001"))
				if tt.wantCode == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if !hasCode(err, tt.wantCode) || !apperr.IsKind(err, apperr.KindConflict) {
					t.Fatalf("err = %v, want conflict %s", err, tt.wantCode)
				}
				if len(repo.state.requests) != 1 || len(repo.state.keys) != 1 {
					t.Fatalf("a refused request must persist nothing (and release its idempotency key): %d requests, %d keys",
						len(repo.state.requests), len(repo.state.keys))
				}
			})
		}
	}
}

func TestUpdateQueuesBehindTheLockHolder(t *testing.T) {
	holder := uuid.MustParse("77777777-7777-4777-8777-777777777777")
	repo, _, created := existingStackFixture(t, StackActive)
	locker := &fakeLocker{queueBehind: &holder}
	svc := NewService(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleDeveloper}, locker, newSvc(repo, fakeVersions{}).clock, &seqIDs{n: 100})
	res, err := svc.Create(context.Background(), dev, createCmd(stackInput(updateInput(), created.Stack.ID), "key-queue-00001"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Request.QueuedBehind == nil || *res.Request.QueuedBehind != holder || res.Request.FencingToken != nil {
		t.Fatalf("an update behind another request must queue, got %+v", res.Request)
	}
	if len(locker.calls) != 1 || locker.calls[0].StackID != created.Stack.ID {
		t.Fatalf("lock calls = %+v", locker.calls)
	}
}

func TestUpdateAndDestroyChecks(t *testing.T) {
	t.Run("unknown and hidden stacks look the same", func(t *testing.T) {
		repo, _, created := existingStackFixture(t, StackActive)
		svc := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleNone})
		_, err1 := svc.Create(context.Background(), dev, createCmd(stackInput(updateInput(), uuid.New()), "key-check-00001"))
		_, hidden := svc.Create(context.Background(), dev, createCmd(stackInput(updateInput(), created.Stack.ID), "key-check-00002"))
		_, hiddenDestroy := svc.Create(context.Background(), dev, createCmd(stackInput(destroyInput(), created.Stack.ID), "key-check-00003"))
		for name, err := range map[string]error{"unknown": err1, "hidden update": hidden, "hidden destroy": hiddenDestroy} {
			e, ok := apperr.As(err)
			if !ok || e.Kind != apperr.KindNotFound || e.Code != "stack_not_found" || strings.Contains(e.Message, "payments") {
				t.Errorf("%s: err = %v, want a 404 stack_not_found that names no team", name, err)
			}
		}
	})
	t.Run("members below the needed role are refused", func(t *testing.T) {
		repo, _, created := existingStackFixture(t, StackActive)
		svc := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleViewer})
		for _, in := range []CreateInput{updateInput(), destroyInput()} {
			_, err := svc.Create(context.Background(), dev, createCmd(stackInput(in, created.Stack.ID), "key-role-000001"))
			if !apperr.IsKind(err, apperr.KindForbidden) {
				t.Fatalf("viewer, %s: err = %v", in.Kind, err)
			}
		}
	})
	t.Run("module version must exist", func(t *testing.T) {
		_, svc, created := existingStackFixture(t, StackActive)
		in := stackInput(updateInput(), created.Stack.ID)
		in.Resources[0].Version = "9.9.9"
		_, err := svc.Create(context.Background(), dev, createCmd(in, "key-check-00002"))
		if !hasCode(err, "module_version_unavailable") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("inputs are validated against the schema", func(t *testing.T) {
		_, svc, created := existingStackFixture(t, StackActive)
		in := stackInput(updateInput(), created.Stack.ID)
		in.Resources[1].Inputs = json.RawMessage(`{"queue_arn":"x"}`) // lambda requires name
		_, err := svc.Create(context.Background(), dev, createCmd(in, "key-check-00003"))
		if !hasCode(err, "validation_failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a destroy never resolves modules", func(t *testing.T) {
		repo, _, created := existingStackFixture(t, StackActive)
		svc := newSvc(repo, fakeVersions{err: apperr.NotFound("catalog down")})
		if _, err := svc.Create(context.Background(), dev, createCmd(stackInput(destroyInput(), created.Stack.ID), "key-check-00004")); err != nil {
			t.Fatalf("a destroy must work even when versions are yanked or unavailable: %v", err)
		}
	})
}

func TestUpdateOfProductionNeedsNoMoreThanDeveloper(t *testing.T) {
	if RequiredRole(KindUpdate, "production") != RoleDeveloper || RequiredRole(KindDestroy, "production") != RoleMaintainer {
		t.Fatal("docs/03 §6.4: developer for any update, maintainer for a production destroy")
	}
}

// authorizeKind is the rbac matrix: developers create and update anywhere, destroy outside
// production, and maintainers destroy in production; an unknown environment is production.
func TestAuthorizeKindFollowsTheRBACMatrix(t *testing.T) {
	tests := []struct {
		role Role
		kind Kind
		env  string
		ok   bool
	}{
		{RoleViewer, KindUpdate, "development", false},
		{RoleNone, KindCreate, "development", false},
		{RoleDeveloper, KindCreate, "production", true},
		{RoleDeveloper, KindUpdate, "production", true},
		{RoleDeveloper, KindDestroy, "development", true},
		{RoleDeveloper, KindDestroy, "staging", true},
		{RoleDeveloper, KindDestroy, "production", false},
		{RoleDeveloper, KindDestroy, "qa", false}, // unknown environment counts as production
		{RoleDeveloper, KindDestroy, "", false},
		{RoleMaintainer, KindDestroy, "production", true},
		{RoleAdmin, KindDestroy, "qa", true},
		{Role("bogus"), KindUpdate, "development", false},
	}
	for _, tt := range tests {
		err := authorizeKind(tt.role, tt.kind, tt.env, "payments")
		if tt.ok != (err == nil) || (err != nil && !apperr.IsKind(err, apperr.KindForbidden)) {
			t.Errorf("%q %s in %q: err = %v", tt.role, tt.kind, tt.env, err)
		}
	}
	err := authorizeKind(RoleDeveloper, KindDestroy, "production", "payments")
	if e, _ := apperr.As(err); !strings.Contains(e.Message, "needs the maintainer role") {
		t.Fatalf("message = %v", err)
	}
}

func TestUpgradeIsRejectedUntilItIsImplemented(t *testing.T) {
	in := updateInput()
	in.Kind = KindUpgrade
	if !slices.Contains(pointers(ValidateShape(in)), "/kind:unsupported_kind") {
		t.Fatal("upgrade must be refused with unsupported_kind")
	}
}
