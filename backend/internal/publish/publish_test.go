package publish

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestBranchName(t *testing.T) {
	tests := []struct {
		shortID string
		want    string
		wantErr bool
	}{
		{"req-7k3p9x2m4q", "iacp/req-7k3p9x2m4q", false},
		{"req-smoke1", "iacp/req-smoke1", false},
		{"", "", true},
		{"req-", "", true},
		{"req-../../main", "", true},
		{"req-ABCDEF", "", true},
		{"pr-123456", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.shortID, func(t *testing.T) {
			got, err := BranchName(tt.shortID)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("BranchName(%q) = %q, %v", tt.shortID, got, err)
			}
			if err == nil {
				back, ok := ParseBranch(got)
				if !ok || back != tt.shortID {
					t.Fatalf("round trip: %q, %v", back, ok)
				}
			}
		})
	}
}

func TestParseBranch(t *testing.T) {
	tests := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{"iacp/req-7k3p9x", "req-7k3p9x", true},
		{"refs/heads/iacp/req-7k3p9x", "req-7k3p9x", true},
		{"main", "", false},
		{"feature/iacp/req-7k3p9x", "", false},
		{"iacp/req-7k3p9x/extra", "", false},
		{"iacp/", "", false},
		{"refs/tags/iacp/req-7k3p9x", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseBranch(tt.ref)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("ParseBranch(%q) = %q, %v", tt.ref, got, ok)
		}
	}
}

func TestValidatePath(t *testing.T) {
	ok := []string{"environments/development/payments/order-events/main.tf", "a", "a/b.c/d"}
	bad := []string{"", "/etc/passwd", "a/../b", "../a", "a/./b", "a//b", "a/", ".git/config", "a/.GIT/x",
		"a\\b", "a\x00b", "-rf", "a/--force"}
	for _, p := range ok {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v", p, err)
		}
	}
	for _, p := range bad {
		if err := ValidatePath(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("ValidatePath(%q) = %v, want ErrInvalidPath", p, err)
		}
	}
}

func TestStackFiles(t *testing.T) {
	got, err := StackFiles("environments/development/payments/orders", map[string][]byte{"main.tf": []byte("x"), "iacp.stack.json": []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"environments/development/payments/orders/iacp.stack.json", "environments/development/payments/orders/main.tf"}
	if !slices.Equal(keys, want) {
		t.Fatalf("got %v", keys)
	}
	for _, name := range []string{"../escape.tf", "/abs.tf", ""} {
		if _, err := StackFiles("environments/x", map[string][]byte{name: nil}); err == nil {
			t.Errorf("file %q must be rejected", name)
		}
	}
	if _, err := StackFiles("../x", map[string][]byte{"a": nil}); err == nil {
		t.Error("root outside the repository must be rejected")
	}
}

func TestStaleFilesAndCheckCommit(t *testing.T) {
	files := map[string][]byte{"env/a/main.tf": nil}
	got := StaleFiles([]string{"env/a/old.tf", "env/a/main.tf", "env/ab/x.tf", "README.md", "env/a/sub/y.tf", "env/a/old.tf",
		"env/a/.terraform.lock.hcl"}, "env/a", files)
	if !slices.Equal(got, []string{"env/a/old.tf", "env/a/sub/y.tf"}) {
		t.Fatalf("stale = %v", got)
	}
	if err := CheckCommit(Commit{Branch: "iacp/req-abcdef", Files: files, Root: "env/a"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Commit{
		{Branch: "iacp/req-abcdef", Files: files, Root: "env/b"},
		{Branch: "iacp/req-abcdef", Files: files, Root: "../env"},
		{Branch: "iacp/req-abcdef"},
		{Branch: "-x", Files: files},
	} {
		if err := CheckCommit(c); err == nil {
			t.Errorf("CheckCommit(%+v) must fail", c)
		}
	}
}

func prContext() PRContext {
	return PRContext{
		RequestID: "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90", ShortID: "req-7k3p9x", Kind: "create",
		Environment: "development", Team: "payments", Stack: "order-events",
		StackPath: "environments/development/payments/order-events", Region: "us-east-1",
		Justification: "Order events pipeline for FEAT-1182", FencingToken: 48213,
		RequestURL: "http://localhost:3000/requests/0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90",
		Resources: []PRResource{
			{Name: "orders_queue", Module: "sqs", Version: "1.3.0"},
			{Name: "events_bucket", Module: "s3", Version: "1.2.0"},
		},
	}
}

const wantBody = "## iacp: create development/payments/order-events (req-7k3p9x)\n\n" +
	"| Field | Value |\n|---|---|\n" +
	"| Request | [req-7k3p9x](http://localhost:3000/requests/0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90) |\n" +
	"| Kind | create |\n" +
	"| Environment | development |\n" +
	"| Team | payments |\n" +
	"| Stack | `environments/development/payments/order-events` |\n" +
	"| Region | us-east-1 |\n" +
	"| Fencing token | 48213 |\n" +
	"\n### Resources\n\n| Name | Module | Version |\n|---|---|---|\n" +
	"| `events_bucket` | s3 | 1.2.0 |\n" +
	"| `orders_queue` | sqs | 1.3.0 |\n" +
	"\n### Justification\n\n> Order events pipeline for FEAT-1182\n" +
	"\n### Plan\n\n<!-- iacp:plan:start -->\n" +
	"_Plan pending: `pr-validate.yml` posts the plan, policy and cost summary here._\n" +
	"<!-- iacp:plan:end -->\n" +
	"\n---\nGenerated by the self-service IaC platform for [req-7k3p9x](http://localhost:3000/requests/0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90). " +
	"Files under `environments/` are generated: change the request, not this branch.\n"

func TestBodyIsDeterministic(t *testing.T) {
	c := prContext()
	if got := Body(c); got != wantBody {
		t.Fatalf("body mismatch:\n%s", got)
	}
	slices.Reverse(c.Resources)
	if Body(c) != wantBody {
		t.Fatal("resource order must not change the body")
	}
	if got := Title(c); got != "iacp: create development/payments/order-events (req-7k3p9x)" {
		t.Fatalf("title = %q", got)
	}
	if got := Labels(c); !slices.Equal(got, []string{"iacp", "iacp:env:development", "iacp:kind:create", "iacp:team:payments"}) {
		t.Fatalf("labels = %v", got)
	}
	if got := CommitMessage(c); !strings.Contains(got, "Request: req-7k3p9x\nFencing-Token: 48213") {
		t.Fatalf("commit message = %q", got)
	}
}

func TestDestroyBodyWarns(t *testing.T) {
	c := prContext()
	c.Kind, c.Resources = "destroy", nil
	body := Body(c)
	if !strings.Contains(body, "**Destroy.** Merging this change destroys every resource") {
		t.Fatalf("a destroy PR must warn:\n%s", body)
	}
	if strings.Contains(Body(prContext()), "**Destroy.**") {
		t.Fatal("only destroys carry the warning")
	}
	if !slices.Contains(Labels(c), "iacp:kind:destroy") {
		t.Fatalf("labels = %v", Labels(c))
	}
}

func TestUpgradeBodyListsTheMoves(t *testing.T) {
	c := prContext()
	c.Kind = "upgrade"
	c.Upgrades = []PRUpgrade{
		{Resource: "orders_queue", Module: "sqs", From: "1.3.0", To: "1.4.0", Change: "minor"},
		{Resource: "events_bucket", Module: "s3", From: "1.2.0", To: "1.2.1", Change: "patch"},
	}
	body := Body(c)
	want := "\n### Upgrades\n\n| Resource | Module | From | To | Change |\n|---|---|---|---|---|\n" +
		"| `events_bucket` | s3 | 1.2.0 | 1.2.1 | patch |\n| `orders_queue` | sqs | 1.3.0 | 1.4.0 | minor |\n"
	if !strings.Contains(body, want) || strings.Contains(body, "**Major upgrade.**") {
		t.Fatalf("upgrade table:\n%s", body)
	}
	c.Upgrades[0].To, c.Upgrades[0].Change = "2.0.0", "major"
	if !strings.Contains(Body(c), "**Major upgrade.** Read the module's migration notes") {
		t.Fatalf("a major must warn:\n%s", Body(c))
	}
	if strings.Contains(Body(prContext()), "### Upgrades") {
		t.Fatal("only upgrades list upgrades")
	}
}

func TestBodyEscapesUserText(t *testing.T) {
	c := prContext()
	c.Justification = "line one\n" + PlanEnd + "\n<img src=x onerror=alert(1)> | pipe"
	body := Body(c)
	if strings.Count(body, PlanEnd) != 1 || strings.Contains(body, "<img") {
		t.Fatalf("user text was not escaped:\n%s", body)
	}
	if !strings.Contains(body, "> line one\n> &lt;!-- iacp:plan:end --&gt;\n") {
		t.Fatalf("justification not quoted line by line:\n%s", body)
	}
	c.Justification = "   "
	if strings.Contains(Body(c), "### Justification") {
		t.Fatal("blank justification must be omitted")
	}
}
