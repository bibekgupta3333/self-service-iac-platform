package stacks

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// The fixed user of dev auth mode is a platform admin on the principal, with no row in
// memberships (and, in a fresh database, no admin flag in users either). It must see the
// same teams on /v1/requests, /v1/stacks and /v1/requests/{id}/events, and a plain user
// outside the team must see none of them on any of the three.
func TestOneCallerSeesTheSameTeamsOnEveryRoute(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()

	newUser := func(gh int64, login string) identity.Principal {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES ($1, $2) RETURNING id`, gh, login).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return identity.Principal{UserID: id, Login: login, Type: identity.ActorUser}
	}
	dev, outsider := newUser(1, "dev"), newUser(2, "nobody")
	dev.PlatformAdmin = true // dev auth mode: the flag lives on the principal, not in users

	var stackID, requestID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH t AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		           VALUES ('payments', 'Payments', 7, 'payments', 'cc') RETURNING id),
		     s AS (INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, created_by)
		           SELECT t.id, e.id, 'orders', 'environments/development/payments/orders',
		                  'development/payments/orders/terraform.tfstate', 'us-east-1', $1
		           FROM t, environments e WHERE e.name::text = 'development' RETURNING id),
		     r AS (INSERT INTO requests (short_id, stack_id, kind, status, spec, created_by)
		           SELECT 'req-devmode1', s.id, 'create', 'draft', '{}', $1 FROM s RETURNING id)
		SELECT s.id, r.id FROM s, r`, dev.UserID).Scan(&stackID, &requestID); err != nil {
		t.Fatal(err)
	}

	users := identity.NewUserStore(pool)
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	repo := requests.NewPostgresRepository(pool, writer)
	access := requests.NewAccess(pool, users)
	reqSvc := requests.NewService(repo, nil, repo, nil, platform.SystemClock{}, platform.RandomIDs{}).WithAccess(access)
	stackSvc := NewService(NewPostgresRepository(pool), users)

	// routes reports what the caller sees on each of the three routes.
	routes := func(p identity.Principal) (requestsListed, stacksListed int, stackOK, eventsOK bool) {
		t.Helper()
		v, err := access.Visible(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		all, ids := v.SQLArgs()
		reqs, _, err := reqSvc.List(ctx, requests.ListFilter{AllTeams: all, TeamIDs: ids, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		sts, _, err := stackSvc.List(ctx, p, Query{})
		if err != nil {
			t.Fatal(err)
		}
		_, stackErr := stackSvc.Get(ctx, p, stackID)
		_, eventsErr := reqSvc.StatusSnapshot(ctx, p, requestID)
		if stackErr != nil && !apperr.IsKind(stackErr, apperr.KindNotFound) || eventsErr != nil && !apperr.IsKind(eventsErr, apperr.KindNotFound) {
			t.Fatalf("a hidden resource must be not found: %v / %v", stackErr, eventsErr)
		}
		return len(reqs), len(sts), stackErr == nil, eventsErr == nil
	}

	if r, s, stackOK, eventsOK := routes(dev); r != 1 || s != 1 || !stackOK || !eventsOK {
		t.Fatalf("the dev-mode admin sees requests=%d stacks=%d stack detail=%v events=%v, want all of it", r, s, stackOK, eventsOK)
	}
	if r, s, stackOK, eventsOK := routes(outsider); r != 0 || s != 0 || stackOK || eventsOK {
		t.Fatalf("an outsider sees requests=%d stacks=%d stack detail=%v events=%v, want none", r, s, stackOK, eventsOK)
	}

	// A member sees exactly their team, on all three.
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (team_id, user_id, role) SELECT id, $1, 'viewer' FROM teams WHERE slug = 'payments'`,
		outsider.UserID); err != nil {
		t.Fatal(err)
	}
	if r, s, stackOK, eventsOK := routes(outsider); r != 1 || s != 1 || !stackOK || !eventsOK {
		t.Fatalf("a viewer sees requests=%d stacks=%d stack detail=%v events=%v, want all of it", r, s, stackOK, eventsOK)
	}
}
