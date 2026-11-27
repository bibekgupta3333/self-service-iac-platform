package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// Break-glass state surgery operations (docs/06 §2.3). The workflow runs nothing else.
const (
	OpStateRm     = "state rm"
	OpStateMv     = "state mv"
	OpImport      = "import"
	OpForceUnlock = "force-unlock"
)

// SurgeryOperations is the allow-list, in the order the workflow offers them.
var SurgeryOperations = []string{OpStateRm, OpStateMv, OpImport, OpForceUnlock}

// Surgery outcomes.
const (
	SurgerySucceeded = "succeeded"
	SurgeryFailed    = "failed"
	SurgeryRefused   = "refused" // validation refused the inputs; nothing ran
)

// SurgeryWorkflow is the only workflow that reports state surgery.
const SurgeryWorkflow = "state-surgery.yml"

// SurgeryReport is the body of POST /v1/internal/state-surgery: what one break-glass run
// did to a stack's state. It mirrors tools/stack_pipeline/state_surgery.py.
type SurgeryReport struct {
	StackPath   string   `json:"stack_path"`
	Operation   string   `json:"operation"`
	Args        []string `json:"args"`
	Ticket      string   `json:"ticket"`
	RequestedBy string   `json:"requested_by"`
	DryRun      bool     `json:"dry_run"`
	Outcome     string   `json:"outcome"`
	ExitCode    *int     `json:"exit_code,omitempty"`
	// PlanExitCode is the post-step terraform plan -detailed-exitcode (0 no changes, 2 changes).
	PlanExitCode *int `json:"plan_exit_code,omitempty"`
	// StateBefore and StateAfter are sha256 digests of the state before and after (empty
	// when there was none). With the S3 backend they become the object VersionIds.
	StateBefore string    `json:"state_before_sha256,omitempty"`
	StateAfter  string    `json:"state_after_sha256,omitempty"`
	GitHub      GitHubRun `json:"github"`
	// TriggeredBy is github.triggering_actor (who started this attempt; differs from the
	// dispatcher on a re-run). RequestedBy is what the workflow reported; the audit event
	// names the verified OIDC actor claim instead.
	TriggeredBy string `json:"triggered_by,omitempty"`
}

// BreakGlassEnvironment is the GitHub Environment whose approval gates the surgery job. Its
// name is in the OIDC token's environment claim only for a job that passed that gate.
const BreakGlassEnvironment = "break-glass"

// GitHubRun identifies the reporting workflow run.
type GitHubRun struct {
	RunID      int64  `json:"run_id"`
	RunAttempt int    `json:"run_attempt"`
	HeadSHA    string `json:"head_sha"`
}

var (
	surgeryStackPathRe = regexp.MustCompile(`^environments/[a-z]+/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}$`)
	// A resource address: optional module path, optional data., type.name, optional index.
	// String keys are limited to a safe character set; no spaces or quotes inside.
	addressRe = regexp.MustCompile(`^(module\.[a-z_][a-z0-9_-]*(\[[0-9]{1,6}\]|\["[A-Za-z0-9_.:/-]{1,64}"\])?\.)*` +
		`(data\.)?[a-z][a-z0-9_]*\.[A-Za-z_][A-Za-z0-9_-]*(\[[0-9]{1,6}\]|\["[A-Za-z0-9_.:/-]{1,64}"\])?$`)
	importIDRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:/_.@=+,-]{0,255}$`)
	lockIDRe    = regexp.MustCompile(`^[0-9A-Fa-f][0-9A-Fa-f-]{7,63}$`)
	ticketRe    = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[0-9]{1,7}$`)
	loginRe     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	sha256Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	headSHARe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	maxAddrSize = 256
)

// ValidateSurgeryArgs checks an operation and its arguments against the allow-list. Pure.
//
//	state rm      ADDRESS... (1 to 10 resource addresses)
//	state mv      SOURCE DESTINATION
//	import        ADDRESS ID
//	force-unlock  LOCK_ID
func ValidateSurgeryArgs(op string, args []string) []apperr.FieldError {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}
	addr := func(i int) {
		if a := args[i]; len(a) > maxAddrSize || !addressRe.MatchString(a) {
			add(fmt.Sprintf("/args/%d", i), "pattern", "must be a Terraform resource address such as module.queue.aws_sqs_queue.this")
		}
	}
	switch op {
	case OpStateRm:
		if len(args) < 1 || len(args) > 10 {
			add("/args", "count", "state rm takes 1 to 10 resource addresses")
			return errs
		}
		for i := range args {
			addr(i)
		}
	case OpStateMv:
		if len(args) != 2 {
			add("/args", "count", "state mv takes a source and a destination address")
			return errs
		}
		addr(0)
		addr(1)
		if args[0] == args[1] {
			add("/args/1", "same_address", "the destination must differ from the source")
		}
	case OpImport:
		if len(args) != 2 {
			add("/args", "count", "import takes a resource address and the resource's ID")
			return errs
		}
		addr(0)
		if !importIDRe.MatchString(args[1]) {
			add("/args/1", "pattern", "must be a resource ID or ARN (letters, digits and :/_.@=+,-)")
		}
	case OpForceUnlock:
		if len(args) != 1 {
			add("/args", "count", "force-unlock takes the lock ID")
			return errs
		}
		if !lockIDRe.MatchString(args[0]) {
			add("/args/0", "pattern", "must be the hexadecimal lock ID shown in the lock error")
		}
	default:
		add("/operation", "enum", "must be one of: state rm, state mv, import, force-unlock")
	}
	return errs
}

// ValidateSurgeryReport checks a report's shape. Pure.
func ValidateSurgeryReport(r SurgeryReport) []apperr.FieldError {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}
	// A refused run whose stack path was the bad input reports it empty; it is still audited.
	if !surgeryStackPathRe.MatchString(r.StackPath) && (r.Outcome != SurgeryRefused || r.StackPath != "") {
		add("/stack_path", "pattern", "must look like environments/<env>/<team>/<stack>")
	}
	if !slices.Contains([]string{SurgerySucceeded, SurgeryFailed, SurgeryRefused}, r.Outcome) {
		add("/outcome", "enum", "must be one of succeeded, failed, refused")
	}
	// A refused run reports what was asked for, which validation rejected: the arguments
	// are not repeated into the audit log, only the operation if it is a known one.
	if r.Outcome != SurgeryRefused {
		errs = append(errs, ValidateSurgeryArgs(r.Operation, r.Args)...)
	} else if r.Operation != "" && !slices.Contains(SurgeryOperations, r.Operation) {
		add("/operation", "enum", "must be one of: state rm, state mv, import, force-unlock")
	}
	if !ticketRe.MatchString(r.Ticket) && r.Outcome != SurgeryRefused {
		add("/ticket", "pattern", "must be an incident or change ticket such as INC-1234")
	}
	for ptr, v := range map[string]string{"/requested_by": r.RequestedBy, "/triggered_by": r.TriggeredBy} {
		if v != "" && v != "unknown" && !loginRe.MatchString(v) {
			add(ptr, "pattern", "must be a GitHub login")
		}
	}
	for ptr, v := range map[string]string{"/state_before_sha256": r.StateBefore, "/state_after_sha256": r.StateAfter} {
		if v != "" && !sha256Re.MatchString(v) {
			add(ptr, "pattern", "must be a sha256 hex digest")
		}
	}
	if r.GitHub.RunID <= 0 || r.GitHub.RunAttempt <= 0 {
		add("/github", "required", "run_id and run_attempt are required")
	}
	if !headSHARe.MatchString(r.GitHub.HeadSHA) {
		add("/github/head_sha", "pattern", "must be 40 lowercase hex characters")
	}
	slices.SortStableFunc(errs, func(a, b apperr.FieldError) int { return strings.Compare(a.Pointer, b.Pointer) })
	return errs
}

// CheckSurgeryProvenance matches a report against the caller's verified workflow. The route
// class (auth.RouteStateSurgery) already pinned the workflow file, ref and event and refused
// the shared token; this checks the rest. Pure.
//
//   - there must be a verified workflow (OIDC), never the shared token (oidc_required);
//   - the run and commit are the caller's own (run_mismatch);
//   - the token names the actor who started the run (actor_missing);
//   - a run that did anything (not refused) must come from a job that passed the
//     break-glass Environment gate: its token carries environment "break-glass"
//     (environment_required). A refused run is reported by the validate job, before the gate.
func CheckSurgeryProvenance(r SurgeryReport, wf *identity.Workflow) error {
	forbidden := func(code, msg string) error {
		return &apperr.Error{Kind: apperr.KindForbidden, Code: code, Message: msg}
	}
	switch {
	case wf == nil:
		return forbidden("oidc_required", "state surgery reports must carry the state-surgery.yml run's GitHub OIDC token")
	case r.GitHub.RunID != wf.RunID || r.GitHub.RunAttempt != wf.RunAttempt || r.GitHub.HeadSHA != wf.SHA:
		return forbidden("run_mismatch",
			fmt.Sprintf("report for run %d/%d sent by run %d/%d", r.GitHub.RunID, r.GitHub.RunAttempt, wf.RunID, wf.RunAttempt))
	case !loginRe.MatchString(wf.Actor):
		return forbidden("actor_missing", "the OIDC token names no actor")
	case r.Outcome != SurgeryRefused && wf.Environment != BreakGlassEnvironment:
		return forbidden("environment_required",
			fmt.Sprintf("a surgery that ran must be reported by the job of the %q environment", BreakGlassEnvironment))
	}
	return nil
}

// SurgeryCorrelationID ties every audit event of one run attempt together. Pure.
func SurgeryCorrelationID(g GitHubRun) string {
	return fmt.Sprintf("state-surgery-%d-%d", g.RunID, g.RunAttempt)
}

// SurgeryStack is the stack a surgery report names.
type SurgeryStack struct {
	ID     uuid.UUID
	TeamID uuid.UUID
}

// SurgeryStore is the transactional port of the SurgeryRecorder.
type SurgeryStore interface {
	// StackByPath resolves the stack (apperr NotFound stack_not_found when it does not exist).
	StackByPath(ctx context.Context, path string) (SurgeryStack, error)
	// Recorded reports whether an audit event with this action and correlation id exists.
	Recorded(ctx context.Context, action, correlationID string) (bool, error)
	AppendAudit(ctx context.Context, e audit.Event) error
}

// SurgeryRepository runs a function in one transaction.
type SurgeryRepository interface {
	InSurgeryTx(ctx context.Context, fn func(SurgeryStore) error) error
}

// SurgeryRecorder appends break-glass state surgery to the audit chain (state.surgery).
type SurgeryRecorder struct {
	repo  SurgeryRepository
	clock platform.Clock
}

// NewSurgeryRecorder returns a SurgeryRecorder.
func NewSurgeryRecorder(repo SurgeryRepository, clock platform.Clock) *SurgeryRecorder {
	return &SurgeryRecorder{repo: repo, clock: clock}
}

// Recorded is the outcome of Record. StackID is nil for a refused run whose stack path was
// invalid or names no stack.
type Recorded struct {
	StackID   *uuid.UUID `json:"stack_id"`
	Duplicate bool       `json:"duplicate"`
}

// Record validates a report from by and appends it as one state.surgery audit event on the
// stack. A report sent again by the same run attempt is recorded once (duplicate).
func (s *SurgeryRecorder) Record(ctx context.Context, by identity.Principal, r SurgeryReport) (Recorded, error) {
	if errs := ValidateSurgeryReport(r); len(errs) > 0 {
		return Recorded{}, apperr.Invalid("validation_failed", "state surgery report is invalid", errs...)
	}
	if err := CheckSurgeryProvenance(r, by.Workflow); err != nil {
		return Recorded{}, err
	}
	payload, err := json.Marshal(surgeryPayload(r, *by.Workflow))
	if err != nil {
		return Recorded{}, fmt.Errorf("encode audit payload: %w", err)
	}
	correlationID := SurgeryCorrelationID(r.GitHub)
	var out Recorded
	err = s.repo.InSurgeryTx(ctx, func(st SurgeryStore) error {
		// The subject is the stack. A refused run with no valid or known stack path is still
		// audited, on the run itself (subject state_surgery) with no team.
		subjectType, subjectID, teamID := "state_surgery", correlationID, (*uuid.UUID)(nil)
		if r.StackPath != "" {
			stack, err := st.StackByPath(ctx, r.StackPath)
			switch {
			case err == nil:
				id, team := stack.ID, stack.TeamID
				out.StackID, subjectType, subjectID, teamID = &id, "stack", id.String(), &team
			case !(r.Outcome == SurgeryRefused && apperr.IsKind(err, apperr.KindNotFound)):
				return err
			}
		}
		dup, err := st.Recorded(ctx, "state.surgery", correlationID)
		if err != nil {
			return fmt.Errorf("check earlier report: %w", err)
		}
		if dup {
			out.Duplicate = true
			return nil
		}
		actorType := string(by.Type)
		if actorType == "" {
			actorType = audit.ActorGitHubActions
		}
		return st.AppendAudit(ctx, audit.Event{OccurredAt: s.clock.Now(), ActorType: actorType, ActorID: by.AuditActorID(),
			Action: "state.surgery", SubjectType: subjectType, SubjectID: subjectID, TeamID: teamID,
			CorrelationID: correlationID, Payload: payload})
	})
	if err != nil {
		return Recorded{}, err
	}
	return out, nil
}

// surgeryPayload is the audit payload of a report sent by wf: everything but the raw
// arguments of a refused run. requested_by is the verified OIDC actor; the workflow's own
// claim is kept as reported_requested_by only when it differs. approved_via is set only for a
// run reported from the break-glass Environment job (CheckSurgeryProvenance); a refused run,
// reported before the gate, records null. Pure.
func surgeryPayload(r SurgeryReport, wf identity.Workflow) map[string]any {
	p := map[string]any{
		"stack_path": nullIfEmpty(r.StackPath), "operation": nullIfEmpty(r.Operation), "requested_by": wf.Actor,
		"dry_run": r.DryRun, "outcome": r.Outcome, "workflow": SurgeryWorkflow,
		"run_id": r.GitHub.RunID, "run_attempt": r.GitHub.RunAttempt, "head_sha": r.GitHub.HeadSHA,
		"approved_via": nil,
	}
	if r.Outcome != SurgeryRefused && wf.Environment == BreakGlassEnvironment {
		p["approved_via"] = "github_environment:" + BreakGlassEnvironment
	}
	if r.RequestedBy != "" && r.RequestedBy != wf.Actor && loginRe.MatchString(r.RequestedBy) {
		p["reported_requested_by"] = r.RequestedBy
	}
	if loginRe.MatchString(r.TriggeredBy) {
		p["triggered_by"] = r.TriggeredBy
	}
	if r.Outcome != SurgeryRefused {
		p["args"] = r.Args
	}
	if ticketRe.MatchString(r.Ticket) {
		p["ticket"] = r.Ticket
	}
	if r.ExitCode != nil {
		p["exit_code"] = *r.ExitCode
	}
	if r.PlanExitCode != nil {
		p["plan_exit_code"] = *r.PlanExitCode
	}
	if r.StateBefore != "" {
		p["state_before_sha256"] = r.StateBefore
	}
	if r.StateAfter != "" {
		p["state_after_sha256"] = r.StateAfter
	}
	return p
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
