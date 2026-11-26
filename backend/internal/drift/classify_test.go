package drift

import (
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func TestClassifyExit(t *testing.T) {
	for _, tt := range []struct {
		code, drifted int
		want          Result
	}{
		{0, 0, ResultClean}, {1, 0, ResultError}, {2, 3, ResultDrifted},
		// A refresh-only plan exits 2 for output-only changes too: that is not drift.
		{2, 0, ResultClean},
	} {
		if got, ok := ClassifyExit(tt.code, tt.drifted); !ok || got != tt.want {
			t.Errorf("ClassifyExit(%d, %d) = %q, %v; want %q", tt.code, tt.drifted, got, ok, tt.want)
		}
	}
	for _, code := range []int{-1, 3, 137, 255} {
		if _, ok := ClassifyExit(code, 0); ok {
			t.Errorf("ClassifyExit(%d) is a terraform outcome", code)
		}
	}
}

func TestNextHealth(t *testing.T) {
	const (
		unk, hea, deg, dri = requests.HealthUnknown, requests.HealthHealthy, requests.HealthDegraded, requests.HealthDrifted
	)
	// Rows: starting health; columns: drifted, clean, error (ADR-0013).
	tests := []struct {
		cur                   requests.StackHealth
		drifted, clean, error requests.StackHealth
	}{
		{unk, dri, unk, unk},
		{hea, dri, hea, hea},
		// A refresh-only scan runs no smoke test: it must not erase a smoke failure by itself.
		{deg, dri, deg, deg},
		{dri, dri, hea, dri},
	}
	for _, tt := range tests {
		for r, want := range map[Result]requests.StackHealth{ResultDrifted: tt.drifted, ResultClean: tt.clean, ResultError: tt.error} {
			if got := NextHealth(tt.cur, r); got != want {
				t.Errorf("NextHealth(%s, %s) = %s, want %s", tt.cur, r, got, want)
			}
		}
	}
	// The one lossy path, spelled out: degraded -> drift -> clean ends healthy.
	h := NextHealth(NextHealth(deg, ResultDrifted), ResultClean)
	if h != hea {
		t.Errorf("degraded, drifted, clean = %s; the documented lossy path ends healthy", h)
	}
}

// boxedLog is a realistic `terraform plan` log that fails: refresh lines, then a boxed
// diagnostic whose source snippet and provider detail carry a secret value.
const boxedLog = "module.db.aws_ssm_parameter.db: Refreshing state... [id=/db/password]\n" +
	"module.db.aws_kms_key.this: Refreshing state... [id=1234]\n" +
	"\u2577\n" +
	"\u2502 Error: reading SSM Parameter (/db/password): operation error SSM: GetParameter, api error ValidationException: bad request\n" +
	"\u2502 \n" +
	"\u2502   with module.db.aws_ssm_parameter.db,\n" +
	"\u2502   on main.tf line 12, in resource \"aws_ssm_parameter\" \"db\":\n" +
	"\u2502   12:   value = \"hunter2-secret\"\n" +
	"\u2502 \n" +
	"\u2502 request body {\"Value\":\"hunter2-secret\"} was rejected\n" +
	"\u2575\n"

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestSummarizeErrorKeepsOnlyTheErrorLine(t *testing.T) {
	want := "Error: reading SSM Parameter (/db/password): operation error SSM: GetParameter, api error ValidationException: bad request"
	// The full log: the first non-empty line is a "Refreshing state" line, and the box
	// characters are lines of their own; neither may be picked.
	if got := SummarizeError(boxedLog); got != want {
		t.Fatalf("full log: %q", got)
	}
	// A five-line tail has no Error line at all: only the fixed message is kept, never source
	// text or provider detail, and the secret never survives.
	tail := lastLines(boxedLog, 5)
	if got := SummarizeError(tail); got != GenericFailure {
		t.Fatalf("5-line tail: %q, want the generic failure", got)
	}
	for _, in := range []string{boxedLog, tail, lastLines(boxedLog, 3), lastLines(boxedLog, 8)} {
		if got := SummarizeError(in); strings.Contains(got, "hunter2") || strings.Contains(got, "value =") {
			t.Errorf("a value survived: %q", got)
		}
	}
}

func TestSummarizeError(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", GenericFailure},
		{"no Error line", "just some text\nmore text", GenericFailure},
		{"box characters only", "\u2577\n\u2502\n\u2575", GenericFailure},
		{"error line first", "Error: creating S3 Bucket: AccessDenied\n  with module.b.aws_s3_bucket.this,\n  on main.tf line 4", "Error: creating S3 Bucket: AccessDenied"},
		{"box prefix", "\u2502 Error: boom\n\u2502 detail", "Error: boom"},
		{"error is not a prefix", "an Error: in the middle", GenericFailure},
		{"secret assignments", "Error: bad password=hunter2 for token: abc123", "Error: bad password= <redacted> for token: <redacted>"},
		{"access key id", "Error: key AKIAABCDEFGHIJKLMNOP rejected", "Error: key <redacted> rejected"},
		{"bearer", "Error: Authorization failed with Bearer abc.def.ghi", "Error: Authorization failed with <redacted>"},
		{"control characters", "Error: a\x1b[31mb", "Error: a [31mb"},
	}
	for _, tt := range tests {
		if got := SummarizeError(tt.in); got != tt.want {
			t.Errorf("%s: SummarizeError = %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := SummarizeError("Error: " + strings.Repeat("x", 500)); len([]rune(got)) != MaxSummaryLen || !strings.HasSuffix(got, "...") {
		t.Errorf("long line = %d runes", len([]rune(got)))
	}
}

func TestSuppresses(t *testing.T) {
	ign, open := StatusIgnored, StatusOpen
	e := func(st *Status, hash string) Entry { return Entry{Result: ResultDrifted, Status: st, PlanHash: hash} }
	for name, tt := range map[string]struct {
		e    Entry
		hash string
		want bool
	}{
		"ignored, same hash":       {e(&ign, "aa"), "aa", true},
		"ignored, other hash":      {e(&ign, "aa"), "bb", false},
		"open, same hash":          {e(&open, "aa"), "aa", false},
		"no status":                {e(nil, "aa"), "aa", false},
		"ignored, no hash in scan": {e(&ign, ""), "", false},
	} {
		if got := Suppresses(tt.e, tt.hash); got != tt.want {
			t.Errorf("%s: Suppresses = %v, want %v", name, got, tt.want)
		}
	}
}

func TestCheckable(t *testing.T) {
	for status, want := range map[string]bool{
		"active": true, "drifted": true, "provisioning": false, "updating": false,
		"destroying": false, "destroyed": false, "failed": false, "": false,
	} {
		if got := Checkable(status); got != want {
			t.Errorf("Checkable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestAllowed(t *testing.T) {
	want := map[Action]Status{ActionReconcile: StatusReconciled, ActionCodify: StatusCodified, ActionIgnore: StatusIgnored}
	for a, st := range want {
		if got, ok := Allowed(StatusOpen, a); !ok || got != st {
			t.Errorf("Allowed(open, %s) = %q, %v; want %q", a, got, ok, st)
		}
		// A resolved report is final, whatever the action.
		for _, from := range []Status{StatusReconciled, StatusIgnored, StatusCodified, ""} {
			if _, ok := Allowed(from, a); ok {
				t.Errorf("Allowed(%q, %s) must be refused", from, a)
			}
		}
	}
	if _, ok := Allowed(StatusOpen, "delete"); ok {
		t.Error("an unknown action must be refused")
	}
}

func TestRBACAction(t *testing.T) {
	if RBACAction(ActionReconcile) != rbac.ActionDriftReconcile || RBACAction(ActionCodify) != rbac.ActionDriftReconcile ||
		RBACAction(ActionIgnore) != rbac.ActionDriftIgnore {
		t.Fatal("reconcile and codify are developer actions, ignore needs a maintainer")
	}
	// The matrix (docs/07): developer may reconcile, only maintainer and up may ignore.
	if !rbac.Allowed(rbac.RoleDeveloper, RBACAction(ActionReconcile), rbac.Resource{Environment: rbac.EnvProduction}) ||
		rbac.Allowed(rbac.RoleDeveloper, RBACAction(ActionIgnore), rbac.Resource{Environment: rbac.EnvDevelopment}) ||
		!rbac.Allowed(rbac.RoleMaintainer, RBACAction(ActionIgnore), rbac.Resource{Environment: rbac.EnvProduction}) ||
		rbac.Allowed(rbac.RoleViewer, RBACAction(ActionReconcile), rbac.Resource{Environment: rbac.EnvDevelopment}) {
		t.Fatal("RBAC matrix disagrees with the resolution actions")
	}
}

func validReport() Report {
	return Report{
		StackPath:         "environments/development/payments/order-events",
		GitHub:            GitHubRun{RunID: 900, RunAttempt: 1, HeadSHA: strings.Repeat("ab", 20)},
		Result:            ResultDrifted,
		TerraformExitCode: ExitChanges,
		Summary:           Summary{Drifted: 2, Updated: 1, Deleted: 1},
		Changed: []ChangedResource{
			{Address: "module.bucket.aws_s3_bucket.this", Type: "aws_s3_bucket", Actions: []string{"update"}},
			{Address: `module.q.aws_sqs_queue.this["a"]`, Type: "aws_sqs_queue", Actions: []string{"delete"}},
		},
		PlanHash: strings.Repeat("c", 64),
	}
}

func TestValidate(t *testing.T) {
	clean := Report{StackPath: validReport().StackPath, GitHub: validReport().GitHub, Result: ResultClean, TerraformExitCode: ExitClean}
	failed := Report{StackPath: validReport().StackPath, GitHub: validReport().GitHub, Result: ResultError,
		TerraformExitCode: ExitError, Error: "terraform init failed"}
	tests := []struct {
		name    string
		mutate  func(*Report)
		base    Report
		pointer string // expected first error pointer; "" means valid
	}{
		{name: "valid drifted", base: validReport()},
		{name: "valid clean", base: clean},
		{name: "valid error", base: failed},
		{name: "exit 2 with only output changes is clean", base: clean, mutate: func(r *Report) { r.TerraformExitCode = ExitChanges }},
		{name: "bad stack path", base: validReport(), mutate: func(r *Report) { r.StackPath = "../etc" }, pointer: "/stack_path"},
		{name: "no run id", base: validReport(), mutate: func(r *Report) { r.GitHub.RunID = 0 }, pointer: "/github/run_id"},
		{name: "short sha", base: validReport(), mutate: func(r *Report) { r.GitHub.HeadSHA = "abc" }, pointer: "/github/head_sha"},
		{name: "exit code not terraform", base: validReport(), mutate: func(r *Report) { r.TerraformExitCode = 3 }, pointer: "/terraform_exit_code"},
		{name: "result disagrees with exit code", base: validReport(), mutate: func(r *Report) { r.Result = ResultClean }, pointer: "/result"},
		{name: "drifted without a count", base: validReport(), mutate: func(r *Report) { r.Summary = Summary{} }, pointer: "/result"},
		{name: "updated plus deleted exceed drifted", base: validReport(), mutate: func(r *Report) { r.Summary.Updated = 5 }, pointer: "/summary"},
		{name: "negative count", base: validReport(), mutate: func(r *Report) { r.Summary.Deleted = -1 }, pointer: "/summary"},
		{name: "drifted without plan hash", base: validReport(), mutate: func(r *Report) { r.PlanHash = "" }, pointer: "/plan_hash"},
		{name: "bad plan hash", base: validReport(), mutate: func(r *Report) { r.PlanHash = "XYZ" }, pointer: "/plan_hash"},
		{name: "clean with resources", base: clean, mutate: func(r *Report) { r.Summary.Drifted = 1 }, pointer: "/summary"},
		{name: "clean with changed list", base: clean, mutate: func(r *Report) { r.Changed = validReport().Changed }, pointer: "/summary"},
		{name: "error without message", base: failed, mutate: func(r *Report) { r.Error = " " }, pointer: "/error"},
		{name: "error text on a clean report", base: clean, mutate: func(r *Report) { r.Error = "x" }, pointer: "/error"},
		{name: "oversized error", base: failed, mutate: func(r *Report) { r.Error = strings.Repeat("x", MaxErrorLen+1) }, pointer: "/error"},
		{name: "too many changed", base: validReport(), mutate: func(r *Report) {
			r.Changed = make([]ChangedResource, MaxChanged+1)
			for i := range r.Changed {
				r.Changed[i] = ChangedResource{Address: "aws_s3_bucket.a", Actions: []string{"update"}}
			}
		}, pointer: "/changed"},
		{name: "address with shell characters", base: validReport(), mutate: func(r *Report) { r.Changed[0].Address = "a; rm -rf /" }, pointer: "/changed/0/address"},
		{name: "no actions", base: validReport(), mutate: func(r *Report) { r.Changed[0].Actions = nil }, pointer: "/changed/0/actions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.base
			r.Changed = append([]ChangedResource(nil), tt.base.Changed...)
			if tt.mutate != nil {
				tt.mutate(&r)
			}
			errs := Validate(r)
			switch {
			case tt.pointer == "" && len(errs) > 0:
				t.Fatalf("valid report rejected: %+v", errs)
			case tt.pointer != "" && (len(errs) == 0 || errs[0].Pointer != tt.pointer):
				t.Fatalf("errors = %+v, want first pointer %s", errs, tt.pointer)
			}
		})
	}
}

func TestValidateResolve(t *testing.T) {
	long := strings.Repeat("x", MaxReasonLen+1)
	tests := []struct {
		name    string
		in      ResolveInput
		pointer string
	}{
		{"reconcile without reason", ResolveInput{Action: ActionReconcile}, ""},
		{"codify with reason", ResolveInput{Action: ActionCodify, Reason: "console edit was intended"}, ""},
		{"ignore with reason", ResolveInput{Action: ActionIgnore, Reason: "known console tag by the cost team"}, ""},
		{"ignore without reason", ResolveInput{Action: ActionIgnore}, "/reason"},
		{"ignore with blank reason", ResolveInput{Action: ActionIgnore, Reason: "        "}, "/reason"},
		{"ignore with a token reason", ResolveInput{Action: ActionIgnore, Reason: "ok"}, "/reason"},
		{"unknown action", ResolveInput{Action: "delete"}, "/action"},
		{"empty action", ResolveInput{}, "/action"},
		{"reason too long", ResolveInput{Action: ActionReconcile, Reason: long}, "/reason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateResolve(tt.in)
			switch {
			case tt.pointer == "" && len(errs) > 0:
				t.Fatalf("valid input rejected: %+v", errs)
			case tt.pointer != "" && (len(errs) == 0 || errs[0].Pointer != tt.pointer):
				t.Fatalf("errors = %+v, want %s", errs, tt.pointer)
			}
		})
	}
}

func TestReconcileJustification(t *testing.T) {
	if got := ReconcileJustification("", "environments/development/a/b"); !strings.Contains(got, "environments/development/a/b") {
		t.Errorf("default = %q", got)
	}
	if got := ReconcileJustification("  console edit  ", "x"); got != "Reconcile drift: console edit" {
		t.Errorf("with reason = %q", got)
	}
}
