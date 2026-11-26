package runs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

const repo = "acme/self-service-iac-platform"

func validReport() Report {
	tok := int64(48213)
	return Report{
		Pipeline: KindValidate, StackPath: "environments/development/payments/order-events", RequestShortID: "req-7k3p9x",
		FencingToken: &tok, Stage: "summary", Status: StagePassed,
		GitHub: GitHubRun{RunID: 11873399021, RunAttempt: 1, JobID: 33412988710, HeadSHA: strings.Repeat("e4", 20)},
		Plan:   json.RawMessage(`{"hash":"3f9a"}`),
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*Report)
		pointers []string
	}{
		{name: "valid", edit: func(*Report) {}},
		{name: "drift needs no lock or request", edit: func(r *Report) {
			r.Pipeline, r.FencingToken, r.RequestShortID = KindDrift, nil, ""
		}},
		{name: "unknown pipeline", edit: func(r *Report) { r.Pipeline = "module_release" }, pointers: []string{"/pipeline"}},
		{name: "bad stack path", edit: func(r *Report) { r.StackPath = "../etc" }, pointers: []string{"/stack_path"}},
		{name: "bad short id", edit: func(r *Report) { r.RequestShortID = "nope" }, pointers: []string{"/request_short_id"}},
		{name: "bad run", edit: func(r *Report) { r.GitHub.RunID, r.GitHub.RunAttempt = 0, 0 },
			pointers: []string{"/github/run_id", "/github/run_attempt"}},
		{name: "bad sha", edit: func(r *Report) { r.GitHub.HeadSHA = "ABC" }, pointers: []string{"/github/head_sha"}},
		{name: "bad stage and status", edit: func(r *Report) { r.Stage, r.Status = "", "ok" }, pointers: []string{"/stage", "/status"}},
		{name: "apply without token or request", edit: func(r *Report) { r.Pipeline, r.FencingToken, r.RequestShortID = KindApply, nil, "" },
			pointers: []string{"/fencing_token", "/request_short_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validReport()
			tt.edit(&r)
			errs := Validate(r)
			if len(errs) != len(tt.pointers) {
				t.Fatalf("errors = %+v, want pointers %v", errs, tt.pointers)
			}
			for i, e := range errs {
				if e.Pointer != tt.pointers[i] {
					t.Fatalf("error %d = %+v, want %s", i, e, tt.pointers[i])
				}
			}
		})
	}
}

func TestCheckProvenance(t *testing.T) {
	events := map[string]string{"pr-validate.yml": "pull_request", "apply.yml": "push", "drift.yml": "schedule", "ttl-reaper.yml": "schedule"}
	wf := func(file, ref string) *identity.Workflow {
		return &identity.Workflow{Repository: repo, Ref: ref, JobWorkflowRef: repo + "/.github/workflows/" + file + "@" + ref,
			RunID: 11873399021, RunAttempt: 1, SHA: strings.Repeat("e4", 20), EventName: events[file]}
	}
	withEvent := func(w *identity.Workflow, event string) *identity.Workflow {
		w.EventName = event
		return w
	}
	tests := []struct {
		name     string
		pipeline Kind
		wf       *identity.Workflow
		code     string
	}{
		{name: "shared token skips provenance", pipeline: KindApply},
		{name: "validate from a PR", pipeline: KindValidate, wf: wf("pr-validate.yml", "refs/pull/812/merge")},
		{name: "apply from main", pipeline: KindApply, wf: wf("apply.yml", MainRef)},
		{name: "drift from main", pipeline: KindDrift, wf: wf("drift.yml", MainRef)},
		{name: "ttl from main", pipeline: KindTTLDestroy, wf: wf("ttl-reaper.yml", MainRef)},
		{name: "apply claimed by the PR workflow", pipeline: KindApply, wf: wf("pr-validate.yml", MainRef), code: "workflow_not_allowed"},
		{name: "apply from a branch", pipeline: KindApply, wf: wf("apply.yml", "refs/heads/feature"), code: "workflow_not_allowed"},
		{name: "apply from main but workflow file from a PR", pipeline: KindApply, wf: func() *identity.Workflow {
			w := wf("apply.yml", MainRef)
			w.JobWorkflowRef = repo + "/.github/workflows/apply.yml@refs/pull/9/merge"
			return w
		}(), code: "workflow_not_allowed"},
		{name: "another run's report", pipeline: KindValidate, wf: func() *identity.Workflow {
			w := wf("pr-validate.yml", "refs/pull/812/merge")
			w.RunID = 1
			return w
		}(), code: "run_mismatch"},
		{name: "another attempt's report", pipeline: KindValidate, wf: func() *identity.Workflow {
			w := wf("pr-validate.yml", "refs/pull/812/merge")
			w.RunAttempt = 2
			return w
		}(), code: "run_mismatch"},
		{name: "another commit's report", pipeline: KindValidate, wf: func() *identity.Workflow {
			w := wf("pr-validate.yml", "refs/pull/812/merge")
			w.SHA = strings.Repeat("ab", 20)
			return w
		}(), code: "run_mismatch"},
		{name: "apply dispatched by hand", pipeline: KindApply, wf: withEvent(wf("apply.yml", MainRef), "workflow_dispatch")},
		{name: "apply via workflow_run", pipeline: KindApply, wf: withEvent(wf("apply.yml", MainRef), "workflow_run"), code: "workflow_not_allowed"},
		{name: "apply via pull_request_target", pipeline: KindApply, wf: withEvent(wf("apply.yml", MainRef), "pull_request_target"), code: "workflow_not_allowed"},
		{name: "drift via push", pipeline: KindDrift, wf: withEvent(wf("drift.yml", MainRef), "push"), code: "workflow_not_allowed"},
		{name: "validate via pull_request_target", pipeline: KindValidate, wf: withEvent(wf("pr-validate.yml", "refs/pull/812/merge"), "pull_request_target"), code: "workflow_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validReport()
			r.Pipeline = tt.pipeline
			err := CheckProvenance(r, tt.wf)
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if e, ok := apperr.As(err); !ok || e.Code != tt.code || e.Kind != apperr.KindForbidden {
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}
}

func TestRunStatus(t *testing.T) {
	tests := []struct {
		stage  string
		status StageStatus
		want   Status
	}{
		{"plan", StageInProgress, StatusInProgress},
		{"plan", StagePassed, StatusInProgress},
		{"summary", StagePassed, StatusSucceeded},
		{"checkov", StageFailed, StatusFailed},
		{"summary", StageCancelled, StatusCancelled},
	}
	for _, tt := range tests {
		got := RunStatus(tt.stage, tt.status)
		if got != tt.want || got.Finished() != (tt.want != StatusInProgress) {
			t.Errorf("RunStatus(%s, %s) = %s", tt.stage, tt.status, got)
		}
	}
}

func TestMergeStages(t *testing.T) {
	stage := func(name, status string) json.RawMessage {
		return json.RawMessage(`{"name":"` + name + `","status":"` + status + `"}`)
	}
	names := func(ss []json.RawMessage) string {
		var out []string
		for _, s := range ss {
			out = append(out, string(s))
		}
		return strings.Join(out, ",")
	}
	base := []json.RawMessage{stage("plan", "in_progress"), stage("checkov", "passed")}

	got, err := MergeStages(base, "summary", stage("summary", "passed"))
	if err != nil || names(got) != names(append(append([]json.RawMessage(nil), base...), stage("summary", "passed"))) {
		t.Fatalf("append = %s, %v", names(got), err)
	}
	got, err = MergeStages(base, "plan", stage("plan", "passed"))
	if err != nil || names(got) != names([]json.RawMessage{stage("plan", "passed"), stage("checkov", "passed")}) {
		t.Fatalf("replace = %s, %v", names(got), err)
	}
	if string(base[0]) != string(stage("plan", "in_progress")) {
		t.Fatal("MergeStages must not modify its input")
	}
	// Replaying the same stage any number of times keeps one entry.
	again, _ := MergeStages(got, "plan", stage("plan", "passed"))
	if len(again) != 2 {
		t.Fatalf("replay = %d entries", len(again))
	}

	full := make([]json.RawMessage, MaxStages)
	for i := range full {
		full[i] = stage(fmt.Sprintf("s%d", i), "passed")
	}
	if _, err := MergeStages(full, "one-more", stage("one-more", "passed")); !hasCode(err, "too_many_stages") {
		t.Fatalf("cap: %v", err)
	}
	if got, err := MergeStages(full, "s3", stage("s3", "failed")); err != nil || len(got) != MaxStages {
		t.Fatalf("replace at the cap = %d, %v", len(got), err)
	}
	if _, err := MergeStages([]json.RawMessage{json.RawMessage(`[`)}, "x", stage("x", "passed")); err == nil {
		t.Fatal("a corrupt stored stage must fail")
	}
}

func hasCode(err error, code string) bool {
	e, ok := apperr.As(err)
	return ok && e.Code == code
}

func TestNextRunStatus(t *testing.T) {
	tests := []struct{ cur, reported, want Status }{
		{StatusInProgress, StatusSucceeded, StatusSucceeded},
		{StatusInProgress, StatusInProgress, StatusInProgress},
		{StatusSucceeded, StatusInProgress, StatusSucceeded},
		{StatusFailed, StatusSucceeded, StatusFailed},
	}
	for _, tt := range tests {
		if got := NextRunStatus(tt.cur, tt.reported); got != tt.want {
			t.Errorf("NextRunStatus(%s, %s) = %s", tt.cur, tt.reported, got)
		}
	}
}

func TestStageEntry(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.FixedZone("x", 3600))
	b, err := StageEntry(validReport(), at)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"summary","status":"passed","reported_at":"2026-11-24T09:00:00Z","job_id":33412988710,"plan":{"hash":"3f9a"}}`
	if string(b) != want {
		t.Fatalf("StageEntry = %s\nwant %s", b, want)
	}
}
