package recovery

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tferror"
)

// TxStores binds a RetryStore to an existing transaction (PostgresRepository).
type TxStores interface {
	StoreFor(tx pgx.Tx) RetryStore
}

// Policy returns the requests.RetryPolicy factory the apply driver calls for each run
// report (requests.ApplyDriver.WithRetry): the retry decision runs in the report's own
// transaction, while the request still holds its stack lock, before a failed apply would
// fail the request.
func (r *Retrier) Policy(stores TxStores) func(tx pgx.Tx) requests.RetryPolicy {
	return func(tx pgx.Tx) requests.RetryPolicy { return txPolicy{r: r, st: stores.StoreFor(tx)} }
}

type txPolicy struct {
	r  *Retrier
	st RetryStore
}

// RetryIn implements requests.RetryPolicy. It answers true when a retry was scheduled now,
// or was scheduled by an earlier report of the same run (a replay, or the summary that
// follows the failed apply stage).
func (p txPolicy) RetryIn(ctx context.Context, req *requests.Request, prog requests.ApplyProgress, correlationID string) (bool, error) {
	out, err := p.r.decideIn(ctx, p.st, FailedApplyFromProgress(req, prog, correlationID))
	if err != nil {
		return false, err
	}
	return out.Decision == DecisionRetryScheduled || out.Duplicate, nil
}

// FailedApplyFromProgress maps a failed apply progress (runs.Progress: the run, and the
// failure detail with the failing stage, its error block and exit code) to a FailedApply.
// No fencing token is passed: the report was already checked against the stack lock in
// the same transaction. Pure.
func FailedApplyFromProgress(req *requests.Request, p requests.ApplyProgress, correlationID string) FailedApply {
	var d struct {
		Stage    string          `json:"stage"`
		Error    json.RawMessage `json:"error"`
		ExitCode *int            `json:"terraform_exit_code"`
	}
	if len(p.FailureDetail) > 0 {
		_ = json.Unmarshal(p.FailureDetail, &d) // a detail we cannot read classifies as unknown
	}
	stage := d.Stage
	if stage == "" {
		stage = p.Run.Stage
	}
	return FailedApply{
		RequestShortID: req.ShortID, StackPath: req.Stack.Path,
		Run:   RunRef{RunID: p.Run.RunID, Attempt: p.Run.Attempt, HeadSHA: p.Run.HeadSHA},
		Stage: stage, ExitCode: d.ExitCode, Message: ErrorMessage(d.Error), CorrelationID: correlationID,
	}
}

// ErrorMessage is the text Classify reads for a report's error block ({"class",
// "message", "signals"}), or for a plain JSON string: the message reduced to its redacted
// Terraform "Error:" lines (tferror.Clean), followed by the signals, one per line. Signals
// are the reported ones that are vocabulary entries (tferror.FilterSignals) plus those
// found in the message itself, so a block without a signals field classifies too. Pure.
func ErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var block struct {
		Message string   `json:"message"`
		Signals []string `json:"signals"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		if err := json.Unmarshal(raw, &block.Message); err != nil {
			return ""
		}
	}
	return ClassificationText(block.Message, block.Signals)
}

// ClassificationText joins a cleaned message and its vocabulary signals for Classify. Pure.
func ClassificationText(message string, reported []string) string {
	signals := tferror.FilterSignals(append(append([]string(nil), reported...), tferror.Signals(message)...))
	parts := make([]string, 0, 1+len(signals))
	if strings.TrimSpace(message) != "" {
		parts = append(parts, tferror.Clean(message, 3, 500))
	}
	return strings.Join(append(parts, signals...), "\n")
}
