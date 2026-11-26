package teamsync

import (
	"testing"

	"github.com/google/uuid"
)

func team(slug string) Team { return Team{ID: uuid.New(), Slug: slug, GitHubSlug: slug} }

func TestCollisions(t *testing.T) {
	a, b := team("payments"), team("checkout")
	if got := Collisions([]Team{a, b}, "iacp-admins"); len(got) != 0 {
		t.Fatalf("distinct teams must not collide: %v", got)
	}

	// "payments-maintainers" as a team would turn its developers into maintainers of payments.
	m := team("payments-maintainers")
	got := Collisions([]Team{a, m}, "iacp-admins")
	if _, ok := got[a.ID]; !ok {
		t.Fatalf("payments must be flagged: %v", got)
	}
	if _, ok := got[m.ID]; !ok {
		t.Fatalf("payments-maintainers must be flagged: %v", got)
	}
	if _, ok := got[b.ID]; ok {
		t.Fatal("an unrelated team must not be flagged")
	}

	// A team named like the admin team would hand platform admin to its developers.
	adm := team("iacp-admins")
	if _, ok := Collisions([]Team{adm}, "iacp-admins")[adm.ID]; !ok {
		t.Fatal("a team colliding with the admin team must be flagged")
	}
	// Case-insensitive.
	if len(Collisions([]Team{team("Payments"), team("payments")}, "")) != 2 {
		t.Fatal("slugs compare case-insensitively")
	}
	// No GitHub slug.
	nameless := Team{ID: uuid.New(), Slug: "x"}
	if _, ok := Collisions([]Team{nameless}, "")[nameless.ID]; !ok {
		t.Fatal("a team without a github slug must be flagged")
	}
}

func TestWithoutCollisionsFeedsDesiredRoles(t *testing.T) {
	a, m := team("payments"), team("payments-maintainers")
	teams := []Team{a, m}
	kept := WithoutCollisions(teams, Collisions(teams, "iacp-admins"))
	if len(kept) != 0 {
		t.Fatalf("both colliding teams are dropped: %v", kept)
	}
	if got := DesiredRoles(kept, []string{"payments-maintainers"}); len(got) != 0 {
		t.Fatalf("a colliding team must receive no roles: %v", got)
	}
}
