// Package drift records and resolves drift reports (WBS 8.3, docs/06 §4).
//
// This file holds the pure rules: report validation, classification of a terraform exit
// code, the stack health table and the resolution state machine. Service orchestrates them
// and talks to the database and the request path through ports.
package drift

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Result is what one drift check found (enum drift_result).
type Result string

// Results.
const (
	ResultClean   Result = "clean"
	ResultDrifted Result = "drifted"
	ResultError   Result = "error"
)

// Status is the resolution state of a drifted report (enum drift_status).
type Status string

// Statuses. Only a report with result drifted has one.
const (
	StatusOpen       Status = "open"
	StatusReconciled Status = "reconciled"
	StatusIgnored    Status = "ignored"
	StatusCodified   Status = "codified"
)

// Health is the stack's drift health (enum stack_health). It is independent of the
// lifecycle status (requests.StackStatus): drift never moves the lifecycle.
type Health string

// Health values.
const (
	HealthHealthy Health = "healthy"
	HealthDrifted Health = "drifted"
)

// Action is what a user does with an open report.
type Action string

// Actions.
const (
	ActionReconcile Action = "reconcile"
	ActionCodify    Action = "codify"
	ActionIgnore    Action = "ignore"
)

// Actions lists every resolution action.
var Actions = []Action{ActionReconcile, ActionCodify, ActionIgnore}

// Terraform exit codes of `plan -detailed-exitcode`.
const (
	ExitClean   = 0
	ExitError   = 1
	ExitChanges = 2
)

// ClassifyExit maps a `terraform plan -refresh-only -detailed-exitcode` exit code and the
// number of drifted resources its plan lists to a result: 0 is no drift, 2 is drift, 1 is a
// failed check (not drift, docs/06 §4). A refresh-only plan also exits 2 when only output
// values would change; with no drifted resource that is not drift, so it is clean. Any other
// code is not a terraform outcome. Pure.
func ClassifyExit(code, drifted int) (Result, bool) {
	switch code {
	case ExitClean:
		return ResultClean, true
	case ExitChanges:
		if drifted == 0 {
			return ResultClean, true
		}
		return ResultDrifted, true
	case ExitError:
		return ResultError, true
	}
	return "", false
}

// NextHealth is the stack health after a check: drift marks it drifted, a clean check
// restores it, and a failed check says nothing about the stack, so health is kept. Pure.
func NextHealth(current Health, r Result) Health {
	switch r {
	case ResultDrifted:
		return HealthDrifted
	case ResultClean:
		return HealthHealthy
	}
	return current
}

// Checkable reports whether a stack in lifecycle status is scanned and may receive a drift
// report: it must have settled after an apply. A stack that is provisioning, updating,
// destroying or failed has a state that is mid-change or not trustworthy, so a refresh-only
// plan would be misleading (docs/06 §4). Pure.
func Checkable(lifecycleStatus string) bool {
	return lifecycleStatus == "active" || lifecycleStatus == "drifted"
}

// Allowed reports the status a resolution action leads to from current; false when the
// report is not open (a resolved report is final) or the action is unknown. Pure.
func Allowed(current Status, a Action) (Status, bool) {
	if current != StatusOpen {
		return "", false
	}
	switch a {
	case ActionReconcile:
		return StatusReconciled, true
	case ActionCodify:
		return StatusCodified, true
	case ActionIgnore:
		return StatusIgnored, true
	}
	return "", false
}

// StatusFor is the resolved status of an action, regardless of the current state. Pure.
func StatusFor(a Action) (Status, bool) { return Allowed(StatusOpen, a) }

// RBACAction is the permission matrix action (docs/07 §7) a resolution action needs:
// reconcile and codify are developer actions, ignore needs a maintainer. Pure.
func RBACAction(a Action) rbac.Action {
	if a == ActionIgnore {
		return rbac.ActionDriftIgnore
	}
	return rbac.ActionDriftReconcile
}

// MaxChanged bounds the changed-resource list one report may carry, and the reason length.
const (
	MaxChanged   = 200
	MaxReasonLen = 500
	minReasonLen = 8
)

// ChangedResource is one drifted resource of a refresh-only plan.
type ChangedResource struct {
	Address string   `json:"address"`
	Type    string   `json:"type"`
	Actions []string `json:"actions"`
}

// Summary holds the resource counts of a report. Pure data.
type Summary struct {
	Drifted int `json:"drifted"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

// GitHubRun identifies the reporting workflow run.
type GitHubRun struct {
	RunID      int64  `json:"run_id"`
	RunAttempt int    `json:"run_attempt"`
	JobID      int64  `json:"job_id,omitempty"`
	HeadSHA    string `json:"head_sha"`
}

// Report is the body of POST /v1/internal/drift: the outcome of one check of one stack.
type Report struct {
	StackPath         string            `json:"stack_path"`
	GitHub            GitHubRun         `json:"github"`
	Result            Result            `json:"result"`
	TerraformExitCode int               `json:"terraform_exit_code"`
	Summary           Summary           `json:"summary"`
	Changed           []ChangedResource `json:"changed"`
	PlanHash          string            `json:"plan_hash,omitempty"`
	LogURL            string            `json:"log_url,omitempty"`
	Error             string            `json:"error,omitempty"`
}

var (
	stackPathRe = regexp.MustCompile(`^environments/[a-z]+/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}$`)
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hashRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	addressRe   = regexp.MustCompile(`^[A-Za-z0-9_.\[\]"'/:-]{1,512}$`)
	actionRe    = regexp.MustCompile(`^[a-z-]{1,16}$`)
)

// MaxErrorLen bounds the failure message of an error report.
const MaxErrorLen = 1000

// Validate checks the report shape and that its parts agree: the result must be the one the
// exit code means, the counts must match the changed list, and only a drifted report
// carries resources. Pure.
func Validate(r Report) []apperr.FieldError {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}
	if !stackPathRe.MatchString(r.StackPath) {
		add("/stack_path", "pattern", "must look like environments/<env>/<team>/<stack>")
	}
	if r.GitHub.RunID <= 0 {
		add("/github/run_id", "minimum", "must be a positive integer")
	}
	if r.GitHub.RunAttempt <= 0 {
		add("/github/run_attempt", "minimum", "must be a positive integer")
	}
	if !shaRe.MatchString(r.GitHub.HeadSHA) {
		add("/github/head_sha", "pattern", "must be 40 lowercase hex characters")
	}
	want, known := ClassifyExit(r.TerraformExitCode, r.Summary.Drifted)
	switch {
	case !known:
		add("/terraform_exit_code", "enum", "must be 0, 1 or 2 (terraform plan -detailed-exitcode)")
	case r.Result != want:
		add("/result", "mismatch", fmt.Sprintf("exit code %d with %d drifted resources means %q, not %q",
			r.TerraformExitCode, r.Summary.Drifted, want, r.Result))
	}
	if r.PlanHash != "" && !hashRe.MatchString(r.PlanHash) {
		add("/plan_hash", "pattern", "must be 64 lowercase hex characters")
	}
	if len(r.Error) > MaxErrorLen {
		add("/error", "maxLength", fmt.Sprintf("must be at most %d characters", MaxErrorLen))
	}
	if len(r.LogURL) > 2048 {
		add("/log_url", "maxLength", "must be at most 2048 characters")
	}
	validateFindings(r, add)
	return errs
}

// validateFindings checks the counts and the changed list against the result; add
// collects the errors.
func validateFindings(r Report, add func(ptr, code, msg string)) {
	s := r.Summary
	if s.Drifted < 0 || s.Updated < 0 || s.Deleted < 0 {
		add("/summary", "minimum", "counts must not be negative")
	}
	switch r.Result {
	case ResultDrifted:
		if s.Drifted <= 0 {
			add("/summary/drifted", "minimum", "a drifted report must count at least one drifted resource")
		}
		if s.Updated+s.Deleted > s.Drifted {
			add("/summary", "inconsistent", "updated and deleted cannot exceed drifted")
		}
		if r.PlanHash == "" {
			add("/plan_hash", "required", "a drifted report must carry the plan hash")
		}
	case ResultClean, ResultError:
		if s != (Summary{}) || len(r.Changed) > 0 {
			add("/summary", "not_allowed", fmt.Sprintf("a %s report carries no drifted resources", r.Result))
		}
	}
	if r.Result == ResultError && strings.TrimSpace(r.Error) == "" {
		add("/error", "required", "an error report must say what failed")
	}
	if r.Result != ResultError && r.Error != "" {
		add("/error", "not_allowed", "only an error report carries an error")
	}
	if len(r.Changed) > MaxChanged {
		add("/changed", "maxItems", fmt.Sprintf("at most %d changed resources; the summary carries the totals", MaxChanged))
	}
	for i, c := range r.Changed {
		ptr := fmt.Sprintf("/changed/%d", i)
		if !addressRe.MatchString(c.Address) {
			add(ptr+"/address", "pattern", "must be a terraform resource address")
		}
		if len(c.Type) > 128 {
			add(ptr+"/type", "maxLength", "must be at most 128 characters")
		}
		if len(c.Actions) == 0 || len(c.Actions) > 4 || slices.ContainsFunc(c.Actions, func(a string) bool { return !actionRe.MatchString(a) }) {
			add(ptr+"/actions", "pattern", "must list one to four terraform actions")
		}
	}
}

// ResolveInput is the body of POST /v1/drift/{id}/resolve.
type ResolveInput struct {
	Action Action `json:"action"`
	// Reason is required for ignore (the audit trail must say why a finding is accepted) and
	// optional for the other actions.
	Reason string `json:"reason,omitempty"`
}

// ValidateResolve checks a resolve call. Pure.
func ValidateResolve(in ResolveInput) []apperr.FieldError {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}
	if !slices.Contains(Actions, in.Action) {
		add("/action", "enum", "must be one of reconcile, codify, ignore")
	}
	reason := strings.TrimSpace(in.Reason)
	switch {
	case len(in.Reason) > MaxReasonLen:
		add("/reason", "maxLength", fmt.Sprintf("must be at most %d characters", MaxReasonLen))
	case in.Action == ActionIgnore && len([]rune(reason)) < minReasonLen:
		add("/reason", "required", fmt.Sprintf("ignoring drift needs a justification of at least %d characters", minReasonLen))
	}
	return errs
}

// ReconcileJustification is the justification of the update request a reconcile opens. Pure.
func ReconcileJustification(reason, stackPath string) string {
	if r := strings.TrimSpace(reason); r != "" {
		return "Reconcile drift: " + r
	}
	return "Reconcile drift detected on " + stackPath
}
