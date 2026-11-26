package drift

import (
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

func TestClassifyExit(t *testing.T) {
	for code, want := range map[int]Result{0: ResultClean, 1: ResultError, 2: ResultDrifted} {
		if got, ok := ClassifyExit(code); !ok || got != want {
			t.Errorf("ClassifyExit(%d) = %q, %v; want %q", code, got, ok, want)
		}
	}
	for _, code := range []int{-1, 3, 137, 255} {
		if _, ok := ClassifyExit(code); ok {
			t.Errorf("ClassifyExit(%d) is a terraform outcome", code)
		}
	}
}

func TestNextHealth(t *testing.T) {
	tests := []struct {
		cur  Health
		res  Result
		want Health
	}{
		{HealthHealthy, ResultDrifted, HealthDrifted},
		{HealthDrifted, ResultDrifted, HealthDrifted},
		{HealthDrifted, ResultClean, HealthHealthy},
		{HealthHealthy, ResultClean, HealthHealthy},
		// A failed check says nothing about the stack.
		{HealthHealthy, ResultError, HealthHealthy},
		{HealthDrifted, ResultError, HealthDrifted},
	}
	for _, tt := range tests {
		if got := NextHealth(tt.cur, tt.res); got != tt.want {
			t.Errorf("NextHealth(%s, %s) = %s, want %s", tt.cur, tt.res, got, tt.want)
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
		{name: "bad stack path", base: validReport(), mutate: func(r *Report) { r.StackPath = "../etc" }, pointer: "/stack_path"},
		{name: "no run id", base: validReport(), mutate: func(r *Report) { r.GitHub.RunID = 0 }, pointer: "/github/run_id"},
		{name: "short sha", base: validReport(), mutate: func(r *Report) { r.GitHub.HeadSHA = "abc" }, pointer: "/github/head_sha"},
		{name: "exit code not terraform", base: validReport(), mutate: func(r *Report) { r.TerraformExitCode = 3 }, pointer: "/terraform_exit_code"},
		{name: "result disagrees with exit code", base: validReport(), mutate: func(r *Report) { r.Result = ResultClean }, pointer: "/result"},
		{name: "drifted without a count", base: validReport(), mutate: func(r *Report) { r.Summary = Summary{} }, pointer: "/summary/drifted"},
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
