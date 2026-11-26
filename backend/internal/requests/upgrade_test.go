package requests

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
)

// sqsTarget serves the sqs versions an upgrade can move to:
//   - 1.4.0, 2.2.0, 3.0.0: compatible with the fixture's inputs and outputs;
//   - 1.5.0: yanked;
//   - 2.0.0: requires a new input (dlq_name), so the current inputs no longer validate;
//   - 2.1.0: drops the queue_url output that order_processor references.
func sqsTarget(version string) catalog.ModuleVersion {
	v, _ := catalog.ParseSemVer(version)
	mv := catalog.ModuleVersion{ModuleName: "sqs", Version: v, Status: catalog.StatusActive,
		Outputs:      []catalog.Output{{Name: "queue_arn"}, {Name: "queue_url"}},
		InputsSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)}
	switch version {
	case "1.5.0":
		mv.Status = catalog.StatusYanked
	case "2.0.0":
		mv.InputsSchema = json.RawMessage(`{"type":"object","required":["name","dlq_name"],"properties":{"name":{"type":"string"},"dlq_name":{"type":"string"}}}`)
	case "2.1.0":
		mv.Outputs = []catalog.Output{{Name: "queue_arn"}}
	}
	return mv
}

func upgradeInput(to string) CreateInput {
	return CreateInput{Kind: KindUpgrade, StackID: &existingStack, Justification: "pick up the fixes",
		Upgrades: []UpgradeInput{{Resource: "orders_queue", ToVersion: to}}}
}

func TestValidateShapeUpgrade(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateInput)
		want   []string
	}{
		{name: "valid", mutate: func(*CreateInput) {}},
		{name: "no upgrades", mutate: func(c *CreateInput) { c.Upgrades = nil }, want: []string{"/upgrades:minItems"}},
		{name: "resources are not sent", mutate: func(c *CreateInput) { c.Resources = validInput().Resources },
			want: []string{"/resources:not_allowed"}},
		{name: "bad entries", mutate: func(c *CreateInput) {
			c.Upgrades = []UpgradeInput{{Resource: "Orders", ToVersion: "latest"}}
		}, want: []string{"/upgrades/0/resource:pattern", "/upgrades/0/to_version:pattern"}},
		{name: "duplicate resource", mutate: func(c *CreateInput) {
			c.Upgrades = append(c.Upgrades, UpgradeInput{Resource: "orders_queue", ToVersion: "1.5.0"})
		}, want: []string{"/upgrades:duplicate"}},
		{name: "no removals", mutate: func(c *CreateInput) { c.AllowRemovals = true }, want: []string{"/allow_removals:not_allowed"}},
		{name: "no stack name", mutate: func(c *CreateInput) { c.Team = "payments" }, want: []string{"/team:not_allowed"}},
		{name: "no confirmation", mutate: func(c *CreateInput) { c.ConfirmStackName = "order-events" },
			want: []string{"/confirm_stack_name:not_allowed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := upgradeInput("1.4.0")
			tt.mutate(&in)
			if got := pointers(ValidateShape(in)); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	// Other kinds carry no upgrades.
	for _, in := range []CreateInput{validInput(), updateInput(), destroyInput()} {
		in.Upgrades, in.AcknowledgeBreaking = []UpgradeInput{{Resource: "orders_queue", ToVersion: "1.4.0"}}, true
		got := pointers(ValidateShape(in))
		if !slices.Contains(got, "/upgrades:not_allowed") || !slices.Contains(got, "/acknowledge_breaking:not_allowed") {
			t.Errorf("%s: %v", in.Kind, got)
		}
	}
}

func TestPlanUpgrade(t *testing.T) {
	current := Normalize(validInput(), "us-east-1")
	tests := []struct {
		name     string
		upgrades []UpgradeInput
		changes  []string
		errs     []string
	}{
		{name: "minor", upgrades: []UpgradeInput{{"orders_queue", "1.4.0"}}, changes: []string{"orders_queue:sqs:1.3.0->1.4.0:minor"}},
		{name: "one major", upgrades: []UpgradeInput{{"orders_queue", "2.2.0"}}, changes: []string{"orders_queue:sqs:1.3.0->2.2.0:major"}},
		{name: "two resources, sorted", upgrades: []UpgradeInput{{"orders_queue", "1.3.1"}, {"order_processor", "2.0.2"}},
			changes: []string{"order_processor:lambda:2.0.1->2.0.2:patch", "orders_queue:sqs:1.3.0->1.3.1:patch"}},
		{name: "two majors", upgrades: []UpgradeInput{{"orders_queue", "3.0.0"}}, errs: []string{"/upgrades/0/to_version:major_jump_too_large"}},
		{name: "downgrade", upgrades: []UpgradeInput{{"orders_queue", "1.2.0"}}, errs: []string{"/upgrades/0/to_version:not_an_upgrade"}},
		{name: "same version", upgrades: []UpgradeInput{{"orders_queue", "1.3.0"}}, errs: []string{"/upgrades/0/to_version:not_an_upgrade"}},
		{name: "unknown resource", upgrades: []UpgradeInput{{"nope", "1.4.0"}}, errs: []string{"/upgrades/0/resource:unknown_resource"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, changes, errs := PlanUpgrade(current, tt.upgrades)
			got := make([]string, 0, len(changes))
			for _, c := range changes {
				got = append(got, c.Resource+":"+c.Module+":"+c.FromVersion+"->"+c.ToVersion+":"+string(c.Change))
			}
			if !slices.Equal(got, tt.changes) && (len(got) > 0 || len(tt.changes) > 0) {
				t.Fatalf("changes = %v, want %v", got, tt.changes)
			}
			if p := pointers(errs); !slices.Equal(p, tt.errs) && (len(p) > 0 || len(tt.errs) > 0) {
				t.Fatalf("errs = %v, want %v", p, tt.errs)
			}
			if len(next) != len(current.Resources) {
				t.Fatalf("an upgrade keeps every resource: %v", next)
			}
			for i, r := range next {
				if r.Name != current.Resources[i].Name || string(r.Inputs) != string(current.Resources[i].Inputs) {
					t.Fatalf("an upgrade keeps names and inputs: %+v", r)
				}
			}
		})
	}
	if current.Resources[1].Version != "1.3.0" {
		t.Fatal("PlanUpgrade must not modify the current spec")
	}
}

func TestUpgradeFieldErrors(t *testing.T) {
	next := []ResourceInput{{Name: "order_processor"}, {Name: "orders_queue"}}
	ups := []UpgradeInput{{Resource: "orders_queue", ToVersion: "2.0.0"}}
	in := []apperr.FieldError{
		{Pointer: "/resources/1/version", Code: "module_version_unavailable", Message: "yanked"},
		{Pointer: "/resources/1/inputs", Code: "required", Message: "missing dlq_name"},
		{Pointer: "/resources/0/inputs/env/QUEUE_URL", Code: "invalid_ref", Message: "no output queue_url"},
		{Pointer: "/resources/0/version", Code: "module_version_unavailable", Message: "the pinned version is yanked"},
		{Pointer: "/other", Code: "x", Message: "kept"},
	}
	got := upgradeFieldErrors(in, next, ups)
	want := []apperr.FieldError{
		{Pointer: "/upgrades/0/to_version", Code: "module_version_unavailable", Message: "yanked"},
		{Pointer: "/upgrades/0/inputs", Code: "required", Message: "missing dlq_name"},
		{Pointer: "/upgrades", Code: "invalid_ref", Message: `resource "order_processor"/inputs/env/QUEUE_URL: no output queue_url`},
		{Pointer: "/other", Code: "x", Message: "kept"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for errs, code := range map[string]string{
		"/upgrades/0/to_version:major_jump_too_large":                                     "major_jump_too_large",
		"/upgrades/0/inputs:required":                                                     "upgrade_requires_input_changes",
		"/upgrades/0/to_version:not_an_upgrade,/upgrades/1/resource:unknown_resource":     "validation_failed",
		"/upgrades/0/to_version:module_version_unavailable,/upgrades/1/inputs/x:required": "upgrade_requires_input_changes",
	} {
		var fields []apperr.FieldError
		for _, e := range strings.Split(errs, ",") {
			p, c, _ := strings.Cut(e, ":")
			fields = append(fields, apperr.FieldError{Pointer: p, Code: c})
		}
		if got := upgradeErrorCode(fields); got != code {
			t.Errorf("%s: code %s, want %s", errs, got, code)
		}
	}
}

func TestUpgradeRequest(t *testing.T) {
	repo, svc, created := existingStackFixture(t, StackActive)
	ctx := context.Background()
	in := stackInput(upgradeInput("1.4.0"), created.Stack.ID)

	res, err := svc.Create(ctx, dev, createCmd(in, "key-upgrade-0001"))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Request
	if r.Kind != KindUpgrade || r.Status != StatusDraft || r.Stack.ID != created.Stack.ID || r.FencingToken == nil ||
		r.Justification != "pick up the fixes" {
		t.Fatalf("request = %+v", r)
	}
	var spec NormalizedSpec
	if err := json.Unmarshal(r.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	var prev NormalizedSpec
	_ = json.Unmarshal(created.Spec, &prev)
	if len(spec.Resources) != 2 || spec.Resources[1].Name != "orders_queue" || spec.Resources[1].Version != "1.4.0" ||
		string(spec.Resources[1].Inputs) != string(prev.Resources[1].Inputs) || spec.Resources[0].Version != "2.0.1" ||
		len(spec.Upgrades) != 1 || spec.Upgrades[0] != (UpgradeChange{Resource: "orders_queue", Module: "sqs",
		FromVersion: "1.3.0", ToVersion: "1.4.0", Change: catalog.ChangeMinor}) {
		t.Fatalf("spec = %s", r.Spec)
	}
	ev := repo.state.events[len(repo.state.events)-1]
	if ev.Action != "request.created" || !strings.Contains(string(ev.Payload), `"kind":"upgrade"`) ||
		!strings.Contains(string(ev.Payload), `"upgrades":[{"resource":"orders_queue","module":"sqs","from_version":"1.3.0","to_version":"1.4.0","change":"minor"}]`) {
		t.Fatalf("audit = %s", ev.Payload)
	}
	again, err := svc.Create(ctx, dev, createCmd(in, "key-upgrade-0001"))
	if err != nil || !again.Replayed || again.Request.ID != r.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}

	// The upgrade is the stack's newest spec, so the next upgrade starts from 1.4.0.
	next := stackInput(upgradeInput("1.4.0"), created.Stack.ID)
	if _, err := svc.Create(ctx, dev, createCmd(next, "key-upgrade-0002")); !hasCode(err, "not_an_upgrade") {
		t.Fatalf("second upgrade to the same version: %v", err)
	}
	if _, ok := repo.state.keys[key(dev.UserID, "key-upgrade-0002")]; ok {
		t.Fatal("probing a refused upgrade's key must not keep it")
	}
	// The used key with another body is still a reuse, even though the precheck fails first.
	other := stackInput(upgradeInput("1.3.1"), created.Stack.ID)
	if _, err := svc.Create(ctx, dev, createCmd(other, "key-upgrade-0001")); !hasCode(err, "idempotency_key_reuse") {
		t.Fatalf("key reuse: %v", err)
	}
}

func TestUpgradeChecks(t *testing.T) {
	tests := []struct {
		name     string
		in       func() CreateInput
		wantCode string
		wantPtr  string
	}{
		{name: "major without acknowledgement", in: func() CreateInput { return upgradeInput("2.2.0") },
			wantCode: "acknowledge_breaking_required", wantPtr: "/acknowledge_breaking:required"},
		{name: "two majors at once", in: func() CreateInput { c := upgradeInput("3.0.0"); c.AcknowledgeBreaking = true; return c },
			wantCode: "major_jump_too_large", wantPtr: "/upgrades/0/to_version:major_jump_too_large"},
		{name: "yanked target", in: func() CreateInput { return upgradeInput("1.5.0") },
			wantCode: "module_version_unavailable", wantPtr: "/upgrades/0/to_version:module_version_unavailable"},
		{name: "unknown target", in: func() CreateInput { return upgradeInput("1.9.0") },
			wantCode: "module_version_unavailable", wantPtr: "/upgrades/0/to_version:module_version_unavailable"},
		{name: "inputs no longer validate", in: func() CreateInput { c := upgradeInput("2.0.0"); c.AcknowledgeBreaking = true; return c },
			wantCode: "upgrade_requires_input_changes", wantPtr: "/upgrades/0/inputs:required"},
		{name: "a removed output breaks a reference", in: func() CreateInput { c := upgradeInput("2.1.0"); c.AcknowledgeBreaking = true; return c },
			wantCode: "invalid_ref", wantPtr: "/upgrades:invalid_ref"},
		{name: "unknown resource", in: func() CreateInput { c := upgradeInput("1.4.0"); c.Upgrades[0].Resource = "nope"; return c },
			wantCode: "unknown_resource", wantPtr: "/upgrades/0/resource:unknown_resource"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, svc, created := existingStackFixture(t, StackActive)
			before := len(repo.state.requests)
			_, err := svc.Create(context.Background(), dev, createCmd(stackInput(tt.in(), created.Stack.ID), "key-upgrade-check"))
			e, ok := apperr.As(err)
			if !ok || e.Code != tt.wantCode || !slices.Contains(pointers(e.Fields), tt.wantPtr) {
				t.Fatalf("err = %v (%+v)", err, e)
			}
			if len(repo.state.requests) != before {
				t.Fatal("a refused upgrade must not record a request")
			}
		})
	}

	// With the acknowledgement, a compatible major passes.
	_, svc, created := existingStackFixture(t, StackActive)
	in := stackInput(upgradeInput("2.2.0"), created.Stack.ID)
	in.AcknowledgeBreaking = true
	res, err := svc.Create(context.Background(), dev, createCmd(in, "key-upgrade-major"))
	if err != nil || !strings.Contains(string(res.Request.Spec), `"change":"major"`) {
		t.Fatalf("acknowledged major: %v", err)
	}
}

func TestUpgradeNeedsAStackThatTakesIt(t *testing.T) {
	for status, code := range map[StackStatus]string{StackProvisioning: "stack_busy", StackDestroyed: "stack_destroyed"} {
		_, svc, created := existingStackFixture(t, status)
		_, err := svc.Create(context.Background(), dev, createCmd(stackInput(upgradeInput("1.4.0"), created.Stack.ID), "key-upgrade-state"))
		if !hasCode(err, code) {
			t.Errorf("%s: %v", status, err)
		}
	}
	// No live spec: the only create failed.
	_, svc, created := existingStackFixture(t, StackFailed)
	created.Status = StatusFailed
	_, err := svc.Create(context.Background(), dev, createCmd(stackInput(upgradeInput("1.4.0"), created.Stack.ID), "key-upgrade-nospec"))
	if !hasCode(err, "no_current_spec") {
		t.Fatalf("no spec: %v", err)
	}
	// A viewer may not upgrade; an outsider does not see the stack.
	repo, _, created := existingStackFixture(t, StackActive)
	viewer := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleViewer})
	if _, err := viewer.Create(context.Background(), dev, createCmd(stackInput(upgradeInput("1.4.0"), created.Stack.ID), "key-upgrade-viewer")); !apperr.IsKind(err, apperr.KindForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	outsider := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{})
	if _, err := outsider.Create(context.Background(), dev, createCmd(stackInput(upgradeInput("1.4.0"), created.Stack.ID), "key-upgrade-outsider")); !hasCode(err, "stack_not_found") {
		t.Fatalf("outsider: %v", err)
	}
}

// The spec an upgrade was validated against must still be the newest one when it is
// recorded; otherwise the upgrade would overwrite a request created in between.
func TestUpgradeRefusesASpecThatChangedMeanwhile(t *testing.T) {
	_, _, created := existingStackFixture(t, StackActive)
	plan := &upgradePlan{base: json.RawMessage(`{"resources":[]}`)}
	st := &memStore{s: memState{requests: []*Request{created}}}
	err := checkUpgradeBase(context.Background(), st, created.Stack.ID, plan)
	if !hasCode(err, "spec_changed") || !apperr.IsKind(err, apperr.KindConflict) {
		t.Fatalf("err = %v", err)
	}
	plan.base = created.Spec
	if err := checkUpgradeBase(context.Background(), st, created.Stack.ID, plan); err != nil {
		t.Fatalf("unchanged spec: %v", err)
	}
	if err := checkUpgradeBase(context.Background(), st, created.Stack.ID, nil); err == nil {
		t.Fatal("an unprepared upgrade must fail")
	}
}
