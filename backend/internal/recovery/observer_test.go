package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

func applyReport(stage string, status runs.StageStatus) runs.Report {
	tok := int64(7)
	return runs.Report{Pipeline: runs.KindApply, StackPath: "environments/development/payments/orders", RequestShortID: "req-7k3p9x",
		FencingToken: &tok, GitHub: runs.GitHubRun{RunID: 55, RunAttempt: 2, HeadSHA: "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4"},
		Stage: stage, Status: status, TerraformExitCode: ptr(1),
		Error: json.RawMessage(`{"class":"permanent","message":"api error ThrottlingException: Rate exceeded"}`)}
}

func TestFailedApplyFrom(t *testing.T) {
	f, ok := FailedApplyFrom(applyReport("apply", runs.StageFailed), "c1")
	if !ok || f.Stage != "apply" || f.Run.RunID != 55 || f.Run.Attempt != 2 || *f.ExitCode != 1 || *f.FencingToken != 7 ||
		f.Message != "api error ThrottlingException: Rate exceeded" || f.CorrelationID != "c1" {
		t.Fatalf("%+v %v", f, ok)
	}
	validate := applyReport("plan", runs.StageFailed)
	validate.Pipeline = runs.KindValidate
	noRequest := applyReport("apply", runs.StageFailed)
	noRequest.RequestShortID = ""
	for name, r := range map[string]runs.Report{
		"passed":     applyReport("apply", runs.StagePassed),
		"cancelled":  applyReport("apply", runs.StageCancelled),
		"validate":   validate,
		"no request": noRequest,
	} {
		if _, ok := FailedApplyFrom(r, ""); ok {
			t.Errorf("%s: must be ignored", name)
		}
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

type recordingFailures struct {
	got []FailedApply
	err error
}

func (r *recordingFailures) OnApplyFailure(_ context.Context, f FailedApply) (Outcome, error) {
	r.got = append(r.got, f)
	return Outcome{}, r.err
}

func TestReportObserver(t *testing.T) {
	rec := &recordingFailures{}
	o := NewReportObserver(rec)
	ctx := context.Background()
	if err := o.RunRecorded(ctx, identity.Principal{}, applyReport("plan", runs.StagePassed)); err != nil || len(rec.got) != 0 {
		t.Fatalf("passed stage: %v, %d", err, len(rec.got))
	}
	if err := o.RunRecorded(ctx, identity.Principal{}, applyReport("apply", runs.StageFailed)); err != nil || len(rec.got) != 1 {
		t.Fatalf("failed stage: %v, %d", err, len(rec.got))
	}
	if rec.got[0].CorrelationID != "run-55-2" {
		t.Fatalf("correlation id %q", rec.got[0].CorrelationID)
	}
	rec.err = errors.New("db down")
	if err := o.RunRecorded(ctx, identity.Principal{}, applyReport("apply", runs.StageFailed)); err == nil {
		t.Fatal("an observer failure must surface")
	}
}
