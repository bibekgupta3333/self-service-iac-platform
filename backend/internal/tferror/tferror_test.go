package tferror

import (
	"strings"
	"testing"
)

// A boxed Terraform log: secrets on the Error: lines (in the provider message and in key=value
// form), on detail lines, and an AWS SDK v2 error with the code recovery classifies by.
const boxed = "aws_db_instance.main: Creating...\n" +
	"╷\n" +
	"│ Error: creating RDS DB Instance (orders-db): operation error RDS: CreateDBInstance, https response error StatusCode: 400, " +
	"api error InvalidParameterValue: The parameter MasterUserPassword hunter2-very-secret is not valid (AKIAABCDEFGHIJKLMNOP)\n" +
	"│ \n" +
	"│   with module.db.aws_db_instance.main,\n" +
	"│   on main.tf line 12:\n" +
	"│   12:   master_password = \"s3cr3t-in-detail\" # ThrottlingException\n" +
	"╵\n" +
	"╷\n" +
	"│ Error: creating SQS Queue (orders-dlq): operation error SQS: CreateQueue, https response error StatusCode: 503, " +
	"api error ThrottlingException: Rate exceeded; Authorization: Bearer abc.def.ghi\n" +
	"╵\n"

var leaks = []string{"hunter2", "AKIAABCDEFGHIJKLMNOP", "s3cr3t-in-detail", "abc.def.ghi", "master_password", "MasterUserPassword", "│"}

func TestSummaryKeepsOnlyTheIdentifierOfEachErrorLine(t *testing.T) {
	got := Summary(boxed, 3, 500, "fallback")
	if got != "Error: creating RDS DB Instance (orders-db) | Error: creating SQS Queue (orders-dlq)" {
		t.Fatalf("summary %q", got)
	}
	if got := Summary(boxed, 1, 500, "f"); got != "Error: creating RDS DB Instance (orders-db)" {
		t.Fatalf("one line: %q", got)
	}
}

func TestExcerptCarriesSignalsButNoSecrets(t *testing.T) {
	got := Excerpt(boxed, 3, 500, "terraform apply failed; see the workflow log")
	for _, l := range leaks {
		if strings.Contains(got, l) {
			t.Errorf("excerpt leaks %q: %s", l, got)
		}
	}
	for _, want := range []string{"api error InvalidParameterValue", "api error ThrottlingException", "StatusCode: 503", "Rate exceeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("excerpt lacks signal %q: %s", want, got)
		}
	}
	if len([]rune(got)) > 500 {
		t.Fatalf("excerpt is %d runes", len([]rune(got)))
	}
}

func TestSignalsReadOnlyErrorLines(t *testing.T) {
	// ThrottlingException appears only in a source line of the first diagnostic.
	log := "│ Error: creating X (a): boom\n│   12:   note = \"ThrottlingException\"\n"
	if sig := Signals(log); len(sig) != 0 {
		t.Fatalf("signals from a detail line: %v", sig)
	}
	if sig := Signals("│ Error: Error acquiring the state lock\n"); len(sig) != 1 || sig[0] != "Error acquiring the state lock" {
		t.Fatalf("state lock: %v", sig)
	}
}

func TestFallbackAndCap(t *testing.T) {
	if got := Excerpt("Refreshing state...\n  master_password = \"x\"\n", 3, 500, "terraform apply failed"); got != "terraform apply failed" {
		t.Fatalf("no Error: line must give the fallback, got %q", got)
	}
	long := "Error: " + strings.Repeat("x", 900)
	if got := Summary(long, 3, 500, "f"); len([]rune(got)) != 500 || !strings.HasSuffix(got, "...") {
		t.Fatalf("cap: %d", len([]rune(got)))
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"password=hunter2 x":            "password= <redacted> x",
		"Authorization: Bearer abc.def": "Authorization: <redacted>",
		"key AKIAABCDEFGHIJKLMNOP":      "key <redacted>",
		"bearer xyz":                    "<redacted>",
		"tab\there\x07":                 "tab here ",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClean(t *testing.T) {
	got := Clean(boxed, 3, 500)
	for _, l := range leaks {
		if strings.Contains(got, l) {
			t.Errorf("clean leaks %q: %s", l, got)
		}
	}
	if !strings.HasPrefix(got, "Error: creating RDS DB Instance (orders-db) | ") {
		t.Fatalf("clean boxed: %q", got)
	}
	if got := Clean("apply.yml did not finish (failure) token=abc123\x07", 3, 500); got != "apply.yml did not finish (failure) token= <redacted>" {
		t.Fatalf("clean platform text: %q", got)
	}
	if got := Clean("line one\nsecret detail: xyz\n", 3, 500); got != "pipeline stage failed; see the workflow log" {
		t.Fatalf("multi-line non-terraform text must be replaced: %q", got)
	}
}
