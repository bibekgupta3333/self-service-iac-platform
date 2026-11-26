package drift

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// memRepo is an in-memory Repository + Store; InTx has no rollback, which these tests do
// not need.
type memRepo struct {
	stack    StackRow
	path     string
	team     string
	env      string
	runs     map[int64]uuid.UUID // github run id -> pipeline run
	reports  []*Entry
	audits   []audit.Event
	checked  *time.Time
	failRuns bool
}

func (m *memRepo) InTx(_ context.Context, fn func(Store) error) error { return fn(m) }

func (m *memRepo) DueStacks(context.Context, int) ([]DueStack, error) { return nil, nil }

func (m *memRepo) ListByStack(_ context.Context, v identity.Visibility, id uuid.UUID, _ int) ([]Entry, error) {
	if id != m.stack.ID || !v.Allows(m.stack.TeamID) {
		return nil, apperr.NotFound("stack %s not found", id)
	}
	var out []Entry
	for _, r := range m.reports {
		out = append(out, *r)
	}
	return out, nil
}

func (m *memRepo) Get(_ context.Context, v identity.Visibility, id uuid.UUID) (Entry, error) {
	for _, r := range m.reports {
		if r.ID == id && v.Allows(r.TeamID) {
			return *r, nil
		}
	}
	return Entry{}, apperr.NotFound("drift report %s not found", id)
}

func (m *memRepo) LockStack(_ context.Context, path string) (StackRow, error) {
	if path != m.path {
		return StackRow{}, apperr.NotFound("stack %q not found", path)
	}
	return m.stack, nil
}

func (m *memRepo) UpsertRun(_ context.Context, r RunRow) (uuid.UUID, error) {
	if m.failRuns {
		return uuid.Nil, errors.New("db down")
	}
	if id, ok := m.runs[r.GitHub.RunID]; ok {
		return id, nil
	}
	id := uuid.New()
	m.runs[r.GitHub.RunID] = id
	return id, nil
}

func (m *memRepo) InsertReport(_ context.Context, n NewReport) (bool, error) {
	for _, r := range m.reports {
		if r.PipelineRunID == n.PipelineRunID {
			return false, nil
		}
	}
	e := &Entry{ID: n.ID, StackID: n.StackID, StackPath: m.path, TeamID: m.stack.TeamID, Team: m.team, Environment: m.env,
		PipelineRunID: n.PipelineRunID, Result: n.Report.Result, TerraformExit: n.Report.TerraformExitCode,
		Summary: n.Report.Summary, Changed: n.Changed, PlanHash: n.Report.PlanHash, Error: n.Report.Error, DetectedAt: n.At}
	if n.Report.Result == ResultDrifted {
		open := StatusOpen
		e.Status = &open
	}
	m.reports = append(m.reports, e)
	return true, nil
}

func (m *memRepo) ReportByRun(_ context.Context, runID uuid.UUID) (Entry, error) {
	for _, r := range m.reports {
		if r.PipelineRunID == runID {
			return *r, nil
		}
	}
	return Entry{}, errors.New("no report")
}

func (m *memRepo) LockReport(ctx context.Context, id uuid.UUID) (Entry, error) {
	return m.Get(ctx, identity.Visibility{All: true}, id)
}

func (m *memRepo) SetStackCheck(_ context.Context, _ uuid.UUID, h Health, at *time.Time) error {
	m.stack.Health = h
	if at != nil {
		m.checked = at
	}
	return nil
}

func (m *memRepo) CloseOpen(_ context.Context, _ uuid.UUID, at time.Time) (int, error) {
	n := 0
	for _, r := range m.reports {
		if r.Status != nil && *r.Status == StatusOpen {
			rec := StatusReconciled
			r.Status, r.ResolvedAt = &rec, &at
			n++
		}
	}
	return n, nil
}

func (m *memRepo) Resolve(_ context.Context, res Resolution) (int, error) {
	n := 0
	for _, r := range m.reports {
		if r.Status != nil && *r.Status == StatusOpen {
			st := res.Status
			r.Status, r.ResolvedBy, r.ResolvedAt, r.ResolutionNote = &st, res.By, &res.At, res.Reason
			if r.ID == res.ReportID {
				r.ResolutionReqID = res.RequestID
			}
			n++
		}
	}
	return n, nil
}

func (m *memRepo) AppendAudit(_ context.Context, e audit.Event) error {
	m.audits = append(m.audits, e)
	return nil
}

func (m *memRepo) actions() []string {
	var out []string
	for _, e := range m.audits {
		out = append(out, e.Action)
	}
	return out
}

type fakeTeams struct{ v identity.Visibility }

func (f fakeTeams) VisibleTeams(context.Context, identity.Principal) (identity.Visibility, error) {
	return f.v, nil
}

type fakeMembers struct{ role rbac.Role }

func (f fakeMembers) RoleInTeam(context.Context, string, uuid.UUID) (rbac.Role, error) {
	return f.role, nil
}

type fakeReconciler struct {
	calls []string
	err   error
}

func (f *fakeReconciler) ReconcileStack(_ context.Context, _ identity.Principal, _ uuid.UUID, justification, key string) (Reconciled, error) {
	f.calls = append(f.calls, key+"|"+justification)
	if f.err != nil {
		return Reconciled{}, f.err
	}
	return Reconciled{RequestID: uuid.MustParse("00000000-0000-0000-0000-0000000000aa"), ShortID: "req-recon1", Status: "draft"}, nil
}

const testPath = "environments/development/payments/order-events"

type fixture struct {
	repo *memRepo
	rec  *fakeReconciler
	svc  *Service
	user identity.Principal
	bot  identity.Principal
}

func newFixture(t *testing.T, role rbac.Role) *fixture {
	t.Helper()
	teamID := uuid.New()
	repo := &memRepo{stack: StackRow{ID: uuid.New(), TeamID: teamID, Status: "active", Health: HealthHealthy},
		path: testPath, team: "payments", env: "development", runs: map[int64]uuid.UUID{}}
	rec := &fakeReconciler{}
	svc := NewService(repo, fakeTeams{identity.Visibility{TeamIDs: []uuid.UUID{teamID}}}, fakeMembers{role}, rec,
		fixedClock{time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)}, platform.RandomIDs{})
	wf := &identity.Workflow{Ref: identity.MainRef, EventName: "schedule", RunID: 900, RunAttempt: 1,
		SHA: strings.Repeat("ab", 20), JobWorkflowRef: "acme/iacp/.github/workflows/drift.yml@" + identity.MainRef}
	return &fixture{repo: repo, rec: rec, svc: svc,
		user: identity.Principal{UserID: uuid.New(), Login: "priya-s", Type: identity.ActorUser},
		bot:  identity.Principal{Login: wf.JobWorkflowRef, Type: identity.ActorGitHubActions, Workflow: wf}}
}

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}

func TestRecordDriftSetsHealthNotLifecycle(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	res, err := f.svc.Record(context.Background(), f.bot, validReport())
	if err != nil || res.Replayed || res.Entry.Status == nil || *res.Entry.Status != StatusOpen {
		t.Fatalf("Record = %+v, %v", res, err)
	}
	if f.repo.stack.Health != HealthDrifted || f.repo.stack.Status != "active" {
		t.Fatalf("stack = %+v: drift moves health only", f.repo.stack)
	}
	if f.repo.checked == nil {
		t.Fatal("last_drift_check_at must be set by a completed check")
	}
	if got := f.repo.actions(); len(got) != 1 || got[0] != "drift.detected" {
		t.Fatalf("audit = %v", got)
	}
	var changed []ChangedResource
	if err := json.Unmarshal(res.Entry.Changed, &changed); err != nil || len(changed) != 2 {
		t.Fatalf("changed = %s, %v", res.Entry.Changed, err)
	}
}

func TestRecordReplayChangesNothing(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	first, err := f.svc.Record(context.Background(), f.bot, validReport())
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.Record(context.Background(), f.bot, validReport())
	if err != nil || !again.Replayed || again.Entry.ID != first.Entry.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if len(f.repo.reports) != 1 || len(f.repo.audits) != 1 {
		t.Fatalf("replay stored %d reports, %d audits", len(f.repo.reports), len(f.repo.audits))
	}
}

func TestCleanScanRestoresHealthAndClosesOpenReports(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	if _, err := f.svc.Record(context.Background(), f.bot, validReport()); err != nil {
		t.Fatal(err)
	}
	f.bot.Workflow.RunID = 901
	clean := Report{StackPath: testPath, GitHub: GitHubRun{RunID: 901, RunAttempt: 1, HeadSHA: strings.Repeat("ab", 20)},
		Result: ResultClean, TerraformExitCode: ExitClean}
	res, err := f.svc.Record(context.Background(), f.bot, clean)
	if err != nil || res.Entry.Status != nil {
		t.Fatalf("clean = %+v, %v", res, err)
	}
	if f.repo.stack.Health != HealthHealthy {
		t.Fatal("a clean scan restores health")
	}
	if st := f.repo.reports[0].Status; st == nil || *st != StatusReconciled || f.repo.reports[0].ResolvedBy != nil {
		t.Fatalf("the open report must be closed by the system: %+v", f.repo.reports[0])
	}
	if got := strings.Join(f.repo.actions(), ","); got != "drift.detected,drift.cleared" {
		t.Fatalf("audit = %s", got)
	}
}

func TestFailedCheckKeepsHealthAndCheckTime(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	f.repo.stack.Health = HealthDrifted
	r := Report{StackPath: testPath, GitHub: validReport().GitHub, Result: ResultError, TerraformExitCode: ExitError, Error: "init failed"}
	res, err := f.svc.Record(context.Background(), f.bot, r)
	if err != nil || res.Entry.Result != ResultError || res.Entry.Status != nil {
		t.Fatalf("error report = %+v, %v", res, err)
	}
	if f.repo.stack.Health != HealthDrifted || f.repo.checked != nil {
		t.Fatalf("a failed check must not change health or the check time: %+v", f.repo.stack)
	}
	if got := f.repo.actions(); len(got) != 1 || got[0] != "drift.check_failed" {
		t.Fatalf("audit = %v", got)
	}
}

func TestRecordRefusals(t *testing.T) {
	t.Run("invalid report", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		r := validReport()
		r.Result = ResultClean
		if _, err := f.svc.Record(context.Background(), f.bot, r); !hasCode(err, "validation_failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("another run's identity", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		f.bot.Workflow.RunID = 7
		if _, err := f.svc.Record(context.Background(), f.bot, validReport()); !hasCode(err, "run_mismatch") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("another workflow", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		f.bot.Workflow.JobWorkflowRef = "acme/iacp/.github/workflows/apply.yml@" + identity.MainRef
		if _, err := f.svc.Record(context.Background(), f.bot, validReport()); !hasCode(err, "workflow_not_allowed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not from main", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		f.bot.Workflow.Ref = "refs/pull/1/merge"
		if _, err := f.svc.Record(context.Background(), f.bot, validReport()); !hasCode(err, "workflow_not_allowed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown stack", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		r := validReport()
		r.StackPath = "environments/development/payments/nope"
		if _, err := f.svc.Record(context.Background(), f.bot, r); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	for _, status := range []string{"provisioning", "updating", "destroying", "destroyed", "failed"} {
		t.Run("stack "+status, func(t *testing.T) {
			f := newFixture(t, rbac.RoleDeveloper)
			f.repo.stack.Status = status
			if _, err := f.svc.Record(context.Background(), f.bot, validReport()); !hasCode(err, "stack_not_checkable") {
				t.Fatalf("err = %v", err)
			}
			if f.repo.stack.Health != HealthHealthy || len(f.repo.reports) != 0 {
				t.Fatal("a refused report changes nothing")
			}
		})
	}
	t.Run("shared token skips provenance", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		token := identity.Principal{Login: "internal-token", Type: identity.ActorGitHubActions}
		if _, err := f.svc.Record(context.Background(), token, validReport()); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func seedOpen(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	res, err := f.svc.Record(context.Background(), f.bot, validReport())
	if err != nil {
		t.Fatal(err)
	}
	f.repo.audits = nil
	return res.Entry.ID
}

func TestResolveReconcileOpensRequestAndAudits(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	id := seedOpen(t, f)
	res, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionReconcile})
	if err != nil || res.Request == nil || res.Request.ShortID != "req-recon1" || res.Replayed {
		t.Fatalf("Resolve = %+v, %v", res, err)
	}
	if st := res.Entry.Status; st == nil || *st != StatusReconciled || res.Entry.ResolvedBy == nil || *res.Entry.ResolvedBy != f.user.UserID ||
		res.Entry.ResolutionReqID == nil {
		t.Fatalf("entry = %+v", res.Entry)
	}
	if len(f.rec.calls) != 1 || !strings.HasPrefix(f.rec.calls[0], "drift-reconcile-"+id.String()+"|Reconcile drift") {
		t.Fatalf("reconciler calls = %v", f.rec.calls)
	}
	if got := f.repo.actions(); len(got) != 1 || got[0] != "drift.resolved" {
		t.Fatalf("audit = %v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.repo.audits[0].Payload, &payload); err != nil || payload["action"] != "reconcile" ||
		payload["request_short_id"] != "req-recon1" {
		t.Fatalf("audit payload = %s, %v", f.repo.audits[0].Payload, err)
	}
	// Health stays drifted until a clean scan: resolving is not proof.
	if f.repo.stack.Health != HealthDrifted {
		t.Fatal("resolving must not restore health")
	}

	// A retry returns the stored resolution; no second request, no second audit event.
	again, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionReconcile})
	if err != nil || !again.Replayed || len(f.rec.calls) != 1 || len(f.repo.audits) != 1 {
		t.Fatalf("replay = %+v, %v, calls=%d audits=%d", again, err, len(f.rec.calls), len(f.repo.audits))
	}
	// A different action on a resolved report conflicts.
	if _, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionCodify}); !hasCode(err, "drift_already_resolved") {
		t.Fatalf("second action: %v", err)
	}
}

func TestResolveCodifyOpensNoRequest(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	id := seedOpen(t, f)
	res, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionCodify, Reason: "intended console change"})
	if err != nil || res.Request != nil || len(f.rec.calls) != 0 {
		t.Fatalf("codify = %+v, %v, calls=%v", res, err, f.rec.calls)
	}
	if st := res.Entry.Status; st == nil || *st != StatusCodified {
		t.Fatalf("status = %v", st)
	}
}

func TestResolveIgnoreNeedsMaintainerAndReason(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	id := seedOpen(t, f)
	ignore := ResolveInput{Action: ActionIgnore, Reason: "known tag set by the cost team"}
	if _, err := f.svc.Resolve(context.Background(), f.user, id, ignore); !apperr.IsKind(err, apperr.KindForbidden) {
		t.Fatalf("developer ignoring: %v", err)
	}
	if st := f.repo.reports[0].Status; *st != StatusOpen {
		t.Fatal("a denied ignore must not resolve the report")
	}

	m := newFixture(t, rbac.RoleMaintainer)
	id = seedOpen(t, m)
	if _, err := m.svc.Resolve(context.Background(), m.user, id, ResolveInput{Action: ActionIgnore}); !hasCode(err, "validation_failed") {
		t.Fatalf("ignore without a justification: %v", err)
	}
	res, err := m.svc.Resolve(context.Background(), m.user, id, ignore)
	if err != nil || *res.Entry.Status != StatusIgnored || res.Entry.ResolutionNote != ignore.Reason {
		t.Fatalf("maintainer ignore = %+v, %v", res, err)
	}
	if f.repo.stack.Health != HealthDrifted || m.repo.stack.Health != HealthDrifted {
		t.Fatal("an ignored report keeps the stack drifted")
	}
}

func TestResolveAccessControl(t *testing.T) {
	t.Run("viewer", func(t *testing.T) {
		f := newFixture(t, rbac.RoleViewer)
		id := seedOpen(t, f)
		if _, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionReconcile}); !apperr.IsKind(err, apperr.KindForbidden) {
			t.Fatalf("viewer: %v", err)
		}
		if len(f.rec.calls) != 0 {
			t.Fatal("no request for a denied caller")
		}
	})
	t.Run("hidden team", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		id := seedOpen(t, f)
		f.svc.teams = fakeTeams{identity.Visibility{TeamIDs: []uuid.UUID{uuid.New()}}}
		if _, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionReconcile}); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("hidden: %v", err)
		}
	})
	t.Run("machine caller", func(t *testing.T) {
		f := newFixture(t, rbac.RoleAdmin)
		id := seedOpen(t, f)
		if _, err := f.svc.Resolve(context.Background(), f.bot, id, ResolveInput{Action: ActionReconcile}); !apperr.IsKind(err, apperr.KindUnauthenticated) {
			t.Fatalf("machine: %v", err)
		}
	})
	t.Run("unknown report", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		if _, err := f.svc.Resolve(context.Background(), f.user, uuid.New(), ResolveInput{Action: ActionReconcile}); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("unknown: %v", err)
		}
	})
	t.Run("clean report has nothing to resolve", func(t *testing.T) {
		f := newFixture(t, rbac.RoleDeveloper)
		clean := Report{StackPath: testPath, GitHub: validReport().GitHub, Result: ResultClean, TerraformExitCode: ExitClean}
		res, err := f.svc.Record(context.Background(), f.bot, clean)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Resolve(context.Background(), f.user, res.Entry.ID, ResolveInput{Action: ActionReconcile}); !hasCode(err, "drift_not_resolvable") {
			t.Fatalf("clean: %v", err)
		}
	})
}

func TestResolveReconcileFailureLeavesReportOpen(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	id := seedOpen(t, f)
	f.rec.err = apperr.Conflict("stack_busy", "the stack is updating")
	if _, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionReconcile}); !hasCode(err, "stack_busy") {
		t.Fatalf("err = %v", err)
	}
	if st := f.repo.reports[0].Status; *st != StatusOpen || len(f.repo.audits) != 0 {
		t.Fatal("a failed reconcile must leave the report open and unaudited")
	}
}

func TestResolveClosesSiblingOpenReports(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	seedOpen(t, f)
	f.bot.Workflow.RunID = 905
	r := validReport()
	r.GitHub.RunID = 905
	second, err := f.svc.Record(context.Background(), f.bot, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Resolve(context.Background(), f.user, second.Entry.ID, ResolveInput{Action: ActionCodify}); err != nil {
		t.Fatal(err)
	}
	for i, rep := range f.repo.reports {
		if *rep.Status != StatusCodified {
			t.Errorf("report %d is %s; resolving closes the stack's open reports", i, *rep.Status)
		}
	}
}

func TestListScopesToVisibleStacks(t *testing.T) {
	f := newFixture(t, rbac.RoleViewer)
	seedOpen(t, f)
	rows, err := f.svc.List(context.Background(), f.user, f.repo.stack.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("List = %v, %v", rows, err)
	}
	f.svc.teams = fakeTeams{} // sees nothing
	if _, err := f.svc.List(context.Background(), f.user, f.repo.stack.ID, 0); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("hidden stack: %v", err)
	}
	if _, err := f.svc.List(context.Background(), f.bot, f.repo.stack.ID, 0); !apperr.IsKind(err, apperr.KindUnauthenticated) {
		t.Fatalf("machine caller: %v", err)
	}
}
