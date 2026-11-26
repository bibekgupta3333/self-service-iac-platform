package drift_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/drift"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Two users resolve two different open reports of one stack at the same moment. Both
// resolutions lock the target and the stack's open reports in id order, so they serialize:
// exactly one wins and resolves every report it locked, the other sees the result, no
// deadlock (40P01) reaches a caller, and at most one reconcile request is opened.
func TestConcurrentResolvesOfSiblingReportsSerialize(t *testing.T) {
	for round := range 5 {
		app, owner := dbtest.NewDatabaseWithOwner(t)
		ctx := context.Background()
		var userID, teamID, stackID, reqID uuid.UUID
		if err := owner.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		if err := owner.QueryRow(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
			VALUES ('payments', 'Payments', 7, 'payments', 'cc-1') RETURNING id`).Scan(&teamID); err != nil {
			t.Fatal(err)
		}
		if err := owner.QueryRow(ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, status, created_by)
			SELECT $1, id, 'order-events', $3, 'development/payments/order-events/terraform.tfstate', 'us-east-1', 'active', $2
			FROM environments WHERE name = 'development' RETURNING id`, teamID, userID, stackPath).Scan(&stackID); err != nil {
			t.Fatal(err)
		}
		if err := owner.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
			VALUES ('req-7k3p9x', $1, 'update', '{}', $2) RETURNING id`, stackID, userID).Scan(&reqID); err != nil {
			t.Fatal(err)
		}

		clock := platform.SystemClock{}
		repo := drift.NewPostgresRepository(app, audit.NewWriter(clock, platform.RandomIDs{}))
		rec := &stubReconciler{reqID: reqID}
		svc := drift.NewService(repo, visibility{identity.Visibility{TeamIDs: []uuid.UUID{teamID}}}, roles{rbac.RoleMaintainer}, rec, clock, platform.RandomIDs{})
		user := identity.Principal{UserID: userID, Login: "priya-s", Type: identity.ActorUser}

		first, err := svc.Record(ctx, driftBot(100), report(100, drift.ResultDrifted))
		if err != nil {
			t.Fatal(err)
		}
		other := report(101, drift.ResultDrifted)
		other.PlanHash = strings.Repeat("d", 64)
		second, err := svc.Record(ctx, driftBot(101), other)
		if err != nil {
			t.Fatal(err)
		}
		if first.Entry.ID == second.Entry.ID {
			t.Fatal("two different findings must be two reports")
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		inputs := []struct {
			id uuid.UUID
			in drift.ResolveInput
		}{
			{first.Entry.ID, drift.ResolveInput{Action: drift.ActionReconcile}},
			{second.Entry.ID, drift.ResolveInput{Action: drift.ActionCodify}},
		}
		for i := range inputs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = svc.Resolve(ctx, user, inputs[i].id, inputs[i].in)
			}()
		}
		wg.Wait()

		wins := 0
		for i, err := range errs {
			switch {
			case err == nil:
				wins++
			case hasCode(err, "drift_already_resolved"):
			default:
				t.Fatalf("round %d: resolve %d failed with %v (a deadlock or an unexpected error)", round, i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d resolutions won, want exactly one", round, wins)
		}
		if rec.calls > 1 {
			t.Fatalf("round %d: %d reconcile requests opened, want at most one", round, rec.calls)
		}
		var open int
		var statuses int
		if err := owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'open'), count(DISTINCT status) FROM drift_reports`).Scan(&open, &statuses); err != nil {
			t.Fatal(err)
		}
		if open != 0 || statuses != 1 {
			t.Fatalf("round %d: %d reports still open, %d distinct statuses; the winner resolves everything it locked with one status", round, open, statuses)
		}

		// A report recorded after the lock was taken is not swept up by an earlier decision.
		late := report(102, drift.ResultDrifted)
		late.PlanHash = strings.Repeat("f", 64)
		res, err := svc.Record(ctx, driftBot(102), late)
		if err != nil || res.Entry.Status == nil || *res.Entry.Status != drift.StatusOpen {
			t.Fatalf("round %d: later finding = %+v, %v", round, res.Entry, err)
		}
	}
}
