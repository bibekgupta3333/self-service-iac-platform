package identity_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

func TestPrincipalContext(t *testing.T) {
	if _, err := identity.FromContext(context.Background()); err == nil {
		t.Fatal("expected ErrNoPrincipal")
	}
	p := identity.Principal{UserID: uuid.New(), Login: "dev", Type: identity.ActorUser}
	got, err := identity.FromContext(identity.WithPrincipal(context.Background(), p))
	if err != nil || got != p || got.AuditActorID() != p.UserID.String() {
		t.Fatalf("got %+v, %v", got, err)
	}
	wf := identity.Principal{Login: "module-release.yml", Type: identity.ActorGitHubActions}
	if wf.AuditActorID() != "module-release.yml" {
		t.Fatalf("actor id = %q", wf.AuditActorID())
	}
}

func TestWorkflowFileAndOnMain(t *testing.T) {
	const repo = "acme/iacp"
	tests := []struct {
		ref, jobRef string
		file        string
		onMain      bool
	}{
		{"refs/heads/main", repo + "/.github/workflows/apply.yml@refs/heads/main", "apply.yml", true},
		{"refs/pull/1/merge", repo + "/.github/workflows/pr-validate.yml@refs/pull/1/merge", "pr-validate.yml", false},
		{"refs/heads/main", repo + "/.github/workflows/apply.yml@refs/heads/feature", "apply.yml", false},
		{"refs/heads/main", "acme/x/workflows/apply.yml@refs/heads/main", "", true},
		{"", "", "", false},
	}
	for _, tt := range tests {
		w := identity.Workflow{Ref: tt.ref, JobWorkflowRef: tt.jobRef}
		if w.File() != tt.file || w.OnMain() != tt.onMain {
			t.Errorf("%q: File=%q OnMain=%v, want %q %v", tt.jobRef, w.File(), w.OnMain(), tt.file, tt.onMain)
		}
	}
}

func TestDevGitHubUserIDIsNegativeAndStable(t *testing.T) {
	for _, login := range []string{"", "local-dev", "priya-s"} {
		id := identity.DevGitHubUserID(login)
		if id >= 0 || id != identity.DevGitHubUserID(login) {
			t.Errorf("DevGitHubUserID(%q) = %d", login, id)
		}
	}
}

func TestEnsureDevUser(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	store := identity.NewUserStore(pool)
	ctx := context.Background()
	a, err := store.EnsureDevUser(ctx, "local-dev")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.EnsureDevUser(ctx, "local-dev")
	if err != nil || a.UserID != b.UserID || a.Type != identity.ActorUser {
		t.Fatalf("not idempotent: %+v %+v %v", a, b, err)
	}
}
