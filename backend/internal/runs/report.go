// Package runs records GitHub Actions run reports (POST /v1/runs, docs/03 §6.9).
//
// Validation, provenance and status rules are pure functions in this file. Service
// checks the fencing token against the stack lock and persists through a Store.
package runs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// Kind is the pipeline that reports (enum pipeline_kind).
type Kind string

// Pipelines that report through POST /v1/runs. Module releases report through
// /v1/internal/catalog/index instead.
const (
	KindValidate   Kind = "validate"
	KindApply      Kind = "apply"
	KindDrift      Kind = "drift"
	KindTTLDestroy Kind = "ttl_destroy"
)

// workflowFiles maps each pipeline to the only workflow allowed to report it (docs/05).
var workflowFiles = map[Kind]string{
	KindValidate:   "pr-validate.yml",
	KindApply:      "apply.yml",
	KindDrift:      "drift.yml",
	KindTTLDestroy: "ttl-reaper.yml",
}

// MainRef is the ref every mutating or scheduled pipeline must run from.
const MainRef = identity.MainRef

// NeedsMain reports whether the pipeline must run from main (docs/03 §6.9). Pure.
func (k Kind) NeedsMain() bool { return k != KindValidate }

// NeedsLock reports whether the pipeline acts under the stack lock and must present
// the current fencing token. Drift checks are read-only and hold no lock. Pure.
func (k Kind) NeedsLock() bool { return k != KindDrift }

// StageStatus is the outcome of one reported stage.
type StageStatus string

// Stage statuses.
const (
	StageInProgress StageStatus = "in_progress"
	StagePassed     StageStatus = "passed"
	StageFailed     StageStatus = "failed"
	StageCancelled  StageStatus = "cancelled"
)

// Status is the status of a whole run (enum pipeline_status).
type Status string

// Run statuses.
const (
	StatusInProgress Status = "in_progress"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// FinalStage is the stage name that closes a successful run ("summary" in docs/03 §6.9).
const FinalStage = "summary"

// RunStatus derives the run status from one stage report. A passed stage only finishes
// the run when it is the final summary; a failed or cancelled stage always does. Pure.
func RunStatus(stage string, s StageStatus) Status {
	switch s {
	case StageFailed:
		return StatusFailed
	case StageCancelled:
		return StatusCancelled
	case StagePassed:
		if stage == FinalStage {
			return StatusSucceeded
		}
	}
	return StatusInProgress
}

// Finished reports whether the run status is final. Pure.
func (s Status) Finished() bool { return s != StatusInProgress }

// GitHubRun identifies the reporting workflow run.
type GitHubRun struct {
	RunID      int64  `json:"run_id"`
	RunAttempt int    `json:"run_attempt"`
	JobID      int64  `json:"job_id,omitempty"`
	HeadSHA    string `json:"head_sha"`
}

// Report is the body of POST /v1/runs.
type Report struct {
	Pipeline          Kind            `json:"pipeline"`
	StackPath         string          `json:"stack_path"`
	RequestShortID    string          `json:"request_short_id,omitempty"`
	FencingToken      *int64          `json:"fencing_token,omitempty"`
	GitHub            GitHubRun       `json:"github"`
	Stage             string          `json:"stage"`
	Status            StageStatus     `json:"status"`
	LogURL            string          `json:"log_url,omitempty"`
	Plan              json.RawMessage `json:"plan,omitempty"`
	Policy            json.RawMessage `json:"policy,omitempty"`
	Cost              json.RawMessage `json:"cost,omitempty"`
	Outputs           json.RawMessage `json:"outputs,omitempty"`
	TerraformExitCode *int            `json:"terraform_exit_code,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
}

var (
	stackPathRe = regexp.MustCompile(`^environments/[a-z]+/[a-z][a-z0-9-]{1,38}/[a-z][a-z0-9-]{1,40}$`)
	shortIDRe   = regexp.MustCompile(`^req-[0-9a-z]{4,32}$`)
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	stageRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
)

// Validate checks the report shape. Pure.
func Validate(r Report) []apperr.FieldError {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}
	if _, ok := workflowFiles[r.Pipeline]; !ok {
		add("/pipeline", "enum", "must be one of validate, apply, drift, ttl_destroy")
	}
	if !stackPathRe.MatchString(r.StackPath) {
		add("/stack_path", "pattern", "must look like environments/<env>/<team>/<stack>")
	}
	if r.RequestShortID != "" && !shortIDRe.MatchString(r.RequestShortID) {
		add("/request_short_id", "pattern", "must look like req-<id>")
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
	if !stageRe.MatchString(r.Stage) {
		add("/stage", "pattern", "must be a short lowercase stage name")
	}
	if !slices.Contains([]StageStatus{StageInProgress, StagePassed, StageFailed, StageCancelled}, r.Status) {
		add("/status", "enum", "must be one of in_progress, passed, failed, cancelled")
	}
	if r.Pipeline.NeedsLock() && (r.FencingToken == nil || *r.FencingToken <= 0) {
		add("/fencing_token", "required", fmt.Sprintf("%s reports must carry the stack lock's fencing token", r.Pipeline))
	}
	if r.Pipeline.NeedsLock() && r.RequestShortID == "" {
		add("/request_short_id", "required", fmt.Sprintf("%s reports must name their request", r.Pipeline))
	}
	return errs
}

// CheckProvenance matches a report against the verified OIDC identity of its caller:
// the run must be the caller's own, the workflow must be the one that owns the pipeline,
// and mutating or scheduled pipelines must run from main. A nil workflow (shared internal
// token) skips these checks. Pure.
func CheckProvenance(r Report, wf *identity.Workflow) error {
	if wf == nil {
		return nil
	}
	if r.GitHub.RunID != wf.RunID || r.GitHub.RunAttempt != wf.RunAttempt {
		return &apperr.Error{Kind: apperr.KindForbidden, Code: "run_mismatch",
			Message: fmt.Sprintf("report for run %d/%d sent by run %d/%d", r.GitHub.RunID, r.GitHub.RunAttempt, wf.RunID, wf.RunAttempt)}
	}
	if want := workflowFiles[r.Pipeline]; wf.File() != want {
		return workflowNotAllowed(fmt.Sprintf("%s reports must come from %s, not %s", r.Pipeline, want, wf.JobWorkflowRef))
	}
	if r.Pipeline.NeedsMain() && !wf.OnMain() {
		return workflowNotAllowed(fmt.Sprintf("%s reports must run from %s", r.Pipeline, MainRef))
	}
	return nil
}

func workflowNotAllowed(msg string) error {
	return &apperr.Error{Kind: apperr.KindForbidden, Code: "workflow_not_allowed", Message: msg}
}

// stageEntry is one element of pipeline_runs.stages.
type stageEntry struct {
	Name              string          `json:"name"`
	Status            StageStatus     `json:"status"`
	ReportedAt        time.Time       `json:"reported_at"`
	JobID             int64           `json:"job_id,omitempty"`
	Plan              json.RawMessage `json:"plan,omitempty"`
	Policy            json.RawMessage `json:"policy,omitempty"`
	Cost              json.RawMessage `json:"cost,omitempty"`
	Outputs           json.RawMessage `json:"outputs,omitempty"`
	TerraformExitCode *int            `json:"terraform_exit_code,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
}

// StageEntry is the stage record appended to the run for report r at time at. Pure.
func StageEntry(r Report, at time.Time) (json.RawMessage, error) {
	b, err := json.Marshal(stageEntry{Name: r.Stage, Status: r.Status, ReportedAt: at.UTC(), JobID: r.GitHub.JobID,
		Plan: r.Plan, Policy: r.Policy, Cost: r.Cost, Outputs: r.Outputs, TerraformExitCode: r.TerraformExitCode, Error: r.Error})
	if err != nil {
		return nil, fmt.Errorf("encode stage: %w", err)
	}
	return b, nil
}
