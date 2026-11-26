package drift_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/drift"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

const stackPath = "environments/development/payments/order-events"

type stubReconciler struct {
	mu    sync.Mutex
	calls int
	reqID uuid.UUID
}

func (s *stubReconciler) ReconcileStack(context.Context, identity.Principal, uuid.UUID, string, string) (drift.Reconciled, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return drift.Reconciled{RequestID: s.reqID, ShortID: "req-7k3p9x", Status: "draft"}, nil
}

type roles struct{ role rbac.Role }

func (r roles) RoleInTeam(context.Context, string, uuid.UUID) (rbac.Role, error) { return r.role, nil }

type visibility struct{ v identity.Visibility }

func (v visibility) VisibleTeams(context.Context, identity.Principal) (identity.Visibility, error) {
	return v.v, nil
}

func driftBot(runID int64) identity.Principal {
	wf := &identity.Workflow{Ref: identity.MainRef, EventName: "schedule", RunID: runID, RunAttempt: 1,
		SHA: strings.Repeat("ab", 20), JobWorkflowRef: "acme/iacp/.github/workflows/drift.yml@" + identity.MainRef}
	return identity.Principal{Login: wf.JobWorkflowRef, Type: identity.ActorGitHubActions, Workflow: wf}
}

func report(runID int64, result drift.Result) drift.Report {
	r := drift.Report{StackPath: stackPath, GitHub: drift.GitHubRun{RunID: runID, RunAttempt: 1, HeadSHA: strings.Repeat("ab", 20)},
		Result: result}
	switch result {
	case drift.ResultDrifted:
		r.TerraformExitCode = drift.ExitChanges
		r.Summary = drift.Summary{Drifted: 2, Updated: 1, Deleted: 1}
		r.PlanHash = strings.Repeat("c", 64)
		r.Changed = []drift.ChangedResource{{Address: "aws_sqs_queue.this", Type: "aws_sqs_queue", Actions: []string{"delete"}}}
	case drift.ResultError:
		r.TerraformExitCode, r.Error = drift.ExitError, "terraform init failed"
	}
	return r
}

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}

func TestPostgresDriftLifecycle(t *testing.T) {
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

	health := func() (status, health string, checked bool) {
		if err := owner.QueryRow(ctx, `SELECT status::text, health::text, last_drift_check_at IS NOT NULL FROM stacks WHERE id = $1`,
			stackID).Scan(&status, &health, &checked); err != nil {
			t.Fatal(err)
		}
		return
	}

	// The stack is listed for the scan until a request holds its lock.
	due, err := svc.DueStacks(ctx, 0)
	if err != nil || len(due) != 1 || due[0].Path != stackPath || due[0].Team != "payments" || due[0].Environment != "development" {
		t.Fatalf("due = %+v, %v", due, err)
	}

	// Drift: a report, health drifted, lifecycle untouched.
	first, err := svc.Record(ctx, driftBot(900), report(900, drift.ResultDrifted))
	if err != nil || first.Replayed || first.Entry.Status == nil || *first.Entry.Status != drift.StatusOpen || first.Entry.Summary.Deleted != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if st, h, checked := health(); st != "active" || h != "drifted" || !checked {
		t.Fatalf("stack = %s/%s checked=%v: drift moves health, never the lifecycle", st, h, checked)
	}
	// The same run reported again (retried HTTP call) changes nothing.
	again, err := svc.Record(ctx, driftBot(900), report(900, drift.ResultDrifted))
	if err != nil || !again.Replayed || again.Entry.ID != first.Entry.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	var reports, runsN int
	if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM drift_reports), (SELECT count(*) FROM pipeline_runs WHERE kind = 'drift')`).Scan(&reports, &runsN); err != nil || reports != 1 || runsN != 1 {
		t.Fatalf("rows = %d reports, %d runs, %v", reports, runsN, err)
	}
	// A run id that belongs to another pipeline is a conflict.
	if _, err := owner.Exec(ctx, `INSERT INTO pipeline_runs (stack_id, kind, github_run_id, workflow_ref, head_sha)
		VALUES ($1, 'apply', 777, 'x', $2)`, stackID, strings.Repeat("ab", 40)[:40]); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, driftBot(777), report(777, drift.ResultDrifted)); !hasCode(err, "run_kind_mismatch") {
		t.Fatalf("run reported as another pipeline: %v", err)
	}

	// History is scoped to visible teams.
	rows, err := svc.List(ctx, user, stackID, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != first.Entry.ID || rows[0].Team != "payments" {
		t.Fatalf("list = %+v, %v", rows, err)
	}
	other := drift.NewService(repo, visibility{identity.Visibility{TeamIDs: []uuid.UUID{uuid.New()}}}, roles{rbac.RoleAdmin}, rec, clock, platform.RandomIDs{})
	if _, err := other.List(ctx, user, stackID, 0); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("hidden stack: %v", err)
	}
	if _, err := other.Resolve(ctx, user, first.Entry.ID, drift.ResolveInput{Action: drift.ActionReconcile}); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("hidden report: %v", err)
	}

	// Two concurrent identical resolves open one request and write one resolution.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Resolve(ctx, user, first.Entry.ID, drift.ResolveInput{Action: drift.ActionReconcile})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent resolve %d: %v", i, err)
		}
	}
	var status string
	var by, reqRef uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT status::text, resolved_by, resolution_request_id FROM drift_reports WHERE id = $1`, first.Entry.ID).
		Scan(&status, &by, &reqRef); err != nil || status != "reconciled" || by != userID || reqRef != reqID {
		t.Fatalf("report = %s by=%s req=%s, %v", status, by, reqRef, err)
	}
	var resolvedEvents int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'drift.resolved' AND subject_id = $1`, stackID.String()).Scan(&resolvedEvents); err != nil || resolvedEvents != 1 {
		t.Fatalf("drift.resolved events = %d, %v", resolvedEvents, err)
	}
	if _, err := svc.Resolve(ctx, user, first.Entry.ID, drift.ResolveInput{Action: drift.ActionIgnore, Reason: "too late to ignore"}); !hasCode(err, "drift_already_resolved") {
		t.Fatalf("ignore after reconcile: %v", err)
	}

	// A clean scan restores health and closes whatever is still open.
	second, err := svc.Record(ctx, driftBot(901), report(901, drift.ResultDrifted))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, driftBot(902), report(902, drift.ResultError)); err != nil {
		t.Fatal(err)
	}
	if _, h, _ := health(); h != "drifted" {
		t.Fatalf("health after a failed check = %s, want drifted", h)
	}
	if _, err := svc.Record(ctx, driftBot(903), report(903, drift.ResultClean)); err != nil {
		t.Fatal(err)
	}
	if st, h, _ := health(); st != "active" || h != "healthy" {
		t.Fatalf("stack after a clean scan = %s/%s", st, h)
	}
	if err := owner.QueryRow(ctx, `SELECT status::text, resolved_by IS NULL FROM drift_reports WHERE id = $1`, second.Entry.ID).Scan(&status, new(bool)); err != nil || status != "reconciled" {
		t.Fatalf("open report after a clean scan = %s, %v", status, err)
	}

	// Ignore keeps the stack drifted and needs a justification, also at the schema level.
	if _, err := svc.Record(ctx, driftBot(904), report(904, drift.ResultDrifted)); err != nil {
		t.Fatal(err)
	}
	third, err := svc.List(ctx, user, stackID, 1)
	if err != nil || len(third) != 1 {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, user, third[0].ID, drift.ResolveInput{Action: drift.ActionIgnore, Reason: "cost team tags this by hand"}); err != nil {
		t.Fatal(err)
	}
	if _, h, _ := health(); h != "drifted" {
		t.Fatalf("health after ignore = %s", h)
	}
	if _, err := owner.Exec(ctx, `UPDATE drift_reports SET status = 'ignored', resolved_at = now(), resolution_reason = ' ' WHERE id = $1`, second.Entry.ID); err == nil {
		t.Fatal("the schema accepted an ignored report without a justification")
	}

	// A stack that is mid-change takes no report and is not listed for scanning.
	if _, err := owner.Exec(ctx, `UPDATE stacks SET status = 'updating' WHERE id = $1`, stackID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, driftBot(905), report(905, drift.ResultDrifted)); !hasCode(err, "stack_not_checkable") {
		t.Fatalf("report for an updating stack: %v", err)
	}
	if due, err := svc.DueStacks(ctx, 0); err != nil || len(due) != 0 {
		t.Fatalf("due while updating = %+v, %v", due, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE stacks SET status = 'active' WHERE id = $1`, stackID); err != nil {
		t.Fatal(err)
	}
	// A stack whose lock a request holds is skipped too.
	if _, err := owner.Exec(ctx, `INSERT INTO locks (stack_id, request_id, holder, fencing_token, acquired_at, expires_at)
		VALUES ($1, $2, 'req-7k3p9x', 1, now(), now() + interval '1 hour')`, stackID, reqID); err != nil {
		t.Fatal(err)
	}
	if due, err := svc.DueStacks(ctx, 0); err != nil || len(due) != 0 {
		t.Fatalf("due while locked = %+v, %v", due, err)
	}
}

func TestDriftReportsAreLeastPrivilege(t *testing.T) {
	app, _ := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	for _, stmt := range []string{
		"DELETE FROM drift_reports",
		"TRUNCATE drift_reports",
		"UPDATE drift_reports SET plan_hash = repeat('f', 64)",
		"UPDATE drift_reports SET result = 'clean'",
		"UPDATE drift_reports SET resources_drifted = 0",
		"UPDATE drift_reports SET changed = '[]'",
		"UPDATE drift_reports SET detected_at = now()",
	} {
		if _, err := app.Exec(ctx, stmt); err == nil {
			t.Errorf("iacp_app could run %q", stmt)
		}
	}
	// The resolution columns are the only writable ones.
	for _, stmt := range []string{
		"UPDATE drift_reports SET status = status",
		"UPDATE drift_reports SET resolved_by = NULL, resolved_at = NULL, resolution_reason = NULL, resolution_request_id = NULL",
		"SELECT * FROM drift_reports",
	} {
		if _, err := app.Exec(ctx, stmt); err != nil {
			t.Errorf("iacp_app cannot run %q: %v", stmt, err)
		}
	}
}
