package recovery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

// FailureHandler handles a failed apply stage (Retrier).
type FailureHandler interface {
	OnApplyFailure(ctx context.Context, f FailedApply) (Outcome, error)
}

// ReportObserver connects run reports (POST /v1/runs) to the Retrier: every failed stage
// of an apply run that names its request is classified, and a transient one may be
// retried. It implements httpapi.RunObserver and runs after the report committed.
type ReportObserver struct{ failures FailureHandler }

// NewReportObserver returns a ReportObserver.
func NewReportObserver(f FailureHandler) *ReportObserver { return &ReportObserver{failures: f} }

// FailedApplyFrom maps a run report to a FailedApply; ok is false for reports recovery does
// not handle (another pipeline, a stage that did not fail, no request). Pure.
func FailedApplyFrom(r runs.Report, correlationID string) (FailedApply, bool) {
	if r.Pipeline != runs.KindApply || r.Status != runs.StageFailed || r.RequestShortID == "" {
		return FailedApply{}, false
	}
	return FailedApply{
		RequestShortID: r.RequestShortID, StackPath: r.StackPath, FencingToken: r.FencingToken,
		Run:   RunRef{RunID: r.GitHub.RunID, Attempt: r.GitHub.RunAttempt, HeadSHA: r.GitHub.HeadSHA},
		Stage: r.Stage, ExitCode: r.TerraformExitCode, Message: ErrorMessage(r.Error), LogURL: r.LogURL,
		CorrelationID: correlationID,
	}, true
}

// ErrorMessage is the message of a report's error block ({"class": ..., "message": ...}),
// or the raw value when it is a plain JSON string. Pure.
func ErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var block struct {
		Message    string `json:"message"`
		LogExcerpt string `json:"log_excerpt"`
	}
	if err := json.Unmarshal(raw, &block); err == nil {
		if block.LogExcerpt != "" {
			return block.Message + "\n" + block.LogExcerpt
		}
		return block.Message
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// RunRecorded implements httpapi.RunObserver.
func (o *ReportObserver) RunRecorded(ctx context.Context, _ identity.Principal, r runs.Report) error {
	f, ok := FailedApplyFrom(r, fmt.Sprintf("run-%d-%d", r.GitHub.RunID, r.GitHub.RunAttempt))
	if !ok {
		return nil
	}
	if _, err := o.failures.OnApplyFailure(ctx, f); err != nil {
		return fmt.Errorf("classify failed %s stage of %s: %w", f.Stage, f.RequestShortID, err)
	}
	return nil
}
