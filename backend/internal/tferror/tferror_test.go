package tferror

import (
	"strings"
	"testing"
)

// A boxed Terraform diagnostic with secrets on the Error: line and on detail lines.
const boxed = "aws_db_instance.main: Creating...\n" +
	"╷\n" +
	"│ Error: creating RDS DB Instance: api error InvalidParameterValue: password=hunter2-very-secret is too short (AKIAABCDEFGHIJKLMNOP)\n" +
	"│ \n" +
	"│   with module.db.aws_db_instance.main,\n" +
	"│   on main.tf line 12:\n" +
	"│   12:   master_password = \"s3cr3t-in-detail\"\n" +
	"╵\n" +
	"╷\n" +
	"│ Error: creating SQS Queue: api error ThrottlingException: Rate exceeded; Authorization: Bearer abc.def.ghi\n" +
	"╵\n"

func TestSummaryKeepsOnlyRedactedErrorLines(t *testing.T) {
	got := Summary(boxed, 3, 500, "fallback")
	for _, secret := range []string{"hunter2", "AKIAABCDEFGHIJKLMNOP", "s3cr3t-in-detail", "abc.def.ghi", "master_password", "│"} {
		if strings.Contains(got, secret) {
			t.Errorf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.HasPrefix(got, "Error: creating RDS DB Instance") || !strings.Contains(got, " | Error: creating SQS Queue: api error ThrottlingException") {
		t.Fatalf("summary %q", got)
	}
}

func TestSummaryFallbackAndCap(t *testing.T) {
	if got := Summary("Refreshing state...\n  master_password = \"x\"\n", 3, 500, "terraform failed"); got != "terraform failed" {
		t.Fatalf("no Error: line must give the fallback, got %q", got)
	}
	long := "Error: " + strings.Repeat("x", 900)
	if got := Summary(long, 3, 500, "f"); len([]rune(got)) != 500 || !strings.HasSuffix(got, "...") {
		t.Fatalf("cap: %d", len([]rune(got)))
	}
	if got := Summary(boxed, 1, 500, "f"); strings.Contains(got, "SQS") {
		t.Fatalf("maxLines 1 kept a second diagnostic: %q", got)
	}
}

func TestClean(t *testing.T) {
	if got := Clean(boxed, 3, 500); strings.Contains(got, "hunter2") || !strings.HasPrefix(got, "Error: creating RDS") {
		t.Fatalf("clean boxed: %q", got)
	}
	got := Clean("apply.yml did not finish (failure)\n token=abc123\x07", 3, 500)
	if got != "apply.yml did not finish (failure) token= <redacted>" {
		t.Fatalf("clean plain: %q", got)
	}
}
