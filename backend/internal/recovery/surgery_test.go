package recovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestValidateSurgeryArgs(t *testing.T) {
	cases := []struct {
		name string
		op   string
		args []string
		ok   bool
	}{
		{"rm one", OpStateRm, []string{"module.queue.aws_sqs_queue.this"}, true},
		{"rm several", OpStateRm, []string{"aws_s3_bucket.logs", `module.buckets["eu-west-1"].aws_s3_bucket.this[0]`}, true},
		{"rm data source", OpStateRm, []string{"module.vpc.data.aws_availability_zones.available"}, true},
		{"rm none", OpStateRm, nil, false},
		{"rm too many", OpStateRm, strings.Split(strings.Repeat("aws_sqs_queue.q ", 11), " ")[:11], false},
		{"mv", OpStateMv, []string{"module.queue.aws_sqs_queue.this", "module.orders.aws_sqs_queue.this"}, true},
		{"mv to itself", OpStateMv, []string{"aws_sqs_queue.a", "aws_sqs_queue.a"}, false},
		{"mv one", OpStateMv, []string{"aws_sqs_queue.a"}, false},
		{"import queue url", OpImport, []string{"module.queue.aws_sqs_queue.this", "https://sqs.us-east-1.amazonaws.com/000000000000/orders"}, true},
		{"import id", OpImport, []string{"module.bucket.aws_s3_bucket.this", "iacp-orders-dev"}, true},
		{"import arn ok", OpImport, []string{"aws_iam_role.r", "arn:aws:iam::000000000000:role/iacp-orders"}, true},
		{"force-unlock", OpForceUnlock, []string{"9db590f1-b6fe-c5f2-2678-8804f089deba"}, true},
		{"force-unlock word", OpForceUnlock, []string{"all"}, false},
		{"state push", "state push", []string{"terraform.tfstate"}, false},
		{"apply", "apply", nil, false},
		// Injection attempts: none of these is an address, an id or a lock id.
		{"shell metachar", OpStateRm, []string{"aws_sqs_queue.a; rm -rf /"}, false},
		{"newline workflow command", OpStateRm, []string{"aws_sqs_queue.a\n::set-output name=x::y"}, false},
		{"option smuggling", OpStateRm, []string{"-state=/etc/passwd"}, false},
		{"backend override", OpStateMv, []string{"aws_sqs_queue.a", "-state-out=x"}, false},
		{"command substitution", OpImport, []string{"aws_s3_bucket.b", "$(curl evil)"}, false},
		{"quote in key", OpStateRm, []string{`module.m["a\"b"].aws_s3_bucket.this`}, false},
		{"space in id", OpImport, []string{"aws_s3_bucket.b", "a b"}, false},
		{"lock id option", OpForceUnlock, []string{"-force"}, false},
		{"too long", OpStateRm, []string{"aws_sqs_queue." + strings.Repeat("a", 300)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateSurgeryArgs(tc.op, tc.args)
			if tc.ok != (len(errs) == 0) {
				t.Fatalf("ValidateSurgeryArgs(%q, %q) = %v", tc.op, tc.args, errs)
			}
		})
	}
}

func surgeryReport() SurgeryReport {
	return SurgeryReport{StackPath: "environments/development/payments/orders", Operation: OpStateMv,
		Args: []string{"aws_sqs_queue.a", "module.q.aws_sqs_queue.this"}, Ticket: "INC-1234", RequestedBy: "priya-s",
		Outcome: SurgerySucceeded, ExitCode: ptr(0), PlanExitCode: ptr(0), StateBefore: strings.Repeat("a", 64),
		StateAfter: strings.Repeat("b", 64), GitHub: GitHubRun{RunID: 77, RunAttempt: 1, HeadSHA: strings.Repeat("e4", 20)}}
}

func TestValidateSurgeryReport(t *testing.T) {
	if errs := ValidateSurgeryReport(surgeryReport()); len(errs) != 0 {
		t.Fatalf("valid report: %v", errs)
	}
	cases := []struct {
		name   string
		mutate func(*SurgeryReport)
		ptr    string
	}{
		{"path", func(r *SurgeryReport) { r.StackPath = "environments/../secrets" }, "/stack_path"},
		{"outcome", func(r *SurgeryReport) { r.Outcome = "maybe" }, "/outcome"},
		{"ticket", func(r *SurgeryReport) { r.Ticket = "see slack" }, "/ticket"},
		{"requester", func(r *SurgeryReport) { r.RequestedBy = "priya s" }, "/requested_by"},
		{"digest", func(r *SurgeryReport) { r.StateAfter = "nope" }, "/state_after_sha256"},
		{"run", func(r *SurgeryReport) { r.GitHub.RunID = 0 }, "/github"},
		{"sha", func(r *SurgeryReport) { r.GitHub.HeadSHA = "main" }, "/github/head_sha"},
		{"args", func(r *SurgeryReport) { r.Args = []string{"x"} }, "/args"},
		{"operation", func(r *SurgeryReport) { r.Operation = "state push" }, "/operation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := surgeryReport()
			tc.mutate(&r)
			errs := ValidateSurgeryReport(r)
			found := false
			for _, e := range errs {
				found = found || e.Pointer == tc.ptr
			}
			if !found {
				t.Fatalf("errors %v lack %s", errs, tc.ptr)
			}
		})
	}
	// A refused run reports the inputs validation rejected: they are not validated again,
	// and only an allow-listed operation is accepted.
	refused := surgeryReport()
	refused.Outcome, refused.Args, refused.Ticket = SurgeryRefused, []string{"; rm -rf /"}, "nope"
	if errs := ValidateSurgeryReport(refused); len(errs) != 0 {
		t.Fatalf("refused report: %v", errs)
	}
	refused.Operation = "state push"
	if errs := ValidateSurgeryReport(refused); len(errs) != 1 || errs[0].Pointer != "/operation" {
		t.Fatalf("refused report with an unknown operation: %v", errs)
	}
}

func TestCheckSurgeryProvenance(t *testing.T) {
	r := surgeryReport()
	if err := CheckSurgeryProvenance(r, nil); err != nil {
		t.Fatal(err)
	}
	wf := &identity.Workflow{RunID: 77, RunAttempt: 1, SHA: strings.Repeat("e4", 20)}
	if err := CheckSurgeryProvenance(r, wf); err != nil {
		t.Fatal(err)
	}
	wf.RunAttempt = 2
	if err := CheckSurgeryProvenance(r, wf); code(err) != "run_mismatch" {
		t.Fatalf("other attempt: %v", err)
	}
}

type memSurgery struct {
	stacks map[string]SurgeryStack
	events []audit.Event
}

func (m *memSurgery) InSurgeryTx(_ context.Context, fn func(SurgeryStore) error) error { return fn(m) }
func (m *memSurgery) StackByPath(_ context.Context, path string) (SurgeryStack, error) {
	s, ok := m.stacks[path]
	if !ok {
		return SurgeryStack{}, apperr.NotFound("no stack at %s", path)
	}
	return s, nil
}
func (m *memSurgery) Recorded(_ context.Context, action, correlationID string) (bool, error) {
	for _, e := range m.events {
		if e.Action == action && e.CorrelationID == correlationID {
			return true, nil
		}
	}
	return false, nil
}
func (m *memSurgery) AppendAudit(_ context.Context, e audit.Event) error {
	m.events = append(m.events, e)
	return nil
}

func TestSurgeryRecorder(t *testing.T) {
	stack := SurgeryStack{ID: uuid.New(), TeamID: uuid.New()}
	repo := &memSurgery{stacks: map[string]SurgeryStack{"environments/development/payments/orders": stack}}
	rec := NewSurgeryRecorder(repo, platform.FixedClock{T: now})
	bot := identity.Principal{Login: "acme/iacp/.github/workflows/state-surgery.yml@refs/heads/main", Type: identity.ActorGitHubActions,
		Workflow: &identity.Workflow{RunID: 77, RunAttempt: 1, SHA: strings.Repeat("e4", 20)}}
	ctx := context.Background()

	out, err := rec.Record(ctx, bot, surgeryReport())
	if err != nil || out.Duplicate || out.StackID != stack.ID {
		t.Fatalf("%+v, %v", out, err)
	}
	e := repo.events[0]
	if e.Action != "state.surgery" || e.SubjectType != "stack" || e.SubjectID != stack.ID.String() || *e.TeamID != stack.TeamID ||
		e.ActorType != "github_actions" || e.CorrelationID != "state-surgery-77-1" {
		t.Fatalf("event %+v", e)
	}
	var p map[string]any
	_ = json.Unmarshal(e.Payload, &p)
	if p["operation"] != OpStateMv || p["ticket"] != "INC-1234" || p["requested_by"] != "priya-s" || p["approved_via"] != "github_environment:break-glass" ||
		p["state_before_sha256"] != strings.Repeat("a", 64) || len(p["args"].([]any)) != 2 {
		t.Fatalf("payload %v", p)
	}
	// Sent again by the same run attempt: recorded once.
	if out, err := rec.Record(ctx, bot, surgeryReport()); err != nil || !out.Duplicate || len(repo.events) != 1 {
		t.Fatalf("duplicate: %+v, %v", out, err)
	}

	// A refused run is recorded without the rejected arguments or ticket.
	refused := surgeryReport()
	refused.GitHub.RunAttempt, refused.Outcome, refused.Args, refused.Ticket = 2, SurgeryRefused, []string{"::stop-commands::x"}, "nope"
	bot.Workflow.RunAttempt = 2
	if _, err := rec.Record(ctx, bot, refused); err != nil {
		t.Fatal(err)
	}
	p = map[string]any{}
	_ = json.Unmarshal(repo.events[1].Payload, &p)
	if _, ok := p["args"]; ok {
		t.Fatalf("refused payload carries args: %v", p)
	}
	if _, ok := p["ticket"]; ok {
		t.Fatalf("refused payload carries an invalid ticket: %v", p)
	}

	unknown := surgeryReport()
	unknown.StackPath = "environments/development/payments/ghost"
	if _, err := rec.Record(ctx, identity.Principal{Type: identity.ActorGitHubActions}, unknown); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("unknown stack: %v", err)
	}
	if _, err := rec.Record(ctx, bot, SurgeryReport{}); code(err) != "validation_failed" {
		t.Fatalf("empty report: %v", err)
	}
}
