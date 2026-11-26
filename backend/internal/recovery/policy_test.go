package recovery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func failedProgress(detail string, runID int64) requests.ApplyProgress {
	return requests.ApplyProgress{Phase: requests.PhaseFailed, FailureReason: requests.FailureApplyFailed,
		FailureDetail: json.RawMessage(detail), Run: requests.RunRef{RunID: runID, Attempt: 1, HeadSHA: "e4c2", Stage: "summary"}}
}

const throttledDetail = `{"run_status":"failed","stage":"apply","stage_status":"failed","terraform_exit_code":1,
 "error":{"class":"permanent","message":"│ Error: api error ThrottlingException: Rate exceeded"}}`

func TestFailedApplyFromProgress(t *testing.T) {
	req := &requests.Request{ShortID: "req-7k3p9x", Stack: requests.StackRef{Path: "environments/development/payments/orders"}}
	f := FailedApplyFromProgress(req, failedProgress(throttledDetail, 41), "c1")
	if f.Stage != "apply" || *f.ExitCode != 1 || f.Message != "│ Error: api error ThrottlingException: Rate exceeded" ||
		f.Run.RunID != 41 || f.StackPath != req.Stack.Path || f.RequestShortID != "req-7k3p9x" || f.FencingToken != nil {
		t.Fatalf("%+v", f)
	}
	// No detail: the report's own stage, nothing to classify.
	f = FailedApplyFromProgress(req, failedProgress(``, 41), "c1")
	if f.Stage != "summary" || f.Message != "" || f.ExitCode != nil {
		t.Fatalf("%+v", f)
	}
	if got := Classify(Failure{Stage: f.Stage, Message: f.Message}); got.Class == ClassTransient {
		t.Fatal("a failure without detail must not be retried")
	}
}

func TestErrorMessage(t *testing.T) {
	cases := map[string]string{
		``:                                       "",
		`{"class":"transient","message":"boom"}`: "boom",
		`{"message":"Error: x","log_excerpt":"line"}`: "Error: x\nline",
		`"plain"`: "plain",
		`42`:      "",
	}
	for raw, want := range cases {
		if got := ErrorMessage(json.RawMessage(raw)); got != want {
			t.Errorf("ErrorMessage(%s) = %q, want %q", raw, got, want)
		}
	}
}

// The policy retries a transient failure, answers true again for the same run (the summary
// that follows the failed apply stage), and declines a config error.
func TestTxPolicy(t *testing.T) {
	tgt := applyingTarget()
	st := newMem(tgt)
	p := txPolicy{r: NewRetrier(&memRepo{s: st}, platform.FixedClock{T: now}, platform.RandomIDs{}), st: st}
	req := &requests.Request{ShortID: tgt.ShortID, Stack: requests.StackRef{Path: tgt.StackPath}}
	ctx := context.Background()

	ok, err := p.RetryIn(ctx, req, failedProgress(throttledDetail, 7), "c")
	if err != nil || !ok || st.target.RetryCount != 1 || len(st.jobs) != 1 {
		t.Fatalf("retry: %v %v count %d jobs %d", ok, err, st.target.RetryCount, len(st.jobs))
	}
	ok, err = p.RetryIn(ctx, req, failedProgress(throttledDetail, 7), "c")
	if err != nil || !ok || st.target.RetryCount != 1 || len(st.jobs) != 1 {
		t.Fatalf("same run again: %v %v count %d jobs %d", ok, err, st.target.RetryCount, len(st.jobs))
	}
	config := `{"stage":"apply","error":{"message":"Error: Invalid value for variable"}}`
	if ok, err := p.RetryIn(ctx, req, failedProgress(config, 8), "c"); err != nil || ok {
		t.Fatalf("config error: %v %v", ok, err)
	}
}
