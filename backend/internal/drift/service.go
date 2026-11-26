package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

// Entry is a stored drift report with the stack it belongs to.
type Entry struct {
	ID              uuid.UUID
	StackID         uuid.UUID
	StackPath       string
	TeamID          uuid.UUID
	Team            string
	Environment     string
	PipelineRunID   uuid.UUID
	Result          Result
	TerraformExit   int
	Summary         Summary
	Changed         json.RawMessage
	PlanHash        string
	LogURL          string
	Error           string
	Status          *Status // nil unless Result is drifted
	DetectedAt      time.Time
	ResolvedBy      *uuid.UUID
	ResolvedAt      *time.Time
	ResolutionNote  string
	ResolutionReqID *uuid.UUID
}

// StackRow is the stack row a report locks.
type StackRow struct {
	ID     uuid.UUID
	TeamID uuid.UUID
	Status string // lifecycle status
	Health Health
}

// DueStack is a stack drift.yml should check.
type DueStack struct {
	ID          uuid.UUID
	Path        string
	Team        string
	Environment string
	Region      string
}

// RunRow is the pipeline_runs row of a drift run.
type RunRow struct {
	StackID     uuid.UUID
	Failed      bool
	GitHub      GitHubRun
	WorkflowRef string
	LogURL      string
	At          time.Time
}

// NewReport is a drift_reports row to insert.
type NewReport struct {
	ID            uuid.UUID
	StackID       uuid.UUID
	PipelineRunID uuid.UUID
	Report        Report
	Changed       json.RawMessage
	At            time.Time
}

// Resolution is the new state of an open report and its open siblings on the same stack.
type Resolution struct {
	ReportID  uuid.UUID
	StackID   uuid.UUID
	Status    Status
	By        *uuid.UUID // nil when a clean scan closes the reports
	Reason    string
	RequestID *uuid.UUID
	At        time.Time
}

// Store is the transactional persistence port. Every method runs in one transaction.
type Store interface {
	// LockStack loads the stack row FOR NO KEY UPDATE (stacks first, the order every lock
	// operation uses). apperr NotFound when no stack has the path.
	LockStack(ctx context.Context, path string) (StackRow, error)
	// UpsertRun inserts the drift run, or updates its status when the same run was reported
	// before (ON CONFLICT on run id, attempt and stack).
	UpsertRun(ctx context.Context, r RunRow) (uuid.UUID, error)
	// InsertReport inserts the report; inserted is false when the run already has one.
	InsertReport(ctx context.Context, r NewReport) (inserted bool, err error)
	ReportByRun(ctx context.Context, runID uuid.UUID) (Entry, error)
	// LockReport loads a report FOR UPDATE.
	LockReport(ctx context.Context, id uuid.UUID) (Entry, error)
	// SetStackCheck records a finished check: health, and last_drift_check_at unless the
	// check failed (at is zero then).
	SetStackCheck(ctx context.Context, stackID uuid.UUID, health Health, checkedAt *time.Time) error
	// CloseOpen resolves every open report of the stack as reconciled (a clean scan) and
	// returns how many it closed.
	CloseOpen(ctx context.Context, stackID uuid.UUID, at time.Time) (int, error)
	// Resolve moves the report, and the stack's other open reports, to r.Status; it returns
	// how many reports it changed.
	Resolve(ctx context.Context, r Resolution) (int, error)
	AppendAudit(ctx context.Context, e audit.Event) error
}

// Repository is the persistence port of the drift service.
type Repository interface {
	InTx(ctx context.Context, fn func(Store) error) error
	// DueStacks lists up to limit stacks to check: settled, with no request holding their
	// lock, least recently checked first.
	DueStacks(ctx context.Context, limit int) ([]DueStack, error)
	// ListByStack returns a visible stack's reports, newest first; apperr NotFound
	// (stack_not_found) when the stack does not exist or is hidden.
	ListByStack(ctx context.Context, v identity.Visibility, stackID uuid.UUID, limit int) ([]Entry, error)
	// Get returns a report of a visible stack; apperr NotFound otherwise.
	Get(ctx context.Context, v identity.Visibility, id uuid.UUID) (Entry, error)
}

// TeamVisibility resolves the teams a caller may read (identity.UserStore).
type TeamVisibility interface {
	VisibleTeams(ctx context.Context, p identity.Principal) (identity.Visibility, error)
}

// Reconciled is the update request a reconcile opened.
type Reconciled struct {
	RequestID uuid.UUID
	ShortID   string
	Status    string
}

// Reconciler opens the update request that re-asserts a stack's recorded configuration
// (requests.Service.ReconcileStack). The key makes a retried resolve open one request.
type Reconciler interface {
	ReconcileStack(ctx context.Context, by identity.Principal, stackID uuid.UUID, justification, key string) (Reconciled, error)
}

// Service records drift reports and resolves them.
type Service struct {
	repo       Repository
	teams      TeamVisibility
	members    requests.Memberships
	reconciler Reconciler
	clock      platform.Clock
	ids        platform.IDGenerator
}

// NewService returns a Service.
func NewService(repo Repository, teams TeamVisibility, members requests.Memberships, reconciler Reconciler,
	clock platform.Clock, ids platform.IDGenerator) *Service {
	return &Service{repo: repo, teams: teams, members: members, reconciler: reconciler, clock: clock, ids: ids}
}

// Recorded is the outcome of Record.
type Recorded struct {
	Entry    Entry
	Replayed bool // the run had already reported this stack
}

// MaxDueStacks bounds one drift.yml dispatch (docs/06 §4: at most 256 stacks).
const MaxDueStacks = 256

// DueStacks lists the stacks the next drift run should check.
func (s *Service) DueStacks(ctx context.Context, limit int) ([]DueStack, error) {
	if limit <= 0 || limit > MaxDueStacks {
		limit = MaxDueStacks
	}
	out, err := s.repo.DueStacks(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list stacks to check: %w", err)
	}
	return out, nil
}

// Record validates a drift check reported by drift.yml, stores it and moves the stack's
// health. The caller's OIDC identity must be the reporting run itself. A replay of the same
// run and stack returns the stored report and changes nothing.
func (s *Service) Record(ctx context.Context, by identity.Principal, r Report) (Recorded, error) {
	if errs := Validate(r); len(errs) > 0 {
		return Recorded{}, apperr.Invalid("validation_failed", "drift report is invalid", errs...)
	}
	// Reuse the run provenance rules: the run and commit are the caller's own, the workflow
	// is drift.yml, on main, via schedule or workflow_dispatch.
	if err := runs.CheckProvenance(runs.Report{Pipeline: runs.KindDrift, GitHub: runs.GitHubRun{
		RunID: r.GitHub.RunID, RunAttempt: r.GitHub.RunAttempt, HeadSHA: r.GitHub.HeadSHA}}, by.Workflow); err != nil {
		return Recorded{}, err
	}
	changed, err := json.Marshal(nonNil(r.Changed))
	if err != nil {
		return Recorded{}, fmt.Errorf("encode changed resources: %w", err)
	}
	var out Recorded
	err = s.repo.InTx(ctx, func(st Store) error {
		stack, err := st.LockStack(ctx, r.StackPath)
		if err != nil {
			return err
		}
		if !Checkable(stack.Status) {
			return apperr.Conflict("stack_not_checkable",
				fmt.Sprintf("the stack is %s; drift is only checked for settled stacks", stack.Status))
		}
		now := s.clock.Now()
		runID, err := st.UpsertRun(ctx, RunRow{StackID: stack.ID, Failed: r.Result == ResultError, GitHub: r.GitHub,
			WorkflowRef: by.Login, LogURL: r.LogURL, At: now})
		if err != nil {
			return fmt.Errorf("record drift run: %w", err)
		}
		inserted, err := st.InsertReport(ctx, NewReport{ID: s.ids.NewID(), StackID: stack.ID, PipelineRunID: runID,
			Report: r, Changed: changed, At: now})
		if err != nil {
			return fmt.Errorf("insert drift report: %w", err)
		}
		entry, err := st.ReportByRun(ctx, runID)
		if err != nil {
			return fmt.Errorf("load drift report: %w", err)
		}
		out = Recorded{Entry: entry, Replayed: !inserted}
		if !inserted {
			return nil
		}
		return s.apply(ctx, st, by, stack, entry, now)
	})
	if err != nil {
		return Recorded{}, err
	}
	return out, nil
}

// apply moves the stack's health for a freshly stored report and audits it.
func (s *Service) apply(ctx context.Context, st Store, by identity.Principal, stack StackRow, e Entry, now time.Time) error {
	var checked *time.Time
	if e.Result != ResultError {
		checked = &now
	}
	if err := st.SetStackCheck(ctx, stack.ID, NextHealth(stack.Health, e.Result), checked); err != nil {
		return fmt.Errorf("update stack health: %w", err)
	}
	audit1 := func(action string, payload map[string]any) error {
		payload["report_id"], payload["stack_path"], payload["run_id"] = e.ID, e.StackPath, e.PipelineRunID
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return st.AppendAudit(ctx, audit.Event{OccurredAt: now, ActorType: string(by.Type), ActorID: by.AuditActorID(),
			Action: action, SubjectType: "stack", SubjectID: stack.ID.String(), TeamID: &stack.TeamID, Payload: b})
	}
	switch e.Result {
	case ResultDrifted:
		return audit1("drift.detected", map[string]any{"resources_drifted": e.Summary.Drifted, "plan_hash": e.PlanHash})
	case ResultError:
		return audit1("drift.check_failed", map[string]any{"error": e.Error})
	}
	n, err := st.CloseOpen(ctx, stack.ID, now)
	if err != nil {
		return fmt.Errorf("close open drift reports: %w", err)
	}
	if n > 0 {
		return audit1("drift.cleared", map[string]any{"closed_reports": n})
	}
	return nil
}

func nonNil(c []ChangedResource) []ChangedResource {
	if c == nil {
		return []ChangedResource{}
	}
	return c
}

// MaxHistory bounds ListByStack.
const (
	DefaultHistory = 30
	MaxHistory     = 200
)

// List returns the drift history of a stack the caller may see, newest first.
func (s *Service) List(ctx context.Context, by identity.Principal, stackID uuid.UUID, limit int) ([]Entry, error) {
	v, err := s.visibility(ctx, by)
	if err != nil {
		return nil, err
	}
	switch {
	case limit <= 0:
		limit = DefaultHistory
	case limit > MaxHistory:
		limit = MaxHistory
	}
	out, err := s.repo.ListByStack(ctx, v, stackID, limit)
	if err != nil {
		return nil, fmt.Errorf("list drift reports: %w", err)
	}
	return out, nil
}

func (s *Service) visibility(ctx context.Context, by identity.Principal) (identity.Visibility, error) {
	if by.UserID == uuid.Nil {
		return identity.Visibility{}, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
			Message: "drift reports are shown to users"}
	}
	v, err := s.teams.VisibleTeams(ctx, by)
	if err != nil {
		return identity.Visibility{}, fmt.Errorf("resolve visible teams: %w", err)
	}
	return v, nil
}

// Resolved is the outcome of Resolve.
type Resolved struct {
	Entry   Entry
	Request *Reconciled // the update request a reconcile opened
	// Replayed is true when the report was already resolved with the same action.
	Replayed bool
}

// Resolve applies a user's decision to an open report. The caller needs the role the
// action's matrix row asks for in the stack's team (a viewer or outsider sees nothing: a
// stack the caller may not see is not found). A reconcile opens an update request that
// re-asserts the stack's recorded configuration, through the ordinary request path, so it
// is planned, reviewed and applied like any other change. A codify records that the
// operator will bring the configuration in line by hand: no request is opened (docs/06 §4).
func (s *Service) Resolve(ctx context.Context, by identity.Principal, id uuid.UUID, in ResolveInput) (Resolved, error) {
	if errs := ValidateResolve(in); len(errs) > 0 {
		return Resolved{}, apperr.Invalid("validation_failed", "resolve request is invalid", errs...)
	}
	v, err := s.visibility(ctx, by)
	if err != nil {
		return Resolved{}, err
	}
	cur, err := s.repo.Get(ctx, v, id)
	if err != nil {
		return Resolved{}, fmt.Errorf("get drift report: %w", err)
	}
	role, err := s.members.RoleInTeam(ctx, cur.Team, by.UserID)
	if err != nil {
		return Resolved{}, fmt.Errorf("lookup membership of team %s: %w", cur.Team, err)
	}
	if by.PlatformAdmin {
		// The flag on the principal is what route policies use too (the dev-mode user has it
		// without a users row that says so); a platform admin is admin in every team.
		role = rbac.RoleAdmin
	}
	if d := rbac.Decide(role, RBACAction(in.Action), rbac.Resource{Environment: cur.Environment}); !d.Allowed {
		return Resolved{}, apperr.Forbidden(fmt.Sprintf("%s drift for team %s needs the %s role or higher", in.Action, cur.Team, d.Need))
	}
	if replay, ok := replayOf(cur, in.Action); ok {
		return replay, nil
	}
	if _, ok := Allowed(statusOf(cur), in.Action); !ok {
		return Resolved{}, resolvedConflict(cur)
	}

	var req *Reconciled
	if in.Action == ActionReconcile {
		// The request is opened first, outside the report's transaction (it has its own),
		// under a key derived from the report, so a retry after a failure opens no second one.
		r, err := s.reconciler.ReconcileStack(ctx, by, cur.StackID, ReconcileJustification(in.Reason, cur.StackPath),
			"drift-reconcile-"+cur.ID.String())
		if err != nil {
			return Resolved{}, fmt.Errorf("open the reconcile request: %w", err)
		}
		req = &r
	}
	var out Resolved
	err = s.repo.InTx(ctx, func(st Store) error {
		locked, err := st.LockReport(ctx, id)
		if err != nil {
			return err
		}
		if replay, ok := replayOf(locked, in.Action); ok {
			out = replay
			return nil
		}
		next, ok := Allowed(statusOf(locked), in.Action)
		if !ok {
			return resolvedConflict(locked)
		}
		now := s.clock.Now()
		res := Resolution{ReportID: id, StackID: locked.StackID, Status: next, By: &by.UserID, Reason: in.Reason, At: now}
		if req != nil {
			res.RequestID = &req.RequestID
		}
		closed, err := st.Resolve(ctx, res)
		if err != nil {
			return fmt.Errorf("resolve drift report: %w", err)
		}
		payload := map[string]any{"report_id": id, "stack_path": locked.StackPath, "action": in.Action, "status": next,
			"reason": in.Reason, "closed_reports": closed}
		if req != nil {
			payload["request_id"], payload["request_short_id"] = req.RequestID, req.ShortID
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		if err := st.AppendAudit(ctx, audit.Event{OccurredAt: now, ActorType: string(by.Type), ActorID: by.AuditActorID(),
			Action: "drift.resolved", SubjectType: "stack", SubjectID: locked.StackID.String(), TeamID: &locked.TeamID, Payload: b}); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		locked.Status, locked.ResolvedBy, locked.ResolvedAt = &next, &by.UserID, &now
		locked.ResolutionNote, locked.ResolutionReqID = in.Reason, res.RequestID
		out = Resolved{Entry: locked, Request: req}
		return nil
	})
	if err != nil {
		return Resolved{}, err
	}
	return out, nil
}

// statusOf is the resolution status of an entry; a report that is not drifted has none to
// move, which Allowed treats as not open.
func statusOf(e Entry) Status {
	if e.Status == nil {
		return ""
	}
	return *e.Status
}

// replayOf recognises a retry: the report is already resolved with the action's status.
// Pure.
func replayOf(e Entry, a Action) (Resolved, bool) {
	want, ok := StatusFor(a)
	if !ok || e.Status == nil || *e.Status != want {
		return Resolved{}, false
	}
	return Resolved{Entry: e, Replayed: true}, true
}

func resolvedConflict(e Entry) error {
	if e.Status == nil {
		return apperr.Conflict("drift_not_resolvable", fmt.Sprintf("a %s report has no drift to resolve", e.Result))
	}
	return apperr.Conflict("drift_already_resolved", fmt.Sprintf("the drift report is already %s", *e.Status))
}
