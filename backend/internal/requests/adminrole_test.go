package requests

import (
	"context"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// The principal's platform-admin flag decides the role, not the membership table, so the
// dev-mode user (an admin for route policies, a stranger in the database) can create,
// update and destroy exactly what the visibility helper lets it see.
func TestPlatformAdminPrincipalActsAsTeamAdmin(t *testing.T) {
	admin := dev
	admin.PlatformAdmin = true
	t.Run("create", func(t *testing.T) {
		svc := newSvcWithMembers(newMemRepo(), fakeVersions{}, fakeMembers{admin.UserID: RoleNone})
		if _, err := svc.Create(context.Background(), admin, createCmd(validInput(), "key-admin-0001")); err != nil {
			t.Fatalf("an admin with no membership must be able to create: %v", err)
		}
		if _, err := svc.Create(context.Background(), dev, createCmd(validInput(), "key-admin-0002")); !apperr.IsKind(err, apperr.KindForbidden) {
			t.Fatalf("the same user without the flag is refused: %v", err)
		}
	})
	t.Run("update and destroy", func(t *testing.T) {
		repo, _, created := existingStackFixture(t, StackActive)
		svc := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{admin.UserID: RoleNone})
		for i, in := range []CreateInput{updateInput(), destroyInput()} {
			key := "key-admin-stack-" + string(rune('a'+i))
			if _, err := svc.Create(context.Background(), admin, createCmd(stackInput(in, created.Stack.ID), key)); err != nil {
				t.Fatalf("admin %s: %v", in.Kind, err)
			}
			// Without the flag the stack stays hidden, as before.
			if _, err := svc.Create(context.Background(), dev, createCmd(stackInput(in, created.Stack.ID), "plain-"+key)); !hasCode(err, "stack_not_found") {
				t.Fatalf("plain %s: %v", in.Kind, err)
			}
		}
	})
}

func TestEffectiveRole(t *testing.T) {
	admin := identity.Principal{PlatformAdmin: true}
	for _, stored := range []Role{RoleNone, RoleViewer, RoleDeveloper, RoleAdmin} {
		if got := admin.EffectiveRole(stored); got != RoleAdmin {
			t.Errorf("platform admin with stored %q = %q", stored, got)
		}
		if got := (identity.Principal{}).EffectiveRole(stored); got != stored {
			t.Errorf("plain user with stored %q = %q", stored, got)
		}
	}
}
