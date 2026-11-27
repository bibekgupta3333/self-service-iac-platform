package rbac

import (
	"slices"
	"testing"
)

// grid is the docs/07 matrix written out by hand, independently of MinRole. Each string
// lists, for viewer, developer, maintainer, admin in that order, whether the action is
// allowed ('y') or denied ('-'), for development, staging and production.
var grid = map[Action]struct{ dev, staging, prod string }{
	ActionRead:              {"yyyy", "yyyy", "yyyy"},
	ActionRequestCreate:     {"-yyy", "-yyy", "-yyy"},
	ActionRequestDestroy:    {"-yyy", "-yyy", "--yy"},
	ActionStatefulDestroy:   {"-yyy", "-yyy", "--yy"},
	ActionRequestCancelOwn:  {"-yyy", "-yyy", "-yyy"},
	ActionRequestCancelAny:  {"--yy", "--yy", "--yy"},
	ActionApprove:           {"-yyy", "-yyy", "--yy"},
	ActionRetry:             {"--yy", "--yy", "--yy"},
	ActionTTLExtend:         {"-yyy", "-yyy", "-yyy"},
	ActionDriftReconcile:    {"-yyy", "-yyy", "-yyy"},
	ActionDriftIgnore:       {"--yy", "--yy", "--yy"},
	ActionExceptionRequest:  {"--yy", "--yy", "--yy"},
	ActionBudgetSet:         {"---y", "---y", "---y"},
	ActionBudgetOverride:    {"---y", "---y", "---y"},
	ActionAuditViewTeam:     {"--yy", "--yy", "--yy"},
	ActionAuditViewAll:      {"---y", "---y", "---y"},
	ActionModuleDeprecate:   {"---y", "---y", "---y"},
	ActionLockForceRelease:  {"---y", "---y", "---y"},
	ActionEnvironmentManage: {"---y", "---y", "---y"},
}

func TestGridCoversEveryAction(t *testing.T) {
	if len(grid) != len(Actions) {
		t.Fatalf("grid has %d actions, Actions has %d", len(grid), len(Actions))
	}
	for _, a := range Actions {
		if _, ok := grid[a]; !ok {
			t.Errorf("action %q missing from the test grid", a)
		}
	}
}

func TestMatrixExhaustive(t *testing.T) {
	for _, a := range Actions {
		g := grid[a]
		for env, row := range map[string]string{EnvDevelopment: g.dev, EnvStaging: g.staging, EnvProduction: g.prod} {
			for i, role := range Roles {
				want := row[i] == 'y'
				if got := Allowed(role, a, Resource{Environment: env}); got != want {
					t.Errorf("Allowed(%s, %s, %s) = %v, want %v", role, a, env, got, want)
				}
			}
			if Allowed(RoleNone, a, Resource{Environment: env}) {
				t.Errorf("no role must never be allowed %s in %s", a, env)
			}
		}
	}
}

func TestUnknownEnvironmentIsTreatedAsProduction(t *testing.T) {
	for _, env := range []string{"", "prod", "Production", "sandbox"} {
		if Allowed(RoleDeveloper, ActionRequestDestroy, Resource{Environment: env}) {
			t.Errorf("developer destroy in unknown env %q must be denied", env)
		}
		if !Allowed(RoleMaintainer, ActionRequestDestroy, Resource{Environment: env}) {
			t.Errorf("maintainer destroy in unknown env %q should be allowed", env)
		}
	}
}

func TestUnknownActionAndRoleAreDenied(t *testing.T) {
	d := Decide(RoleAdmin, Action("nuke"), Resource{Environment: EnvDevelopment})
	if d.Allowed || d.Reason != ReasonNoAction {
		t.Fatalf("unknown action: %+v", d)
	}
	for _, r := range []Role{"root", "Admin", "owner"} {
		if Allowed(r, ActionRead, Resource{}) {
			t.Errorf("unknown role %q must be denied", r)
		}
	}
}

func TestSelfApproval(t *testing.T) {
	for _, env := range []string{EnvDevelopment, EnvStaging, EnvProduction} {
		for _, role := range []Role{RoleDeveloper, RoleMaintainer} {
			d := Decide(role, ActionApprove, Resource{Environment: env, OwnedBySubject: true})
			if d.Allowed {
				t.Errorf("%s approving own work in %s must be denied", role, env)
			}
		}
		if !Allowed(RoleAdmin, ActionApprove, Resource{Environment: env, OwnedBySubject: true}) {
			t.Errorf("admin approving in %s should be allowed", env)
		}
	}
	d := Decide(RoleMaintainer, ActionApprove, Resource{Environment: EnvProduction, OwnedBySubject: true})
	if d.Reason != ReasonSelf || d.Need != RoleMaintainer {
		t.Fatalf("self approval decision = %+v", d)
	}
}

func TestDecisionNeed(t *testing.T) {
	d := Decide(RoleDeveloper, ActionRequestDestroy, Resource{Environment: EnvProduction})
	if d.Allowed || d.Need != RoleMaintainer || d.Reason != ReasonRole {
		t.Fatalf("decision = %+v", d)
	}
}

func TestRoleOrderingAndMax(t *testing.T) {
	for i, lo := range Roles {
		for j, hi := range Roles {
			if got := hi.AtLeast(lo); got != (j >= i) {
				t.Errorf("%s.AtLeast(%s) = %v", hi, lo, got)
			}
			want := Roles[max(i, j)]
			if got := Max(lo, hi); got != want {
				t.Errorf("Max(%s,%s) = %s, want %s", lo, hi, got, want)
			}
		}
	}
	if Max(RoleNone, RoleViewer) != RoleViewer || Max(RoleViewer, "bogus") != RoleViewer || Max("x", "y") != RoleNone {
		t.Fatal("Max must prefer valid roles")
	}
	if RoleNone.AtLeast(RoleViewer) || Role("x").AtLeast(RoleNone) {
		t.Fatal("unknown roles grant nothing")
	}
}

func TestPermissions(t *testing.T) {
	got := Permissions(RoleViewer, EnvProduction)
	if !slices.Equal(got, []Action{ActionRead}) {
		t.Fatalf("viewer permissions = %v", got)
	}
	if n := len(Permissions(RoleAdmin, EnvProduction)); n != len(Actions) {
		t.Fatalf("admin has %d of %d permissions", n, len(Actions))
	}
	if slices.Contains(Permissions(RoleDeveloper, EnvProduction), ActionRequestDestroy) {
		t.Fatal("developers cannot destroy in production")
	}
}
