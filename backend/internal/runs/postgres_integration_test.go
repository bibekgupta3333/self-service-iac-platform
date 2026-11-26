package runs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

const stackPath = "environments/development/payments/order-events"

func TestPostgresRecordRun(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()

	var userID, teamID, stackID, reqID uuid.UUID
	for _, q := range []struct {
		sql  string
		dest *uuid.UUID
		args []any
	}{
		{`INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`, &userID, nil},
		{`INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center) VALUES ('payments', 'Payments', 7, 'payments', 'cc-1') RETURNING id`, &teamID, nil},
	} {
		if err := pool.QueryRow(ctx, q.sql, q.args...).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, created_by)
		SELECT $1, id, 'order-events', $3, 'development/payments/order-events/terraform.tfstate', 'us-east-1', $2
		FROM environments WHERE name = 'development' RETURNING id`, teamID, userID, stackPath).Scan(&stackID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
		VALUES ('req-7k3p9x', $1, 'create', '{}', $2) RETURNING id`, stackID, userID).Scan(&reqID); err != nil {
		t.Fatal(err)
	}

	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, time.Hour)
	grant, err := lockSvc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: reqID})
	if err != nil {
		t.Fatal(err)
	}
	token := grant.Lock.FencingToken

	svc := runs.NewService(runs.NewPostgresRepository(pool), platform.SystemClock{})
	prRef := "refs/pull/812/merge"
	bot := identity.Principal{Login: "acme/iacp/.github/workflows/pr-validate.yml@" + prRef, Type: identity.ActorGitHubActions,
		Workflow: &identity.Workflow{Ref: prRef, JobWorkflowRef: "acme/iacp/.github/workflows/pr-validate.yml@" + prRef,
			RunID: 11873399021, RunAttempt: 1, SHA: strings.Repeat("e4", 20), EventName: "pull_request"}}
	report := func(stage string, status runs.StageStatus, tok int64) runs.Report {
		return runs.Report{Pipeline: runs.KindValidate, StackPath: stackPath, RequestShortID: "req-7k3p9x", FencingToken: &tok,
			Stage: stage, Status: status, Plan: json.RawMessage(`{"hash":"3f9a"}`),
			GitHub: runs.GitHubRun{RunID: 11873399021, RunAttempt: 1, JobID: 33412988710, HeadSHA: strings.Repeat("e4", 20)}}
	}

	first, err := svc.Record(ctx, bot, report("plan", runs.StagePassed, token))
	if err != nil || first.RunStatus != runs.StatusInProgress || first.RequestStatus == nil || *first.RequestStatus != "draft" {
		t.Fatalf("first report = %+v, %v", first, err)
	}
	done, err := svc.Record(ctx, bot, report("summary", runs.StagePassed, token))
	if err != nil || done.RunID != first.RunID || done.RunStatus != runs.StatusSucceeded {
		t.Fatalf("summary report = %+v, %v", done, err)
	}
	// A late stage report is kept but cannot reopen a finished run.
	late, err := svc.Record(ctx, bot, report("cleanup", runs.StageInProgress, token))
	if err != nil || late.RunID != first.RunID || late.RunStatus != runs.StatusSucceeded {
		t.Fatalf("late report = %+v, %v", late, err)
	}
	var (
		stages     []map[string]any
		jobID      int64
		workflow   string
		finished   bool
		requestRef uuid.UUID
	)
	if err := pool.QueryRow(ctx, `SELECT stages, github_job_id, workflow_ref, finished_at IS NOT NULL, request_id FROM pipeline_runs WHERE id = $1`,
		first.RunID).Scan(&stages, &jobID, &workflow, &finished, &requestRef); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 3 || stages[0]["name"] != "plan" || stages[1]["name"] != "summary" || jobID != 33412988710 ||
		workflow != bot.Login || !finished || requestRef != reqID {
		t.Fatalf("run row: stages=%v job=%d workflow=%s finished=%v request=%s", stages, jobID, workflow, finished, requestRef)
	}

	// The lock moves on: the old token is stale.
	if _, err := lockSvc.Release(ctx, locks.TokenCommand{StackID: stackID, RequestID: reqID, FencingToken: token}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, bot, report("plan", runs.StagePassed, token)); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("report after release: %v", err)
	}

	unknown := report("plan", runs.StagePassed, token)
	unknown.StackPath = "environments/development/payments/nope"
	if _, err := svc.Record(ctx, bot, unknown); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("unknown stack: %v", err)
	}

	// Drift reports need no lock; the same run reported as another pipeline conflicts.
	driftBot := identity.Principal{Login: "acme/iacp/.github/workflows/drift.yml@refs/heads/main", Type: identity.ActorGitHubActions}
	drift := runs.Report{Pipeline: runs.KindDrift, StackPath: stackPath, Stage: "summary", Status: runs.StagePassed,
		GitHub: runs.GitHubRun{RunID: 900, RunAttempt: 1, HeadSHA: strings.Repeat("ab", 20)}}
	if res, err := svc.Record(ctx, driftBot, drift); err != nil || res.RunStatus != runs.StatusSucceeded || res.RequestStatus != nil {
		t.Fatalf("drift report = %+v, %v", res, err)
	}
	again, err := lockSvc.Acquire(ctx, locks.AcquireCommand{StackID: stackID, RequestID: reqID})
	if err != nil || again.Lock.FencingToken <= token {
		t.Fatalf("re-acquire = %+v, %v", again, err)
	}
	drift.Pipeline, drift.FencingToken, drift.RequestShortID = runs.KindApply, &again.Lock.FencingToken, "req-7k3p9x"
	if _, err := svc.Record(ctx, driftBot, drift); !hasCode(err, "run_kind_mismatch") {
		t.Fatalf("re-reporting a run as another pipeline: %v", err)
	}
}

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}
