package stacks

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// seeded users: a member of payments, a member of search, an admin, a disabled member, an outsider.
type cast struct{ payments, search, admin, disabled, outsider identity.Principal }

func TestVisibilityAndFilters(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()

	user := func(gh int64, login string, admin bool, disabled bool) identity.Principal {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login, is_platform_admin, disabled_at)
			VALUES ($1, $2, $3, CASE WHEN $4 THEN now() END) RETURNING id`, gh, login, admin, disabled).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return identity.Principal{UserID: id, Login: login, Type: identity.ActorUser}
	}
	c := cast{user(1, "pay", false, false), user(2, "sea", false, false), user(3, "root", true, false),
		user(4, "gone", false, true), user(5, "nobody", false, false)}

	team := func(slug string, gh int64, member identity.Principal, role string) {
		if _, err := pool.Exec(ctx, `
			WITH t AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
			           VALUES ($1, $1, $2, $1, 'cc') RETURNING id)
			INSERT INTO memberships (team_id, user_id, role) SELECT id, $3, $4::team_role FROM t`, slug, gh, member.UserID, role); err != nil {
			t.Fatal(err)
		}
	}
	team("payments", 10, c.payments, "viewer")
	team("search", 11, c.search, "developer")
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (team_id, user_id, role) SELECT id, $1, 'viewer' FROM teams WHERE slug = 'payments'`,
		c.disabled.UserID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO environments (name, aws_account_id, allowed_regions)
		VALUES ('production', '111111111111', ARRAY['us-east-1'])`); err != nil {
		t.Fatal(err)
	}
	stack := func(teamSlug, env, name, status string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, status, outputs, created_by, created_at)
			SELECT t.id, e.id, $3, 'environments/' || $2 || '/' || $1 || '/' || $3, $2 || '/' || $1 || '/' || $3, 'us-east-1',
			       $4::stack_status, '{"queue_url":"http://q"}', $5, clock_timestamp()
			FROM teams t, environments e WHERE t.slug = $1 AND e.name::text = $2 RETURNING id`,
			teamSlug, env, name, status, c.admin.UserID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pay1 := stack("payments", "development", "orders", "active")
	pay2 := stack("payments", "production", "orders", "failed")
	sea1 := stack("search", "development", "index", "active")
	// Two requests on pay1: the list shows the newer one.
	for _, kind := range []string{"create", "update"} {
		if _, err := pool.Exec(ctx, `INSERT INTO requests (short_id, stack_id, kind, status, spec, created_by, created_at)
			VALUES ('req-' || $2, $1, $2::request_kind, 'draft', '{}', $3, clock_timestamp())`, pay1, kind, c.admin.UserID); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewService(NewPostgresRepository(pool))
	ids := func(p identity.Principal, q Query) []uuid.UUID {
		t.Helper()
		rows, _, err := svc.List(ctx, p, q)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}
	eq := func(got []uuid.UUID, want ...uuid.UUID) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	eq(ids(c.payments, Query{}), pay2, pay1)                 // newest first, only their team
	eq(ids(c.search, Query{}), sea1)                         // another team's stacks are invisible
	eq(ids(c.admin, Query{}), sea1, pay2, pay1)              // platform admins see everything
	eq(ids(c.outsider, Query{}))                             // no membership, no stacks
	eq(ids(c.disabled, Query{}))                             // a disabled user loses access
	eq(ids(c.admin, Query{Team: "payments"}), pay2, pay1)    // filters
	eq(ids(c.admin, Query{Environment: "production"}), pay2) //
	eq(ids(c.admin, Query{Status: requests.StackActive}), sea1, pay1)
	eq(ids(c.payments, Query{Team: "search"})) // a filter never widens visibility

	page, next, err := svc.List(ctx, c.admin, Query{Limit: 2})
	if err != nil || len(page) != 2 || next == nil {
		t.Fatalf("page 1: %d, %+v, %v", len(page), next, err)
	}
	rest, end, err := svc.List(ctx, c.admin, Query{Limit: 2, After: next})
	if err != nil || len(rest) != 1 || end != nil || rest[0].ID != pay1 {
		t.Fatalf("page 2: %+v, %+v, %v", rest, end, err)
	}

	got, err := svc.Get(ctx, c.payments, pay1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Team != "payments" || got.Environment != "development" || got.Status != requests.StackActive ||
		string(got.Outputs) != `{"queue_url": "http://q"}` || got.LatestRequest == nil || got.LatestRequest.Kind != requests.KindUpdate {
		t.Fatalf("stack = %+v (outputs %s)", got, got.Outputs)
	}
	for _, p := range []identity.Principal{c.search, c.outsider, c.disabled} {
		if _, err := svc.Get(ctx, p, pay1); err == nil {
			t.Fatalf("%s must not see payments/orders", p.Login)
		}
	}
	if _, err := svc.Get(ctx, c.admin, uuid.New()); err == nil {
		t.Fatal("unknown id")
	}
	if s, err := svc.Get(ctx, c.admin, sea1); err != nil || s.LatestRequest != nil {
		t.Fatalf("a stack with no request has no latest request: %+v, %v", s, err)
	}
}
