package teamsync_test

import (
	"context"
	"testing"
)

// Platform teams "payments" and "payments-maintainers" have overlapping role slugs: neither
// may receive synced roles, and the check reports both.
func TestSyncSkipsTeamsWithAmbiguousSlugs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments-maintainers', 'PM', 900, 'payments-maintainers', 'cc')`); err != nil {
		t.Fatal(err)
	}
	bad, err := e.syncer.CheckTeams(ctx)
	if err != nil || len(bad) != 2 {
		t.Fatalf("CheckTeams = %v, %v", bad, err)
	}
	res, err := e.syncer.SyncLogin(ctx, gh(7001, "eve", "payments-maintainers", "checkout"))
	if err != nil {
		t.Fatal(err)
	}
	got := e.roles(t, res.UserID)
	if len(got) != 1 || got["checkout"] != "developer" {
		t.Fatalf("only the unambiguous team may grant a role: %v", got)
	}
}
