package runs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/stacks"
)

// services wires the real services over pool, as cmd/api does: the run service drives the
// request service inside the report's transaction, and lock promotion schedules publishing.
type services struct {
	lock   *locks.Service
	reqs   *requests.Service
	run    *runs.Service
	writer *audit.Writer
}

func newServices(pool *pgxpool.Pool) services {
	clock := platform.SystemClock{}
	writer := audit.NewWriter(clock, platform.RandomIDs{})
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), clock, time.Hour)
	lockSvc.OnPromote(requests.PublishOnPromote(clock))
	repo := requests.NewPostgresRepository(pool, writer)
	reqSvc := requests.NewService(repo, nil, repo, lockSvc, clock, platform.RandomIDs{})
	driver := requests.NewApplyDriver(reqSvc, writer)
	return services{lock: lockSvc, reqs: reqSvc, run: runs.NewService(runs.NewPostgresRepository(pool, driver), clock), writer: writer}
}

const (
	applyWorkflow = "acme/iacp/.github/workflows/apply.yml@refs/heads/main"
	planHash      = "3f9a1c0b7e2d4f6a8b0c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f708"
)

type applyEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	ctx    context.Context
	svc    services
	teamID uuid.UUID
	userID uuid.UUID
	n      int
}

func newApplyEnv(t *testing.T) *applyEnv {
	t.Helper()
	pool := dbtest.NewDatabase(t)
	e := &applyEnv{t: t, pool: pool, ctx: context.Background(), svc: newServices(pool)}
	e.must(pool.QueryRow(e.ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&e.userID))
	e.must(pool.QueryRow(e.ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments', 'Payments', 7, 'payments', 'cc-1') RETURNING id`).Scan(&e.teamID))
	return e
}

func (e *applyEnv) must2(_ any, err error) {
	e.t.Helper()
	e.must(err)
}

func (e *applyEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

// stack inserts a stack in status and returns its id and path.
func (e *applyEnv) stack(name, status string) (uuid.UUID, string) {
	e.t.Helper()
	path := "environments/development/payments/" + name
	var id uuid.UUID
	e.must(e.pool.QueryRow(e.ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, status, destroyed_at,
			outputs, created_by)
		SELECT $1, id, $2, $3, 'development/payments/' || $2 || '/terraform.tfstate', 'us-east-1', $4::stack_status,
		       CASE WHEN $4 = 'destroyed' THEN now() END, '{"old":"output"}', $5
		FROM environments WHERE name = 'development' RETURNING id`, e.teamID, name, path, status, e.userID).Scan(&id))
	return id, path
}

// request inserts a request. Statuses past plan_ready carry the approved plan hash the
// schema requires; terminal ones are completed.
func (e *applyEnv) request(stackID uuid.UUID, kind, status string) (uuid.UUID, string) {
	e.t.Helper()
	e.n++
	short := fmt.Sprintf("req-%06d", e.n)
	var id uuid.UUID
	e.must(e.pool.QueryRow(e.ctx, `INSERT INTO requests (short_id, stack_id, kind, status, spec, created_by, approved_plan_hash, completed_at,
			created_at, updated_at)
		VALUES ($1, $2, $3::request_kind, $4::request_status, '{}', $5,
		        CASE WHEN $4 IN ('approved', 'applying', 'applied') THEN $6 END,
		        CASE WHEN $4 IN ('applied', 'failed', 'rejected', 'cancelled') THEN now() END,
		        now() + $7 * interval '1 ms', now() + $7 * interval '1 ms')
		RETURNING id`, short, stackID, kind, status, e.userID, planHash, e.n).Scan(&id))
	return id, short
}

// acquire gives request the stack lock and returns its fencing token.
func (e *applyEnv) acquire(stackID, reqID uuid.UUID) int64 {
	e.t.Helper()
	res, err := e.svc.lock.Acquire(e.ctx, locks.AcquireCommand{StackID: stackID, RequestID: reqID})
	e.must(err)
	if res.Outcome == locks.OutcomeQueued {
		e.t.Fatalf("request %s was queued behind %s", reqID, res.Lock.RequestID)
	}
	return res.Lock.FencingToken
}

// applyRun is one apply.yml run reporting for one request.
type applyRun struct {
	e       *applyEnv
	path    string
	short   string
	token   int64
	runID   int64
	attempt int // 0 means 1
	sha     string
	bot     identity.Principal
	outputs json.RawMessage
}

func (e *applyEnv) run(path, short string, token, runID int64) *applyRun {
	sha := strings.Repeat(fmt.Sprintf("%02x", runID%255), 20)
	return &applyRun{e: e, path: path, short: short, token: token, runID: runID, sha: sha, bot: workflowPrincipal(applyWorkflow, "refs/heads/main", "push", runID, sha)}
}

func workflowPrincipal(jobWorkflowRef, ref, event string, runID int64, sha string) identity.Principal {
	return identity.Principal{Login: jobWorkflowRef, Type: identity.ActorGitHubActions,
		Workflow: &identity.Workflow{Ref: ref, JobWorkflowRef: jobWorkflowRef, RunID: runID, RunAttempt: 1, SHA: sha, EventName: event}}
}

func (a *applyRun) report(stage string, status runs.StageStatus) runs.Report {
	a.bot.Workflow.RunAttempt = a.attemptOrOne()
	tok := a.token
	r := runs.Report{Pipeline: runs.KindApply, StackPath: a.path, RequestShortID: a.short, FencingToken: &tok, Stage: stage, Status: status,
		GitHub: runs.GitHubRun{RunID: a.runID, RunAttempt: a.attemptOrOne(), JobID: 5000 + a.runID, HeadSHA: a.sha}}
	if stage == "summary" && len(a.outputs) > 0 {
		r.Outputs = a.outputs
	}
	if status == runs.StageFailed {
		r.Error = json.RawMessage(`{"class":"permanent","message":"` + stage + ` failed"}`)
	}
	return r
}

// send records a report as the run's workflow.
func (a *applyRun) send(stage string, status runs.StageStatus) (runs.Recorded, error) {
	return a.e.svc.run.Record(a.e.ctx, a.bot, a.report(stage, status))
}

func (a *applyRun) mustSend(stage string, status runs.StageStatus) runs.Recorded {
	a.e.t.Helper()
	res, err := a.send(stage, status)
	if err != nil {
		a.e.t.Fatalf("%s %s: %v", stage, status, err)
	}
	return res
}

func (e *applyEnv) requestRow(id uuid.UUID) (status, reason string, completed bool) {
	e.t.Helper()
	e.must(e.pool.QueryRow(e.ctx, `SELECT status::text, coalesce(failure_reason, ''), completed_at IS NOT NULL FROM requests WHERE id = $1`, id).
		Scan(&status, &reason, &completed))
	return
}

type stackRow struct {
	Status, Health string
	Outputs        string
	SHA            *string
	LastRequest    *uuid.UUID
	Destroyed      bool
}

func (e *applyEnv) stackRow(id uuid.UUID) stackRow {
	e.t.Helper()
	var r stackRow
	var outputs []byte
	e.must(e.pool.QueryRow(e.ctx, `SELECT status::text, health, outputs, last_applied_sha, last_applied_request_id, destroyed_at IS NOT NULL
		FROM stacks WHERE id = $1`, id).Scan(&r.Status, &r.Health, &outputs, &r.SHA, &r.LastRequest, &r.Destroyed))
	r.Outputs = string(outputs)
	return r
}

func (e *applyEnv) lockHolder(stackID uuid.UUID) (uuid.UUID, bool) {
	e.t.Helper()
	var holder uuid.UUID
	err := e.pool.QueryRow(e.ctx, `SELECT request_id FROM locks WHERE stack_id = $1`, stackID).Scan(&holder)
	return holder, err == nil
}

func (e *applyEnv) audits(action string, subject uuid.UUID) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM audit_events WHERE action = $1 AND subject_id = $2`, action, subject.String()).Scan(&n))
	return n
}

func (e *applyEnv) runs(stackID uuid.UUID) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM pipeline_runs WHERE stack_id = $1`, stackID).Scan(&n))
	return n
}

func (e *applyEnv) runStages(runID uuid.UUID) (status string, stages map[string]string) {
	e.t.Helper()
	var raw []map[string]any
	e.must(e.pool.QueryRow(e.ctx, `SELECT status::text, stages FROM pipeline_runs WHERE id = $1`, runID).Scan(&status, &raw))
	stages = map[string]string{}
	for _, s := range raw {
		stages[s["name"].(string)] = s["status"].(string)
	}
	return
}

func (e *applyEnv) publishJobs(reqID uuid.UUID) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM outbox WHERE topic = 'request.publish' AND aggregate_id = $1`, reqID).Scan(&n))
	return n
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if e, ok := apperr.As(err); !ok || e.Code != code {
		t.Fatalf("want error %s, got %v", code, err)
	}
}

func TestApplyReportsDriveACreateToActive(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "provisioning")
	reqID, short := e.request(stackID, "create", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 1001)
	run.outputs = json.RawMessage(`{"queue_url":"http://localhost:4566/q","token":"<sensitive>"}`)

	first := run.mustSend("plan", runs.StagePassed)
	if first.RequestStatus == nil || *first.RequestStatus != "applying" || first.RunStatus != runs.StatusInProgress {
		t.Fatalf("first apply report: %+v", first)
	}
	if st, _, _ := e.requestRow(reqID); st != "applying" || e.stackRow(stackID).Status != "provisioning" {
		t.Fatalf("after the first report: request %s, stack %+v", st, e.stackRow(stackID))
	}
	run.mustSend("plan", runs.StagePassed) // a retried call
	run.mustSend("apply", runs.StagePassed)
	run.mustSend("apply", runs.StagePassed)
	run.mustSend("smoke", runs.StagePassed)
	if _, ok := e.lockHolder(stackID); !ok {
		t.Fatal("the lock must stay held until the run finishes")
	}

	done := run.mustSend("summary", runs.StagePassed)
	if *done.RequestStatus != "applied" || done.RunStatus != runs.StatusSucceeded || done.RunID != first.RunID {
		t.Fatalf("summary: %+v", done)
	}
	st, _, completed := e.requestRow(reqID)
	row := e.stackRow(stackID)
	if st != "applied" || !completed || row.Status != "active" || row.Health != "healthy" || row.Destroyed ||
		row.SHA == nil || *row.SHA != run.sha || row.LastRequest == nil || *row.LastRequest != reqID {
		t.Fatalf("request %s completed=%v, stack %+v", st, completed, row)
	}
	var outputs map[string]string
	if err := json.Unmarshal([]byte(row.Outputs), &outputs); err != nil || outputs["queue_url"] != "http://localhost:4566/q" || outputs["token"] != "<sensitive>" || len(outputs) != 2 {
		t.Fatalf("outputs = %s (%v)", row.Outputs, err)
	}
	if _, held := e.lockHolder(stackID); held {
		t.Fatal("the terminal transition must release the stack lock")
	}
	if n := e.audits("request.transitioned", reqID); n != 2 {
		t.Fatalf("request.transitioned audit events = %d, want apply_started and apply_succeeded", n)
	}
	if n := e.audits("stack.status_changed", stackID); n != 1 {
		t.Fatalf("stack.status_changed audit events = %d", n)
	}
	if runStatus, stages := e.runStages(first.RunID); runStatus != "succeeded" || len(stages) != 4 {
		t.Fatalf("run %s stages %v", runStatus, stages)
	}

	// apply.yml's own release step runs after the API released the lease: still a success.
	rel, err := e.svc.lock.Release(e.ctx, locks.TokenCommand{StackID: stackID, RequestID: reqID, FencingToken: run.token})
	if err != nil || !rel.AlreadyReleased {
		t.Fatalf("the workflow's release after the final transition must be an idempotent success: %+v %v", rel, err)
	}
	// The summary is replayed after the lock was released: still accepted (the token is the
	// request's own), recorded, and changes nothing.
	again, err := run.send("summary", runs.StagePassed)
	if err != nil || *again.RequestStatus != "applied" {
		t.Fatalf("replayed summary: %+v %v", again, err)
	}
	// A late failure for the finished request cannot undo it.
	late, err := run.send("apply", runs.StageFailed)
	if err != nil || *late.RequestStatus != "applied" {
		t.Fatalf("late apply failure: %+v %v", late, err)
	}
	if _, stages := e.runStages(first.RunID); stages["apply"] != "passed" || stages["summary"] != "passed" || len(stages) != 4 {
		t.Fatalf("a late report was merged into the finished run's history: %v", stages)
	}
	// A new run id or attempt carrying the released token is stale: only the run that
	// reported for this request may keep reporting.
	for name, mk := range map[string]func() *applyRun{
		"new run id": func() *applyRun { return e.run(path, short, run.token, 1002) },
		"new attempt": func() *applyRun {
			a := e.run(path, short, run.token, 1001)
			a.attempt = 2
			return a
		},
	} {
		_, err := mk().send("summary", runs.StagePassed)
		if err == nil {
			t.Fatalf("%s: a released token opened a new run", name)
		}
		wantCode(t, err, "stale_fencing_token")
	}
	if n := e.runs(stackID); n != 1 {
		t.Fatalf("pipeline_runs rows = %d, want 1", n)
	}
	if st, _, _ := e.requestRow(reqID); st != "applied" || e.stackRow(stackID).Status != "active" || e.audits("request.transitioned", reqID) != 2 {
		t.Fatalf("a late report changed state: request %s stack %+v", st, e.stackRow(stackID))
	}
	if n := e.audits("request.run_report_ignored", reqID); n != 2 {
		t.Fatalf("ignored reports audited = %d, want 2", n)
	}

	// Another holder's token is still refused, terminal request or not.
	other := e.run(path, short, run.token+999, 1001)
	if _, err := other.send("summary", runs.StagePassed); err == nil {
		t.Fatal("a foreign token was accepted for a terminal request")
	} else {
		wantCode(t, err, "stale_fencing_token")
	}
}

func TestFailedUpdateFailsTheStackAndPromotesTheNextWaiter(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "active")
	reqID, short := e.request(stackID, "update", "approved")
	token := e.acquire(stackID, reqID)
	waiting, _ := e.request(stackID, "update", "draft")
	if res, err := e.svc.lock.Acquire(e.ctx, locks.AcquireCommand{StackID: stackID, RequestID: waiting}); err != nil || res.Outcome != locks.OutcomeQueued {
		t.Fatalf("second request must queue: %+v %v", res, err)
	}
	run := e.run(path, short, token, 2001)

	if res := run.mustSend("plan", runs.StagePassed); *res.RequestStatus != "applying" {
		t.Fatalf("%+v", res)
	}
	res := run.mustSend("plan", runs.StageFailed)
	if res.RunStatus != runs.StatusFailed || *res.RequestStatus != "failed" {
		t.Fatalf("failed plan: %+v", res)
	}
	st, reason, completed := e.requestRow(reqID)
	if st != "failed" || reason != requests.FailurePlanFailed || !completed {
		t.Fatalf("request %s reason %s", st, reason)
	}
	var detail map[string]any
	e.must(e.pool.QueryRow(e.ctx, `SELECT failure_detail FROM requests WHERE id = $1`, reqID).Scan(&detail))
	if detail["stage"] != "plan" {
		t.Fatalf("failure detail = %v", detail)
	}
	if got := e.stackRow(stackID); got.Status != "failed" || got.SHA != nil || got.Outputs != `{"old": "output"}` {
		t.Fatalf("a failed update marks the stack failed and keeps what was applied before: %+v", got)
	}
	holder, held := e.lockHolder(stackID)
	if !held || holder != waiting {
		t.Fatalf("the lock must pass to the next waiter in the same transaction: held=%v by %s", held, holder)
	}
	if e.publishJobs(waiting) != 1 {
		t.Fatalf("the promoted request's publish job was not scheduled: %d", e.publishJobs(waiting))
	}
	if n := e.audits("request.transitioned", reqID); n != 2 {
		t.Fatalf("transitions audited = %d", n)
	}
}

func TestFailedSmokeTestDegradesTheStackWithoutFailingTheRun(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "active")
	reqID, short := e.request(stackID, "update", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 3001)
	run.outputs = json.RawMessage(`{"queue_url":"http://q"}`)

	run.mustSend("plan", runs.StagePassed)
	run.mustSend("apply", runs.StagePassed)
	smoke := run.mustSend("smoke", runs.StageFailed)
	if smoke.RunStatus != runs.StatusInProgress || *smoke.RequestStatus != "applying" {
		t.Fatalf("a failed smoke stage alone must not finish the run or fail the request: %+v", smoke)
	}
	// apply.yml reports the summary from job.status, which the failed smoke step turned red.
	done := run.mustSend("summary", runs.StageFailed)
	if done.RunStatus != runs.StatusSucceeded || *done.RequestStatus != "applied" {
		t.Fatalf("summary of a smoke-only failure: %+v", done)
	}
	if got := e.stackRow(stackID); got.Status != "active" || got.Health != "degraded" || got.Outputs != `{"queue_url": "http://q"}` {
		t.Fatalf("stack = %+v", got)
	}
	runStatus, stages := e.runStages(done.RunID)
	if runStatus != "succeeded" || stages["smoke"] != "failed" || stages["summary"] != "failed" {
		t.Fatalf("the stages keep what each step reported: %s %v", runStatus, stages)
	}

	// A failed release after the verdict is recorded and audited, never flipping the request.
	rel, err := run.send("release", runs.StageFailed)
	if err != nil || rel.RunStatus != runs.StatusSucceeded || *rel.RequestStatus != "applied" {
		t.Fatalf("release report: %+v %v", rel, err)
	}
	if st, _, _ := e.requestRow(reqID); st != "applied" {
		t.Fatalf("a failed release flipped the request to %s", st)
	}
	if n := e.audits("request.lock_release_failed", reqID); n != 1 {
		t.Fatalf("lock_release_failed audit events = %d", n)
	}
	if _, stages := e.runStages(done.RunID); stages["release"] != "failed" {
		t.Fatalf("the release failure must be visible in the run: %v", stages)
	}
}

func TestFailedReleaseWhileTheLockIsStillHeldIsVisibleOnTheStack(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "active")
	reqID, short := e.request(stackID, "update", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 3501)
	run.mustSend("apply", runs.StagePassed)

	// The release failed before the run reported its verdict, so the lease is current.
	res, err := run.send("release", runs.StageFailed)
	if err != nil || res.RunStatus != runs.StatusInProgress || *res.RequestStatus != "applying" {
		t.Fatalf("release report: %+v %v", res, err)
	}
	if st, _, _ := e.requestRow(reqID); st != "applying" {
		t.Fatalf("request = %s", st)
	}
	var held bool
	var shown string
	e.must(e.pool.QueryRow(e.ctx, `SELECT (payload->>'lock_held')::bool, payload->>'held_by_this_request' FROM audit_events
		WHERE action = 'request.lock_release_failed' AND subject_id = $1`, reqID.String()).Scan(&held, &shown))
	if !held || shown != "true" {
		t.Fatalf("the audit payload must say the lock is still held by this request: %v %s", held, shown)
	}
	st, err := stacks.NewPostgresRepository(e.pool).Get(e.ctx, identity.Visibility{All: true}, stackID)
	if err != nil || st.Lock == nil || st.Lock.RequestID != reqID || st.Lock.RequestShortID != short || st.Lock.ExpiresAt.IsZero() {
		t.Fatalf("the stack must show its lock: %+v %v", st.Lock, err)
	}
}

func TestDestroyCompletionMarksTheStackDestroyed(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "active")
	reqID, short := e.request(stackID, "destroy", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 4001)

	run.mustSend("plan", runs.StagePassed)
	run.mustSend("destroy", runs.StageInProgress)
	if got := e.stackRow(stackID); got.Status != "destroying" {
		t.Fatalf("while the destroy runs the stack is destroying: %+v", got)
	}
	run.mustSend("destroy", runs.StagePassed)
	done := run.mustSend("summary", runs.StagePassed)
	if *done.RequestStatus != "applied" {
		t.Fatalf("summary: %+v", done)
	}
	row := e.stackRow(stackID)
	if row.Status != "destroyed" || !row.Destroyed || row.Outputs != `{}` || row.Health != "unknown" {
		t.Fatalf("stack = %+v", row)
	}
	if st, _, completed := e.requestRow(reqID); st != "applied" || !completed {
		t.Fatalf("a successful destroy applies the request: %s", st)
	}
	if e.audits("stack.destroyed", stackID) != 1 {
		t.Fatal("stack.destroyed was not audited")
	}

	// Nothing may target a destroyed stack any more.
	admin := identity.Principal{UserID: e.userID, Login: "priya-s", Type: identity.ActorUser, PlatformAdmin: true}
	for i, in := range []requests.CreateInput{
		{Kind: requests.KindUpdate, StackID: &stackID, Justification: "raise the timeout", Resources: []requests.ResourceInput{
			{Name: "orders_queue", Module: "sqs", Version: "1.3.0", Inputs: json.RawMessage(`{"name":"orders"}`)}}},
		{Kind: requests.KindDestroy, StackID: &stackID, ConfirmStackName: "order-events", Justification: "again"},
	} {
		body, _ := json.Marshal(in)
		_, err := e.svc.reqs.Create(e.ctx, admin, requests.CreateCommand{Input: in, IdempotencyKey: fmt.Sprintf("key-after-destroy-%d", i),
			RequestHash: requests.RequestHash("POST", "/v1/requests", body)})
		wantCode(t, err, "stack_destroyed")
	}
}

func TestACancelledApplyFailsTheRequest(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "provisioning")
	reqID, short := e.request(stackID, "create", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 4501)
	run.mustSend("plan", runs.StagePassed)
	res := run.mustSend("apply", runs.StageCancelled)
	st, reason, _ := e.requestRow(reqID)
	if res.RunStatus != runs.StatusCancelled || st != "failed" || reason != requests.FailureApplyCancelled || e.stackRow(stackID).Status != "failed" {
		t.Fatalf("run %s request %s/%s stack %+v", res.RunStatus, st, reason, e.stackRow(stackID))
	}
}

// A summary that arrives before any other stage (their reports were lost) still carries
// the job's verdict: a passed summary means every step passed.
func TestSummaryBeforeTheOtherStages(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "provisioning")
	reqID, short := e.request(stackID, "create", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 4801)
	res := run.mustSend("summary", runs.StagePassed)
	if *res.RequestStatus != "applied" || e.stackRow(stackID).Status != "active" {
		t.Fatalf("%+v / %+v", res, e.stackRow(stackID))
	}
	// The stages that follow are recorded and change nothing.
	if _, err := run.send("apply", runs.StagePassed); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := e.requestRow(reqID); st != "applied" {
		t.Fatalf("request = %s", st)
	}
}

// A passed validation is never an approval, and validate reports never move requests.
func TestValidateReportsNeverMoveRequests(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "provisioning")
	for _, status := range []string{"pr_open", "validating", "plan_ready"} {
		reqID, short := e.request(stackID, "create", status)
		token := e.acquire(stackID, reqID)
		prRef := "refs/pull/812/merge"
		sha := strings.Repeat("e4", 20)
		bot := workflowPrincipal("acme/iacp/.github/workflows/pr-validate.yml@"+prRef, prRef, "pull_request", 9001, sha)
		for _, stage := range []string{"plan", "summary"} {
			res, err := e.svc.run.Record(e.ctx, bot, runs.Report{Pipeline: runs.KindValidate, StackPath: path, RequestShortID: short,
				FencingToken: &token, Stage: stage, Status: runs.StagePassed, Plan: json.RawMessage(`{"hash":"` + planHash + `"}`),
				GitHub: runs.GitHubRun{RunID: 9001, RunAttempt: 1, HeadSHA: sha}})
			if err != nil || *res.RequestStatus != status {
				t.Fatalf("%s %s: %+v %v", status, stage, res, err)
			}
		}
		if st, _, _ := e.requestRow(reqID); st != status || e.stackRow(stackID).Status != "provisioning" {
			t.Fatalf("a validate report moved the request to %s or the stack to %s", st, e.stackRow(stackID).Status)
		}
		if _, err := e.svc.lock.Release(e.ctx, locks.TokenCommand{StackID: stackID, RequestID: reqID, FencingToken: token}); err != nil {
			t.Fatal(err)
		}
	}
}

// The fencing token, the run and commit identity, the workflow file, the event and the ref
// are all still enforced, and a rejected report moves nothing and records nothing.
func TestRejectedApplyReportsChangeNothing(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "active")
	reqID, short := e.request(stackID, "update", "approved")
	token := e.acquire(stackID, reqID)
	otherStack, otherPath := e.stack("order-api", "active")
	otherReq, otherShort := e.request(otherStack, "update", "approved")
	_ = otherPath
	_ = otherReq

	good := e.run(path, short, token, 5001)
	cases := []struct {
		name string
		by   func() identity.Principal
		edit func(*runs.Report)
		code string
	}{
		{"stale fencing token", good.principal, func(r *runs.Report) { v := token + 1; r.FencingToken = &v }, "stale_fencing_token"},
		{"head_sha is not the token's sha", good.principal, func(r *runs.Report) { r.GitHub.HeadSHA = strings.Repeat("ab", 20) }, "run_mismatch"},
		{"another run id", good.principal, func(r *runs.Report) { r.GitHub.RunID = 5002 }, "run_mismatch"},
		{"another run attempt", good.principal, func(r *runs.Report) { r.GitHub.RunAttempt = 2 }, "run_mismatch"},
		{"a pr-validate token reporting apply", func() identity.Principal {
			return workflowPrincipal("acme/iacp/.github/workflows/pr-validate.yml@refs/heads/main", "refs/heads/main", "push", 5001, good.sha)
		}, func(*runs.Report) {}, "workflow_not_allowed"},
		{"apply.yml on a pull_request event", func() identity.Principal {
			return workflowPrincipal(applyWorkflow, "refs/heads/main", "pull_request", 5001, good.sha)
		}, func(*runs.Report) {}, "workflow_not_allowed"},
		{"apply.yml run from a branch", func() identity.Principal {
			return workflowPrincipal("acme/iacp/.github/workflows/apply.yml@refs/heads/evil", "refs/heads/evil", "push", 5001, good.sha)
		}, func(*runs.Report) {}, "workflow_not_allowed"},
		{"a request of another stack", good.principal, func(r *runs.Report) { r.RequestShortID = otherShort }, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := good.report("summary", runs.StagePassed)
			tc.edit(&r)
			_, err := e.svc.run.Record(e.ctx, tc.by(), r)
			wantCode(t, err, tc.code)
			if st, _, _ := e.requestRow(reqID); st != "approved" {
				t.Fatalf("a rejected report moved the request to %s", st)
			}
			if got := e.stackRow(stackID); got.Status != "active" || got.SHA != nil {
				t.Fatalf("a rejected report changed the stack: %+v", got)
			}
			if h, ok := e.lockHolder(stackID); !ok || h != reqID {
				t.Fatal("a rejected report released the lock")
			}
			if n := e.runs(stackID); n != 0 {
				t.Fatalf("a rejected report was recorded: %d runs", n)
			}
		})
	}

	t.Run("a request that was never approved", func(t *testing.T) {
		stack2, path2 := e.stack("order-queue", "active")
		for _, status := range []string{"draft", "pr_open", "validating", "plan_ready"} {
			id, short := e.request(stack2, "update", status)
			tok := e.acquire(stack2, id)
			run := e.run(path2, short, tok, 6001)
			_, err := run.send("summary", runs.StagePassed)
			wantCode(t, err, "request_not_applying")
			if st, _, _ := e.requestRow(id); st != status || e.stackRow(stack2).Status != "active" || e.runs(stack2) != 0 {
				t.Fatalf("%s: request %s stack %+v runs %d", status, st, e.stackRow(stack2), e.runs(stack2))
			}
			if _, err := e.svc.lock.Release(e.ctx, locks.TokenCommand{StackID: stack2, RequestID: id, FencingToken: tok}); err != nil {
				t.Fatal(err)
			}
		}
	})

	// The same run, correctly identified, goes through.
	if res, err := good.send("plan", runs.StagePassed); err != nil || *res.RequestStatus != "applying" {
		t.Fatalf("the genuine report: %+v %v", res, err)
	}
}

func (a *applyRun) attemptOrOne() int {
	if a.attempt == 0 {
		return 1
	}
	return a.attempt
}

func (a *applyRun) principal() identity.Principal { return a.bot }

// A terminal request that still holds its lease (its release found the lease taken over, or
// a hand-fixed row) takes a late report into the run and ignores it.
func TestLateReportForATerminalRequestStillHoldingTheLease(t *testing.T) {
	e := newApplyEnv(t)
	stackID, path := e.stack("order-events", "failed")
	reqID, short := e.request(stackID, "update", "approved")
	run := e.run(path, short, e.acquire(stackID, reqID), 7001)
	// The request ended without giving the lock up (say, a release that lost a race).
	e.must2(e.pool.Exec(e.ctx, `UPDATE requests SET status = 'failed', completed_at = now(), failure_reason = 'apply_failed' WHERE id = $1`, reqID))
	run.outputs = json.RawMessage(`{"x":"y"}`)

	res := run.mustSend("summary", runs.StagePassed)
	if *res.RequestStatus != "failed" || res.RunStatus != runs.StatusSucceeded {
		t.Fatalf("%+v", res)
	}
	if got := e.stackRow(stackID); got.Status != "failed" || got.SHA != nil || got.Outputs != `{"old": "output"}` {
		t.Fatalf("a late report changed the stack: %+v", got)
	}
	if st, _, _ := e.requestRow(reqID); st != "failed" || e.audits("request.run_report_ignored", reqID) != 1 {
		t.Fatalf("request %s, ignored audits %d", st, e.audits("request.run_report_ignored", reqID))
	}
}
