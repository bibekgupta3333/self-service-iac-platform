package rbac

import (
	"testing"
	"time"
)

// prodGrid is the production column of the docs/07 matrix for the actions that change
// infrastructure or approve a change, written out by hand (viewer, developer, maintainer,
// admin). TestMatrixExhaustive covers every action; this table is the production review
// checklist (WBS 8.6 production gates) and fails loudly if a threshold is relaxed.
var prodGrid = map[Action]string{
	ActionRequestCreate:    "-yyy", // create, update, upgrade
	ActionRequestDestroy:   "--yy", // destroy: maintainer
	ActionStatefulDestroy:  "--yy", // allow_stateful_destroy: maintainer
	ActionApprove:          "--yy", // approve PR / Environment: maintainer, never own work below admin
	ActionRetry:            "--yy", // retry, rollback
	ActionRequestCancelAny: "--yy",
	ActionDriftIgnore:      "--yy",
	ActionBudgetOverride:   "---y", // an override never waives a production budget (budget.rego)
	ActionLockForceRelease: "---y",
}

func TestProductionThresholds(t *testing.T) {
	for _, env := range []string{EnvProduction, "", "prod", "sandbox"} {
		for a, row := range prodGrid {
			for i, role := range Roles {
				want := row[i] == 'y'
				if got := Allowed(role, a, Resource{Environment: env}); got != want {
					t.Errorf("Allowed(%s, %s, %q) = %v, want %v", role, a, env, got, want)
				}
			}
		}
	}
}

func TestProductionIsStricterThanStaging(t *testing.T) {
	for _, a := range Actions {
		prod, _ := MinRole(a, EnvProduction)
		staging, _ := MinRole(a, EnvStaging)
		if !prod.AtLeast(staging) {
			t.Errorf("%s: production needs %s, below staging's %s", a, prod, staging)
		}
	}
}

func TestIsProduction(t *testing.T) {
	for env, want := range map[string]bool{
		EnvDevelopment: false, EnvStaging: false, EnvProduction: true, "": true, "Production": true, "dev": true,
	} {
		if got := IsProduction(env); got != want {
			t.Errorf("IsProduction(%q) = %v, want %v", env, got, want)
		}
	}
}

func TestNeedsFreshAuth(t *testing.T) {
	for _, a := range Actions {
		for _, env := range []string{EnvDevelopment, EnvStaging, EnvProduction, "unknown"} {
			want := a == ActionRequestDestroy && (env == EnvProduction || env == "unknown")
			if got := NeedsFreshAuth(a, env); got != want {
				t.Errorf("NeedsFreshAuth(%s, %s) = %v, want %v", a, env, got, want)
			}
		}
	}
}

func TestFreshAuth(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		authAt time.Time
		want   bool
	}{
		{"just signed in", now, true},
		{"at the window edge", now.Add(-ReauthWindow), true},
		{"past the window", now.Add(-ReauthWindow - time.Second), false},
		{"unknown sign-in time", time.Time{}, false},
		{"slightly ahead (skew)", now.Add(30 * time.Second), true},
		{"far in the future", now.Add(2 * time.Minute), false},
	}
	for _, tt := range tests {
		if got := FreshAuth(tt.authAt, now, ReauthWindow); got != tt.want {
			t.Errorf("%s: FreshAuth = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDecideFresh(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-time.Hour)
	prod := Resource{Environment: EnvProduction}

	// A production destroy by a maintainer with a stale session asks for a new sign-in.
	d := DecideFresh(RoleMaintainer, ActionRequestDestroy, prod, stale, now)
	if d.Allowed || d.Reason != ReasonReauth || d.Need != RoleMaintainer {
		t.Fatalf("stale production destroy = %+v", d)
	}
	// Admins are not exempt.
	if d := DecideFresh(RoleAdmin, ActionRequestDestroy, prod, time.Time{}, now); d.Reason != ReasonReauth {
		t.Fatalf("admin without a sign-in time = %+v", d)
	}
	// Fresh: allowed.
	if d := DecideFresh(RoleMaintainer, ActionRequestDestroy, prod, now.Add(-time.Minute), now); !d.Allowed {
		t.Fatalf("fresh production destroy = %+v", d)
	}
	// The role check wins over the sign-in check.
	if d := DecideFresh(RoleDeveloper, ActionRequestDestroy, prod, stale, now); d.Reason != ReasonRole {
		t.Fatalf("developer production destroy = %+v", d)
	}
	// Outside production and for other actions the sign-in time does not matter.
	if d := DecideFresh(RoleDeveloper, ActionRequestDestroy, Resource{Environment: EnvStaging}, stale, now); !d.Allowed {
		t.Fatalf("staging destroy = %+v", d)
	}
	if d := DecideFresh(RoleDeveloper, ActionRequestCreate, prod, time.Time{}, now); !d.Allowed {
		t.Fatalf("production update = %+v", d)
	}
}
