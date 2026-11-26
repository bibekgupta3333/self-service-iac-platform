package runs

import (
	"encoding/json"
	"fmt"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Stage names apply.yml reports (docs/05 §4.3). The mutating stage is "apply", or "destroy"
// when the workflow was dispatched with destroy: true.
const (
	StagePlanName    = "plan"
	StageApplyName   = "apply"
	StageDestroyName = "destroy"
	StageSmokeName   = "smoke"
	StageReleaseName = "release"
)

// StageRecord is a stored stage entry reduced to what verdicts need.
type StageRecord struct {
	Name              string          `json:"name"`
	Status            StageStatus     `json:"status"`
	Outputs           json.RawMessage `json:"outputs,omitempty"`
	TerraformExitCode *int            `json:"terraform_exit_code,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
}

// ParseStages decodes stored stage entries (pipeline_runs.stages).
func ParseStages(raw []json.RawMessage) ([]StageRecord, error) {
	out := make([]StageRecord, 0, len(raw))
	for _, r := range raw {
		var s StageRecord
		if err := json.Unmarshal(r, &s); err != nil {
			return nil, fmt.Errorf("decode stored stage: %w", err)
		}
		out = append(out, s)
	}
	return out, nil
}

func (s StageRecord) mutates() bool { return s.Name == StageApplyName || s.Name == StageDestroyName }

func (s StageRecord) advisory() bool { return s.Name == StageSmokeName || s.Name == StageReleaseName }

// failedOrCancelled reports whether the stage ended badly.
func (s StageRecord) failedOrCancelled() bool {
	return s.Status == StageFailed || s.Status == StageCancelled
}

// SmokeExplainsFailure reports whether a failed summary of an apply run is only the job
// failing because of the smoke test: the mutating stage passed, the smoke stage failed, and
// no other stage (release aside) failed or was cancelled. apply.yml reports the summary
// from job.status, so a failed smoke step turns the whole job red even though the stack was
// applied; docs/05 step 12 wants that stack active with health degraded, not a failed run.
// Pure.
func SmokeExplainsFailure(stages []StageRecord) bool {
	applied, smokeFailed := false, false
	for _, s := range stages {
		switch {
		case s.mutates():
			if s.Status != StagePassed {
				return false
			}
			applied = true
		case s.Name == StageSmokeName:
			smokeFailed = smokeFailed || s.Status == StageFailed
			if s.Status == StageCancelled {
				return false
			}
		case s.Name == FinalStage || s.Name == StageReleaseName:
		case s.failedOrCancelled():
			return false
		}
	}
	return applied && smokeFailed
}

// SettleRunStatus is the run status after a report of stage with the per-report status
// reported, once the run's stages (including this one) are known. It only differs from the
// per-report status when a failed summary is explained by the smoke test alone, in which
// case the run succeeded with a degraded stack. Pure.
func SettleRunStatus(kind Kind, reported Status, stage string, stages []StageRecord) Status {
	if kind == KindApply && stage == FinalStage && reported == StatusFailed && SmokeExplainsFailure(stages) {
		return StatusSucceeded
	}
	return reported
}

// healthOf is the stack health the stages imply: the smoke test's verdict, unknown when it
// did not run. Pure.
func healthOf(stages []StageRecord) requests.StackHealth {
	h := requests.HealthUnknown
	for _, s := range stages {
		if s.Name != StageSmokeName {
			continue
		}
		switch s.Status {
		case StagePassed:
			h = requests.HealthHealthy
		case StageFailed:
			h = requests.HealthDegraded
		}
	}
	return h
}

// outputsOf is the outputs of the apply: the last stage that carried a non-empty object. Pure.
func outputsOf(stages []StageRecord) json.RawMessage {
	var out json.RawMessage
	for _, s := range stages {
		if len(s.Outputs) > 0 && string(s.Outputs) != "null" {
			out = s.Outputs
		}
	}
	return out
}

// failureOf names why a finished run did not apply: the first failed stage that is not the
// smoke test or the lock release, else the summary. Pure.
func failureOf(runStatus Status, stages []StageRecord) (reason string, detail json.RawMessage) {
	var culprit *StageRecord
	for i := range stages {
		if s := &stages[i]; !s.advisory() && s.failedOrCancelled() {
			culprit = s
			break
		}
	}
	reason = requests.FailureApplyFailed
	switch {
	case runStatus == StatusCancelled:
		reason = requests.FailureApplyCancelled
	case culprit == nil:
		reason = requests.FailureApplyIncomplete
	case culprit.Name == StagePlanName:
		reason = requests.FailurePlanFailed
	case culprit.Name == FinalStage:
		reason = requests.FailureApplyIncomplete
	}
	d := map[string]any{"run_status": runStatus}
	if culprit != nil {
		d["stage"], d["stage_status"] = culprit.Name, culprit.Status
		if len(culprit.Error) > 0 {
			d["error"] = culprit.Error
		}
		if culprit.TerraformExitCode != nil {
			d["terraform_exit_code"] = *culprit.TerraformExitCode
		}
	}
	detail, _ = json.Marshal(d) // maps of plain values cannot fail to marshal
	return reason, detail
}

// releaseMessage is the error text of a failed release report. Pure.
func releaseMessage(r Report) string {
	var e struct {
		Message string `json:"message"`
	}
	if len(r.Error) > 0 {
		_ = json.Unmarshal(r.Error, &e)
	}
	if e.Message == "" {
		return "the stack lock could not be released"
	}
	return e.Message
}

// Progress is what an apply-pipeline report (r, which left its run in runStatus with the
// given stages) tells the lifecycle about the request. The mapping:
//
//   - release: never a verdict. A failed release is observed (audited, lock state
//     surfaced) and cannot flip an applied request back to failed.
//   - run still in progress: started.
//   - run succeeded: succeeded, with health from the smoke stage and the reported outputs.
//   - run failed or cancelled: failed, with the failing stage as the reason.
//
// A validate pipeline never reaches this function: a green validation is not an approval.
// Pure.
func Progress(r Report, runStatus Status, stages []StageRecord) requests.ApplyProgress {
	p := requests.ApplyProgress{Run: requests.RunRef{RunID: r.GitHub.RunID, Attempt: r.GitHub.RunAttempt,
		HeadSHA: r.GitHub.HeadSHA, Stage: r.Stage}}
	switch {
	case r.Stage == StageReleaseName:
		p.Phase = requests.PhaseObserved
		if r.Status == StageFailed {
			p.ReleaseFailure = releaseMessage(r)
		}
	case runStatus == StatusSucceeded:
		p.Phase, p.Health, p.Outputs = requests.PhaseSucceeded, healthOf(stages), outputsOf(stages)
	case runStatus.Finished():
		p.Phase = requests.PhaseFailed
		p.FailureReason, p.FailureDetail = failureOf(runStatus, stages)
	default:
		p.Phase = requests.PhaseStarted
	}
	return p
}

// DrivesRequest reports whether reports of the pipeline move requests and stacks. Only the
// apply pipeline does; validate reports are stored and nothing more. Pure.
func (k Kind) DrivesRequest() bool { return k == KindApply }

// AcceptsReleasedToken reports whether an apply report may skip the current-lease check
// because the request it names is terminal and the report carries the very token that
// request was granted. The backend releases the lock itself when the request ends, so by
// the time apply.yml sends its summary replay, or reports that its own release failed, the
// lease is gone and the normal check would answer stale_fencing_token. Such a report is
// recorded in its run and can change nothing: a terminal request ignores it (and a failed
// release is audited), so the exception cannot be used to move state. Any other token,
// including one from a previous holder of the stack, still fails. Pure.
func AcceptsReleasedToken(r Report, req RequestRef) bool {
	return r.Pipeline == KindApply && r.FencingToken != nil && req.FencingToken != nil &&
		*r.FencingToken == *req.FencingToken && requests.Status(req.Status).IsTerminal()
}
