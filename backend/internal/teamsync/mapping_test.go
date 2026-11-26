package teamsync

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

var (
	payments = Team{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Slug: "payments", GitHubSlug: "payments"}
	checkout = Team{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), Slug: "checkout", GitHubSlug: "checkout-eng"}
	teams    = []Team{payments, checkout}
)

func TestDesiredRoles(t *testing.T) {
	tests := []struct {
		name  string
		slugs []string
		want  map[uuid.UUID]rbac.Role
	}{
		{"no teams", nil, map[uuid.UUID]rbac.Role{}},
		{"developer", []string{"payments"}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleDeveloper}},
		{"viewer", []string{"iacp-payments-viewers"}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleViewer}},
		{"maintainer", []string{"payments-maintainers"}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleMaintainer}},
		{"highest role wins", []string{"iacp-payments-viewers", "payments", "payments-maintainers"},
			map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleMaintainer}},
		{"developer beats viewer", []string{"payments", "iacp-payments-viewers"},
			map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleDeveloper}},
		{"uses the github slug, not the platform slug", []string{"checkout", "checkout-eng-maintainers"},
			map[uuid.UUID]rbac.Role{checkout.ID: rbac.RoleMaintainer}},
		{"case-insensitive", []string{"Payments-Maintainers"}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleMaintainer}},
		{"two teams", []string{"payments", "iacp-checkout-eng-viewers"},
			map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleDeveloper, checkout.ID: rbac.RoleViewer}},
		{"unrelated and lookalike teams grant nothing", []string{"payments-ops", "iacp-payments", "xpayments", "iacp-admins"},
			map[uuid.UUID]rbac.Role{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DesiredRoles(teams, tt.slugs)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for id, r := range tt.want {
				if got[id] != r {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
	if got := DesiredRoles([]Team{{ID: uuid.New(), Slug: "x"}}, []string{"", "-maintainers"}); len(got) != 0 {
		t.Fatalf("a team without a github slug must never match: %v", got)
	}
}

func TestIsAdmin(t *testing.T) {
	m := Mapping{AdminTeam: "iacp-admins"}
	if !m.IsAdmin([]string{"payments", "IACP-Admins"}) || m.IsAdmin([]string{"payments", "iacp-admin"}) || m.IsAdmin(nil) {
		t.Fatal("IsAdmin mismatch")
	}
	if (Mapping{}).IsAdmin([]string{""}) {
		t.Fatal("an unset admin team must grant nothing")
	}
}

func TestPlan(t *testing.T) {
	sync := func(id uuid.UUID, r rbac.Role) Membership { return Membership{TeamID: id, Role: r, Source: SourceSync} }
	manual := func(id uuid.UUID, r rbac.Role) Membership {
		return Membership{TeamID: id, Role: r, Source: SourceManual}
	}
	tests := []struct {
		name    string
		current []Membership
		desired map[uuid.UUID]rbac.Role
		want    []Change
	}{
		{"nothing to do", []Membership{sync(payments.ID, rbac.RoleDeveloper)}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleDeveloper}, nil},
		{"add", nil, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleViewer},
			[]Change{{payments.ID, rbac.RoleNone, rbac.RoleViewer}}},
		{"promote", []Membership{sync(payments.ID, rbac.RoleDeveloper)}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleMaintainer},
			[]Change{{payments.ID, rbac.RoleDeveloper, rbac.RoleMaintainer}}},
		{"demote", []Membership{sync(payments.ID, rbac.RoleMaintainer)}, map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleViewer},
			[]Change{{payments.ID, rbac.RoleMaintainer, rbac.RoleViewer}}},
		{"remove when the user left the github team", []Membership{sync(payments.ID, rbac.RoleDeveloper)}, nil,
			[]Change{{payments.ID, rbac.RoleDeveloper, rbac.RoleNone}}},
		{"manual rows are never removed", []Membership{manual(payments.ID, rbac.RoleAdmin)}, nil, nil},
		{"manual rows are never changed", []Membership{manual(payments.ID, rbac.RoleAdmin)},
			map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleViewer}, nil},
		{"mixed and ordered", []Membership{sync(checkout.ID, rbac.RoleViewer)},
			map[uuid.UUID]rbac.Role{payments.ID: rbac.RoleDeveloper},
			[]Change{{payments.ID, rbac.RoleNone, rbac.RoleDeveloper}, {checkout.ID, rbac.RoleViewer, rbac.RoleNone}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Plan(tt.current, tt.desired)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Plan = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconcileAdmin(t *testing.T) {
	tests := []struct {
		name      string
		has, want bool
		others    int
		wantD     AdminDecision
	}{
		{"unchanged admin", true, true, 0, AdminDecision{Admin: true}},
		{"unchanged non-admin", false, false, 0, AdminDecision{}},
		{"grant", false, true, 0, AdminDecision{Admin: true, Changed: true}},
		{"revoke while others remain", true, false, 1, AdminDecision{Admin: false, Changed: true}},
		{"never revoke the last admin", true, false, 0, AdminDecision{Admin: true, Blocked: true}},
	}
	for _, tt := range tests {
		if got := ReconcileAdmin(tt.has, tt.want, tt.others); got != tt.wantD {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.wantD)
		}
	}
}
