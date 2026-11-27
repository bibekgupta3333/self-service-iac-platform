package recovery

import (
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tferror"
)

// The pipeline and the API keep only tferror's excerpt of a log (the identifier of each
// Error: line plus fixed-shape signals); the classifier must still see what it needs there.
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
		{box("Error acquiring the state lock"), ClassTransient, "lock_contention"},
		{box("creating Lambda Function (f): api error InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda."), ClassTransient, "eventual_consistency"},
		{box("creating S3 Bucket (b): https response error StatusCode: 409, api error BucketAlreadyExists: taken"), ClassConfig, "validation"},
		{box("creating IAM Role (r): StatusCode: 403, api error AccessDenied: x is not authorized to perform: iam:CreateRole"), ClassInfra, "permission_denied"},
		{box("creating Log Group (g): api error OperationAbortedException: conflicting operation"), ClassInfra, "partial_apply"},
	}
	for _, tc := range cases {
		msg := tferror.Clean(tc.log, 3, 500)
		if got := Classify(Failure{Stage: "apply", Message: msg}); got.Class != tc.class || got.Reason != tc.reason {
			t.Errorf("Classify(%q) = %s/%s, want %s/%s", msg, got.Class, got.Reason, tc.class, tc.reason)
		}
	}
}
