package teamsync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/teamsync"
)

type env struct {
	pool   *pgxpool.Pool
	syncer *teamsync.Syncer
	teams  map[string]uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	clock, ids := platform.SystemClock{}, platform.RandomIDs{}
	e := &env{pool: pool, teams: map[string]uuid.UUID{},
		syncer: teamsync.NewSyncer(db.NewTxRunner(pool), audit.NewWriter(clock, ids), clock, teamsync.Mapping{AdminTeam: "iacp-admins"})}
	for i, slug := range []string{"payments", "checkout"} {
		var id uuid.UUID
		err := pool.QueryRow(context.Background(), `
			INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
			VALUES ($1, $1, $2, $1, 'cc-1') RETURNING id`, slug, 100+i).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		e.teams[slug] = id
	}
	return e
}

func (e *env) roles(t *testing.T, user uuid.UUID) map[string]string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT t.slug, m.role::text FROM memberships m JOIN teams t ON t.id = m.team_id WHERE m.user_id = $1`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var slug, role string
		if err := rows.Scan(&slug, &role); err != nil {
			t.Fatal(err)
		}
		out[slug] = role
	}
	return out
}

func (e *env) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) isAdmin(t *testing.T, user uuid.UUID) bool {
	t.Helper()
	var a bool
	if err := e.pool.QueryRow(context.Background(), `SELECT is_platform_admin FROM users WHERE id = $1`, user).Scan(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func gh(id int64, login string, slugs ...string) identity.GitHubUser {
	return identity.GitHubUser{ID: id, Login: login, Name: login, Email: login + "@example.test", OrgMember: true, TeamSlugs: slugs}
}

func TestSyncLoginCreatesUserAndMemberships(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, err := e.syncer.SyncLogin(ctx, gh(1001, "priya", "payments", "iacp-checkout-viewers", "unrelated"))
	if err != nil {
		t.Fatal(err)
	}
	if res.UserID == uuid.Nil || res.Login != "priya" || res.PlatformAdmin {
		t.Fatalf("result = %+v", res)
	}
	got := e.roles(t, res.UserID)
	if len(got) != 2 || got["payments"] != "developer" || got["checkout"] != "viewer" {
		t.Fatalf("memberships = %v", got)
	}
	if n := e.auditCount(t, "rbac.membership_changed"); n != 2 {
		t.Fatalf("membership audit events = %d, want 2", n)
	}

	// A second login with no change is a no-op for memberships and audit.
	if _, err := e.syncer.SyncLogin(ctx, gh(1001, "priya", "payments", "iacp-checkout-viewers")); err != nil {
		t.Fatal(err)
	}
	if n := e.auditCount(t, "rbac.membership_changed"); n != 2 {
		t.Fatalf("an unchanged sync must not audit: %d", n)
	}
}

func TestSyncLoginTracksPromotionsRemovalsAndRenames(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _ := e.syncer.SyncLogin(ctx, gh(1002, "sam", "payments", "checkout"))

	second, err := e.syncer.SyncLogin(ctx, gh(1002, "sam-renamed", "payments-maintainers"))
	if err != nil {
		t.Fatal(err)
	}
	if second.UserID != first.UserID {
		t.Fatal("the user is keyed by the GitHub id, so a rename keeps the same row")
	}
	got := e.roles(t, first.UserID)
	if len(got) != 1 || got["payments"] != "maintainer" {
		t.Fatalf("after promotion and removal: %v", got)
	}
	var login string
	if err := e.pool.QueryRow(ctx, `SELECT github_login::text FROM users WHERE id = $1`, first.UserID).Scan(&login); err != nil || login != "sam-renamed" {
		t.Fatalf("login = %q, %v", login, err)
	}
}

func TestSyncLoginLeavesManualMembershipsAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, _ := e.syncer.SyncLogin(ctx, gh(1003, "lee"))
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (team_id, user_id, role, source) VALUES ($1, $2, 'maintainer', 'manual')`,
		e.teams["payments"], res.UserID); err != nil {
		t.Fatal(err)
	}
	// GitHub says developer, and says nothing about checkout; the manual row must survive.
	if _, err := e.syncer.SyncLogin(ctx, gh(1003, "lee", "payments")); err != nil {
		t.Fatal(err)
	}
	if got := e.roles(t, res.UserID); got["payments"] != "maintainer" {
		t.Fatalf("manual membership was changed: %v", got)
	}
	if _, err := e.syncer.SyncLogin(ctx, gh(1003, "lee")); err != nil {
		t.Fatal(err)
	}
	if got := e.roles(t, res.UserID); got["payments"] != "maintainer" {
		t.Fatalf("manual membership was removed: %v", got)
	}
}

func TestSyncLoginAdminGrantAndLastAdminProtection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	a, err := e.syncer.SyncLogin(ctx, gh(2001, "root-a", "iacp-admins"))
	if err != nil || !a.PlatformAdmin || !e.isAdmin(t, a.UserID) {
		t.Fatalf("admin grant: %+v, %v", a, err)
	}
	if n := e.auditCount(t, "rbac.platform_admin_changed"); n != 1 {
		t.Fatalf("admin change events = %d", n)
	}

	// The only admin leaves the admin team: the sync must not strip them.
	a2, err := e.syncer.SyncLogin(ctx, gh(2001, "root-a"))
	if err != nil || !a2.PlatformAdmin || !e.isAdmin(t, a.UserID) {
		t.Fatalf("last admin was downgraded: %+v, %v", a2, err)
	}
	if n := e.auditCount(t, "rbac.admin_downgrade_blocked"); n != 1 {
		t.Fatalf("blocked events = %d", n)
	}

	// With a second admin, the first can be downgraded.
	b, _ := e.syncer.SyncLogin(ctx, gh(2002, "root-b", "iacp-admins"))
	if !b.PlatformAdmin {
		t.Fatal("second admin not granted")
	}
	a3, err := e.syncer.SyncLogin(ctx, gh(2001, "root-a"))
	if err != nil || a3.PlatformAdmin || e.isAdmin(t, a.UserID) {
		t.Fatalf("admin should have been downgraded: %+v, %v", a3, err)
	}
	// ...and now root-b is the last admin.
	b2, _ := e.syncer.SyncLogin(ctx, gh(2002, "root-b"))
	if !b2.PlatformAdmin {
		t.Fatal("the new last admin was downgraded")
	}
}

func TestSyncLoginConcurrentLastTwoAdminsNeitherLoses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, _ := e.syncer.SyncLogin(ctx, gh(2101, "a", "iacp-admins"))
	b, _ := e.syncer.SyncLogin(ctx, gh(2102, "b", "iacp-admins"))

	errs := make(chan error, 2)
	for _, u := range []identity.GitHubUser{gh(2101, "a"), gh(2102, "b")} {
		go func() { _, err := e.syncer.SyncLogin(ctx, u); errs <- err }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var admins int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_platform_admin AND id IN ($1, $2)`, a.UserID, b.UserID).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins < 1 {
		t.Fatal("concurrent downgrades removed every platform admin")
	}
}

func TestSyncLoginRejectsDisabledUsersAndRollsBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, _ := e.syncer.SyncLogin(ctx, gh(3001, "gone", "payments"))
	if _, err := e.pool.Exec(ctx, `UPDATE users SET disabled_at = $2 WHERE id = $1`, res.UserID, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := e.roles(t, res.UserID)
	if _, err := e.syncer.SyncLogin(ctx, gh(3001, "gone", "payments", "checkout")); !errors.Is(err, oauth.ErrUserDisabled) {
		t.Fatalf("err = %v, want ErrUserDisabled", err)
	}
	if after := e.roles(t, res.UserID); len(after) != len(before) {
		t.Fatalf("a disabled user's memberships changed: %v -> %v", before, after)
	}
}

func TestSyncLoginRejectsMalformedUsers(t *testing.T) {
	e := newEnv(t)
	for _, u := range []identity.GitHubUser{gh(0, "x"), gh(-5, "x"), gh(5, "")} {
		if _, err := e.syncer.SyncLogin(context.Background(), u); err == nil {
			t.Errorf("SyncLogin(%+v) should fail", u)
		}
	}
}

func TestSyncLoginLoginCollisionWithDevUserFails(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := identity.NewUserStore(e.pool).EnsureDevUser(ctx, "local-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.syncer.SyncLogin(ctx, gh(4001, "local-dev")); err == nil {
		t.Fatal("a GitHub user must not be able to take over another account's login")
	}
}
