package recovery

import (
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tferror"
)

// The pipeline sends a Summary of the log and its Signals as separate fields, and the API
// stores them redacted; the classifier must still see what it needs there.
func TestClassifyTheStoredExcerpt(t *testing.T) {
	box := func(msg string) string {
		return "╷\n│ Error: " + msg + "\n│\n│   on main.tf line 3:\n╵\n"
	}
	cases := []struct {
		log    string
		class  Class
		reason string
	}{
		{box("creating SQS Queue (q): operation error SQS: CreateQueue, https response error StatusCode: 400, api error ThrottlingException: Rate exceeded"), ClassTransient, "throttling"},
		{box("putting S3 Bucket Policy (b): https response error StatusCode: 503, api error ServiceUnavailable: retry"), ClassTransient, "provider_5xx"},
		{box("reading EC2 VPC (v): RequestLimitExceeded: Request limit exceeded. status code: 503"), ClassTransient, "throttling"},
		{box("Error acquiring the state lock"), ClassTransient, "lock_contention"},
		{box("creating Lambda Function (f): api error InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda."), ClassTransient, "eventual_consistency"},
		{box("creating ECS Service (s): read tcp 10.0.0.1:443: unexpected EOF"), ClassTransient, "timeout"},
		{box("creating S3 Bucket (b): https response error StatusCode: 409, api error BucketAlreadyExists: taken"), ClassConfig, "validation"},
		{box("creating IAM Policy (p): api error MalformedPolicyDocument: bad"), ClassConfig, "validation"},
		{box("creating IAM Role (r): StatusCode: 403, api error AccessDenied: x is not authorized to perform: iam:CreateRole"), ClassInfra, "permission_denied"},
		{box("creating EC2 Instance (i): UnauthorizedOperation: no"), ClassInfra, "permission_denied"},
		{box("creating Log Group (g): api error OperationAbortedException: conflicting operation"), ClassInfra, "partial_apply"},
	}
	for _, tc := range cases {
		msg := ClassificationText(tferror.Summary(tc.log, 3, 500, "terraform apply failed"), tferror.Signals(tc.log))
		if got := Classify(Failure{Stage: "apply", Message: msg}); got.Class != tc.class || got.Reason != tc.reason {
			t.Errorf("Classify(%q) = %s/%s, want %s/%s", msg, got.Class, got.Reason, tc.class, tc.reason)
		}
	}
}

// Every vocabulary entry reaches the classifier and gives the class and reason it is
// tagged with: there is no classifier phrase the excerpt cannot carry, and no signal the
// classifier ignores.
func TestEveryVocabularyEntryClassifiesAsTagged(t *testing.T) {
	for _, v := range tferror.Vocabulary {
		msg := ClassificationText("Error: x (y)", []string{v.Text})
		if got := Classify(Failure{Stage: "apply", Message: msg}); string(got.Class) != v.Class || got.Reason != v.Reason {
			t.Errorf("%q: Classify = %s/%s, want %s/%s", v.Text, got.Class, got.Reason, v.Class, v.Reason)
		}
	}
}

// Words that only look like error codes are not signals, so they cannot reach the stored
// error block through the signals field.
func TestFreeTextIsNotASignal(t *testing.T) {
	probe := "Error: putting SSM Parameter (/p): value HunterTwoPasswordError rejected, api error Xs3cr3tV4lu3"
	if sig := tferror.Signals(probe); len(sig) != 0 {
		t.Fatalf("signals from free text: %v", sig)
	}
	if got := ClassificationText("Error: x (y)", []string{"HunterTwoPasswordError", "api error Xs3cr3tV4lu3"}); got != "Error: x (y)" {
		t.Fatalf("reported non-vocabulary signals survived: %q", got)
	}
}
