package runs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func st(name string, s StageStatus) StageRecord { return StageRecord{Name: name, Status: s} }

func TestRunStatusAdvisoryStages(t *testing.T) {
	tests := []struct {
		stage  string
		status StageStatus
		want   Status
	}{
		{"smoke", StageFailed, StatusInProgress}, // does not fail the run
		{"smoke", StagePassed, StatusInProgress},
		{"smoke", StageCancelled, StatusCancelled},
		{"release", StageFailed, StatusInProgress}, // happens after the outcome is known
		{"release", StageCancelled, StatusInProgress},
		{"apply", StageFailed, StatusFailed},
		{"destroy", StageFailed, StatusFailed},
		{"plan", StageFailed, StatusFailed},
		{"summary", StageFailed, StatusFailed},
		{"summary", StagePassed, StatusSucceeded},
	}
	for _, tt := range tests {
		if got := RunStatus(tt.stage, tt.status); got != tt.want {
			t.Errorf("RunStatus(%s, %s) = %s, want %s", tt.stage, tt.status, got, tt.want)
		}
	}
}

func TestSmokeExplainsFailure(t *testing.T) {
	tests := []struct {
		name   string
		stages []StageRecord
		want   bool
	}{
		{"smoke alone failed after a passed apply", []StageRecord{st("plan", StagePassed), st("apply", StagePassed), st("smoke", StageFailed), st("summary", StageFailed)}, true},
		{"destroy counts as the mutating stage", []StageRecord{st("destroy", StagePassed), st("smoke", StageFailed), st("summary", StageFailed)}, true},
		{"release failures do not matter", []StageRecord{st("apply", StagePassed), st("smoke", StageFailed), st("summary", StageFailed), st("release", StageFailed)}, true},
		{"smoke passed: the summary failed for another reason", []StageRecord{st("apply", StagePassed), st("smoke", StagePassed), st("summary", StageFailed)}, false},
		{"no smoke stage at all", []StageRecord{st("apply", StagePassed), st("summary", StageFailed)}, false},
		{"apply failed", []StageRecord{st("apply", StageFailed), st("smoke", StageFailed), st("summary", StageFailed)}, false},
		{"no apply stage reported", []StageRecord{st("smoke", StageFailed), st("summary", StageFailed)}, false},
		{"plan failed too", []StageRecord{st("plan", StageFailed), st("apply", StagePassed), st("smoke", StageFailed)}, false},
		{"smoke was cancelled", []StageRecord{st("apply", StagePassed), st("smoke", StageCancelled), st("summary", StageFailed)}, false},
		{"apply still in progress", []StageRecord{st("apply", StageInProgress), st("smoke", StageFailed)}, false},
	}
	for _, tt := range tests {
		if got := SmokeExplainsFailure(tt.stages); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSettleRunStatus(t *testing.T) {
	explained := []StageRecord{st("apply", StagePassed), st("smoke", StageFailed), st("summary", StageFailed)}
	unexplained := []StageRecord{st("apply", StageFailed), st("summary", StageFailed)}
	tests := []struct {
		name     string
		kind     Kind
		reported Status
		stage    string
		stages   []StageRecord
		want     Status
	}{
		{"smoke-only failure succeeds with a degraded stack", KindApply, StatusFailed, "summary", explained, StatusSucceeded},
		{"a real failure stays failed", KindApply, StatusFailed, "summary", unexplained, StatusFailed},
		{"only the summary settles", KindApply, StatusFailed, "smoke", explained, StatusFailed},
		{"a passed summary is untouched", KindApply, StatusSucceeded, "summary", explained, StatusSucceeded},
		{"only the apply pipeline settles", KindValidate, StatusFailed, "summary", explained, StatusFailed},
	}
	for _, tt := range tests {
		if got := SettleRunStatus(tt.kind, tt.reported, tt.stage, tt.stages); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func applyReport(stage string, status StageStatus) Report {
	r := validReport()
	r.Pipeline, r.Stage, r.Status = KindApply, stage, status
	return r
}

func TestProgress(t *testing.T) {
	outputs := json.RawMessage(`{"queue_url":"http://q","secret":"<sensitive>"}`)
	withOutputs := StageRecord{Name: "summary", Status: StagePassed, Outputs: outputs}
	exit := 1
	failedApply := StageRecord{Name: "apply", Status: StageFailed, TerraformExitCode: &exit, Error: json.RawMessage(`{"class":"permanent","message":"boom"}`)}

	t.Run("in progress is started", func(t *testing.T) {
		p := Progress(applyReport("plan", StagePassed), StatusInProgress, []StageRecord{st("plan", StagePassed)})
		if p.Phase != requests.PhaseStarted || p.Run.RunID != 11873399021 || p.Run.Stage != "plan" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("success carries outputs and healthy smoke", func(t *testing.T) {
		stages := []StageRecord{st("plan", StagePassed), st("apply", StagePassed), st("smoke", StagePassed), withOutputs}
		p := Progress(applyReport("summary", StagePassed), StatusSucceeded, stages)
		if p.Phase != requests.PhaseSucceeded || p.Health != requests.HealthHealthy || string(p.Outputs) != string(outputs) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("a failed smoke test degrades the stack", func(t *testing.T) {
		stages := []StageRecord{st("apply", StagePassed), st("smoke", StageFailed), st("summary", StageFailed)}
		p := Progress(applyReport("summary", StageFailed), StatusSucceeded, stages)
		if p.Phase != requests.PhaseSucceeded || p.Health != requests.HealthDegraded {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("no smoke result is unknown health", func(t *testing.T) {
		p := Progress(applyReport("summary", StagePassed), StatusSucceeded, []StageRecord{st("apply", StagePassed), st("summary", StagePassed)})
		if p.Health != requests.HealthUnknown || len(p.Outputs) != 0 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("failure names the failing stage", func(t *testing.T) {
		p := Progress(applyReport("apply", StageFailed), StatusFailed, []StageRecord{st("plan", StagePassed), failedApply})
		var d map[string]any
		if err := json.Unmarshal(p.FailureDetail, &d); err != nil {
			t.Fatal(err)
		}
		if p.Phase != requests.PhaseFailed || p.FailureReason != requests.FailureApplyFailed || d["stage"] != "apply" ||
			d["terraform_exit_code"] != float64(1) || d["error"] == nil {
			t.Fatalf("%+v %v", p, d)
		}
	})
	t.Run("failure reasons by stage", func(t *testing.T) {
		for _, tc := range []struct {
			stages []StageRecord
			status Status
			want   string
		}{
			{[]StageRecord{st("plan", StageFailed)}, StatusFailed, requests.FailurePlanFailed},
			{[]StageRecord{st("apply", StagePassed), st("summary", StageFailed)}, StatusFailed, requests.FailureApplyIncomplete},
			{[]StageRecord{st("destroy", StageFailed)}, StatusFailed, requests.FailureApplyFailed},
			{[]StageRecord{st("plan", StagePassed), st("apply", StageCancelled)}, StatusCancelled, requests.FailureApplyCancelled},
			{[]StageRecord{st("smoke", StageFailed)}, StatusFailed, requests.FailureApplyIncomplete}, // advisory stages are never the culprit
		} {
			if p := Progress(applyReport("summary", StageFailed), tc.status, tc.stages); p.FailureReason != tc.want {
				t.Errorf("%v: reason %s, want %s", tc.stages, p.FailureReason, tc.want)
			}
		}
	})
	t.Run("release is only ever observed", func(t *testing.T) {
		r := applyReport("release", StageFailed)
		r.Error = json.RawMessage(`{"class":"permanent","message":"lock service down"}`)
		for _, rs := range []Status{StatusInProgress, StatusSucceeded, StatusFailed} {
			p := Progress(r, rs, []StageRecord{st("apply", StagePassed), st("summary", StagePassed), st("release", StageFailed)})
			if p.Phase != requests.PhaseObserved || p.ReleaseFailure != "lock service down" {
				t.Fatalf("run %s: %+v", rs, p)
			}
		}
		r.Error = nil
		if p := Progress(r, StatusSucceeded, nil); p.ReleaseFailure == "" {
			t.Fatal("a failed release without a message still counts as a failure")
		}
		if p := Progress(applyReport("release", StagePassed), StatusSucceeded, nil); p.ReleaseFailure != "" {
			t.Fatalf("a passed release is no failure: %+v", p)
		}
	})
}

func TestDrivesRequest(t *testing.T) {
	for k, want := range map[Kind]bool{KindApply: true, KindValidate: false, KindDrift: false, KindTTLDestroy: false} {
		if got := k.DrivesRequest(); got != want {
			t.Errorf("%s.DrivesRequest() = %v", k, got)
		}
	}
}

func TestAcceptsReleasedToken(t *testing.T) {
	tok, other := int64(7), int64(8)
	rep := applyReport("summary", StagePassed)
	rep.FencingToken = &tok
	terminal := RequestRef{Status: "applied", FencingToken: &tok}
	tests := []struct {
		name string
		r    func(Report) Report
		req  RequestRef
		want bool
	}{
		{"the request's own token, request applied", func(r Report) Report { return r }, terminal, true},
		{"failed request too", func(r Report) Report { return r }, RequestRef{Status: "failed", FencingToken: &tok}, true},
		{"a release report", func(r Report) Report { r.Stage = "release"; return r }, terminal, true},
		{"still applying: the lease must be current", func(r Report) Report { return r }, RequestRef{Status: "applying", FencingToken: &tok}, false},
		{"approved: the lease must be current", func(r Report) Report { return r }, RequestRef{Status: "approved", FencingToken: &tok}, false},
		{"another token", func(r Report) Report { r.FencingToken = &other; return r }, terminal, false},
		{"request never held a lease", func(r Report) Report { return r }, RequestRef{Status: "applied"}, false},
		{"not the apply pipeline", func(r Report) Report { r.Pipeline = KindValidate; return r }, terminal, false},
		{"no token", func(r Report) Report { r.FencingToken = nil; return r }, terminal, false},
	}
	for _, tt := range tests {
		if got := AcceptsReleasedToken(tt.r(rep), tt.req); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

func TestValidateOutputsMustBeAnObject(t *testing.T) {
	for _, tc := range []struct {
		raw string
		bad bool
	}{
		{`{"a":1}`, false}, {`{}`, false}, {`[1]`, true}, {`"x"`, true}, {`null`, true}, {`3`, true},
	} {
		r := validReport()
		r.Outputs = json.RawMessage(tc.raw)
		errs := Validate(r)
		if tc.bad != (len(errs) == 1 && errs[0].Pointer == "/outputs") || (!tc.bad && len(errs) != 0) {
			t.Errorf("outputs %s: errors %+v", tc.raw, errs)
		}
	}
	r := validReport()
	if errs := Validate(r); len(errs) != 0 || strings.Contains(string(r.Outputs), "x") {
		t.Fatalf("absent outputs are fine: %+v", errs)
	}
}
