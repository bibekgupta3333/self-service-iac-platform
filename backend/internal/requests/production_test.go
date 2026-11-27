package requests

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// allow_stateful_destroy is accepted on the kinds that can delete resources only.
func TestValidateShapeStatefulDestroy(t *testing.T) {
	create := validInput()
	create.AllowStatefulDestroy = true
	if got := pointers(ValidateShape(create)); !slices.Contains(got, "/allow_stateful_destroy:not_allowed") {
		t.Errorf("create with allow_stateful_destroy: %v", got)
	}
	for _, in := range []CreateInput{updateInput(), destroyInput()} {
		in.AllowStatefulDestroy = true
		if got := pointers(ValidateShape(in)); len(got) != 0 {
			t.Errorf("%s with allow_stateful_destroy: %v", in.Kind, got)
		}
	}
}

func TestNormalizeKeepsStatefulDestroy(t *testing.T) {
	in := destroyInput()
	in.AllowStatefulDestroy = true
	n := NormalizeForStack(in, StackTarget{TeamSlug: "payments", Environment: "production", Name: "order-events", Region: "us-east-1"})
	b, err := json.Marshal(n)
	if err != nil || !n.AllowStatefulDestroy {
		t.Fatalf("normalized = %+v, %v", n, err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil || back["allow_stateful_destroy"] != true {
		t.Fatalf("stored spec = %s", b)
	}
	// Absent unless set, so specs stored before WBS 8.8 keep their exact bytes.
	plain, _ := json.Marshal(NormalizeForStack(destroyInput(), StackTarget{Environment: "production"}))
	var m map[string]any
	_ = json.Unmarshal(plain, &m)
	if _, ok := m["allow_stateful_destroy"]; ok {
		t.Fatalf("allow_stateful_destroy must be omitted when false: %s", plain)
	}
}

// The production gates on top of the role threshold: a maintainer for allow_stateful_destroy
// in production and a recent sign-in for a production destroy.
func TestAuthorizeProductionGates(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	fresh, stale := now.Add(-2*time.Minute), now.Add(-time.Hour)
	stateful := func(in CreateInput) CreateInput { in.AllowStatefulDestroy = true; return in }

	tests := []struct {
		name   string
		role   Role
		in     CreateInput
		env    string
		authAt time.Time
		code   string // "" = allowed
	}{
		{"production destroy, fresh sign-in", RoleMaintainer, destroyInput(), "production", fresh, ""},
		{"production destroy, stale sign-in", RoleMaintainer, destroyInput(), "production", stale, rbac.ReasonReauth},
		{"production destroy, no sign-in time", RoleAdmin, destroyInput(), "production", time.Time{}, rbac.ReasonReauth},
		{"unknown environment destroy counts as production", RoleAdmin, destroyInput(), "qa", stale, rbac.ReasonReauth},
		{"staging destroy, stale sign-in", RoleDeveloper, destroyInput(), "staging", stale, ""},
		{"production update, stale sign-in", RoleDeveloper, updateInput(), "production", stale, ""},
		{"production stateful update by a developer", RoleDeveloper, stateful(updateInput()), "production", fresh, "forbidden"},
		{"production stateful update by a maintainer", RoleMaintainer, stateful(updateInput()), "production", stale, ""},
		{"staging stateful update by a developer", RoleDeveloper, stateful(updateInput()), "staging", stale, ""},
		{"production stateful destroy, stale sign-in", RoleMaintainer, stateful(destroyInput()), "production", stale, rbac.ReasonReauth},
	}
	for _, tt := range tests {
		err := authorizeProductionGates(tt.role, tt.in, tt.env, tt.authAt, now)
		if tt.code == "" {
			if err != nil {
				t.Errorf("%s: err = %v", tt.name, err)
			}
			continue
		}
		e, ok := apperr.As(err)
		if !ok || e.Kind != apperr.KindForbidden || e.Code != tt.code {
			t.Errorf("%s: err = %v, want 403 %s", tt.name, err, tt.code)
		}
	}
}

// ResolveApplyTarget hands the pre-apply gate what it needs: the environment, the author,
// the pull request and allow_stateful_destroy (read from the stored spec, fail closed).
func TestResolveApplyTargetProductionFields(t *testing.T) {
	h := holder(StatusApproved, func(h *StackHolder) {
		h.StackPath = "environments/production/payments/order-events"
		h.RequesterLogin = "ada"
		h.Request.PR = &PRLink{Number: 812}
		h.Request.Spec = json.RawMessage(`{"team":"payments","allow_stateful_destroy":true}`)
	})
	got, err := ResolveApplyTarget(h, "", t0)
	if err != nil || got.Environment != "production" || got.RequestedBy != "ada" || got.PRNumber != 812 || !got.AllowStatefulDestroy {
		t.Fatalf("target = %+v, %v", got, err)
	}
	for _, spec := range []string{`{"team":"payments"}`, `not json`, ``, `{"allow_stateful_destroy":"yes"}`} {
		h.Request.Spec = json.RawMessage(spec)
		if got, _ := ResolveApplyTarget(h, "", t0); got.AllowStatefulDestroy {
			t.Errorf("spec %q allows a stateful destroy", spec)
		}
	}
	h.Request.PR = nil
	if got, _ := ResolveApplyTarget(h, "", t0); got.PRNumber != 0 {
		t.Errorf("no PR recorded: pr_number = %d", got.PRNumber)
	}
}

func TestCheckApplyCaller(t *testing.T) {
	wf := func(env string) *identity.Workflow { return &identity.Workflow{Environment: env} }
	tests := []struct {
		env  string
		wf   *identity.Workflow
		code string
	}{
		{"production", wf("production"), ""},
		{"production", wf(""), "production_environment_required"},
		{"production", wf("staging"), "production_environment_required"},
		{"production", nil, "oidc_required"},
		{"qa", wf("qa"), "production_environment_required"}, // unknown environment: production rules
		{"", wf("production"), ""},
		{"staging", wf("staging"), ""},
		{"staging", wf(""), ""},
		{"staging", nil, ""},
		{"staging", wf("production"), "environment_mismatch"},
		{"development", wf("staging"), "environment_mismatch"},
	}
	for _, tt := range tests {
		err := CheckApplyCaller(tt.env, tt.wf)
		if tt.code == "" {
			if err != nil {
				t.Errorf("%q / %+v: %v", tt.env, tt.wf, err)
			}
			continue
		}
		if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindForbidden || e.Code != tt.code {
			t.Errorf("%q / %+v: err = %v, want %s", tt.env, tt.wf, err, tt.code)
		}
	}
}
