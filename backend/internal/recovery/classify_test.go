package recovery

import (
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		in     Failure
		class  Class
		reason string
	}{
		// --- transient ---------------------------------------------------------------
		{"sdk v2 throttling", Failure{Stage: "apply", ExitCode: ptr(1), Message: `Error: creating SQS Queue (orders-dlq): operation error SQS: CreateQueue, ` +
			`https response error StatusCode: 400, RequestID: 2b9b1f3e, api error ThrottlingException: Rate exceeded`},
			ClassTransient, "throttling"},
		{"ec2 request limit", Failure{Stage: "apply", Message: `Error: reading EC2 VPC: RequestLimitExceeded: Request limit exceeded.
	status code: 503, request id: 7c4e`}, ClassTransient, "throttling"},
		{"s3 slow down", Failure{Stage: "apply", Message: `api error SlowDown: Please reduce your request rate.`}, ClassTransient, "throttling"},
		{"dynamodb throughput", Failure{Stage: "apply", Message: `ProvisionedThroughputExceededException: The level of configured provisioned throughput for the table was exceeded`},
			ClassTransient, "throttling"},
		{"provider 503", Failure{Stage: "apply", Message: `Error: putting S3 Bucket (iacp-orders) Policy: operation error S3: PutBucketPolicy, ` +
			`https response error StatusCode: 503, RequestID: X1, HostID: Y1, api error ServiceUnavailable: Service is unable to handle request.`},
			ClassTransient, "provider_5xx"},
		{"internal error", Failure{Stage: "destroy", Message: `Error: deleting KMS Key: InternalFailure: An internal error occurred`}, ClassTransient, "provider_5xx"},
		{"i/o timeout", Failure{Stage: "plan", ExitCode: ptr(1), Message: `Error: reading Lambda Function: dial tcp 10.0.0.12:443: i/o timeout`},
			ClassTransient, "timeout"},
		{"deadline", Failure{Stage: "apply", Message: `Error: waiting for SQS Queue attributes: context deadline exceeded`}, ClassTransient, "timeout"},
		{"connection reset", Failure{Stage: "apply", Message: `read tcp 172.17.0.2:51234->52.95.1.1:443: read: connection reset by peer`}, ClassTransient, "timeout"},
		{"state lock", Failure{Stage: "apply", Message: "Error: Error acquiring the state lock\n\nError message: operation error S3: PutObject, " +
			"https response error StatusCode: 412, api error PreconditionFailed\nLock Info:\n  ID: 7a1f"}, ClassTransient, "lock_contention"},
		{"eventual consistency lambda role", Failure{Stage: "apply", Message: `Error: creating Lambda Function (thumbnails): operation error Lambda: CreateFunction, ` +
			`https response error StatusCode: 400, api error InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda.`},
			ClassTransient, "eventual_consistency"},
		{"runner shutdown", Failure{Stage: "apply", Message: `The runner has received a shutdown signal. This can happen when the runner service is stopped`},
			ClassTransient, "runner_lost"},
		{"killed by signal", Failure{Stage: "apply", ExitCode: ptr(137), Message: ""}, ClassTransient, "runner_lost"},

		// --- config ------------------------------------------------------------------
		{"policy deny", Failure{Stage: "policy", Message: "deny: aws_s3_bucket.this must block public access (set block_public_acls)"},
			ClassConfig, "policy_denied"},
		{"policy stage without text", Failure{Stage: "conftest"}, ClassConfig, "policy_denied"},
		{"plan mismatch", Failure{Stage: "apply", Message: "plan_mismatch: the re-plan (ab12) differs from the approved plan (cd34); re-validation required"},
			ClassConfig, "plan_mismatch"},
		{"invalid variable", Failure{Stage: "plan", ExitCode: ptr(1), Message: "Error: Invalid value for variable\n\n  on main.tf line 4:\n" +
			"visibility_timeout_seconds must be between 0 and 43200."}, ClassConfig, "validation"},
		{"unsupported argument", Failure{Stage: "plan", Message: `Error: Unsupported argument

  on main.tf line 12, in module "bucket":
  12:   versioned = true

An argument named "versioned" is not expected here.`}, ClassConfig, "validation"},
		{"name conflict at apply", Failure{Stage: "apply", ExitCode: ptr(1), Message: `Error: creating S3 Bucket (orders-store): operation error S3: CreateBucket, ` +
			`https response error StatusCode: 409, api error BucketAlreadyExists: The requested bucket name is not available.`},
			ClassConfig, "validation"},
		{"parameter combination", Failure{Stage: "apply", Message: `api error InvalidParameterCombination: Cannot specify both FifoQueue and ContentBasedDeduplication=false`},
			ClassConfig, "validation"},
		{"plan error", Failure{Stage: "plan", ExitCode: ptr(1), Message: "Error: Failed to query available provider packages"}, ClassConfig, "plan_error"},
		{"plan without message", Failure{Stage: "plan", ExitCode: ptr(1)}, ClassConfig, "plan_error"},
		{"generic summary", Failure{Stage: "summary", Message: "apply.yml did not finish (failure); see the workflow run log"},
			ClassConfig, "unclassified"},
		{"empty", Failure{}, ClassConfig, "unclassified"},

		// --- infra -------------------------------------------------------------------
		{"partial apply", Failure{Stage: "apply", ExitCode: ptr(1), Message: `Error: creating CloudWatch Logs Log Group (/aws/lambda/x): ` +
			`operation error CloudWatch Logs: CreateLogGroup, https response error StatusCode: 400, api error OperationAbortedException: A conflicting operation is currently in progress`},
			ClassInfra, "partial_apply"},
		{"apply without message", Failure{Stage: "apply", ExitCode: ptr(1)}, ClassInfra, "partial_apply"},
		{"quota", Failure{Stage: "apply", Message: `Error: creating EC2 EIP: AddressLimitExceeded: The maximum number of addresses has been reached.`},
			ClassInfra, "quota"},
		{"capacity", Failure{Stage: "apply", Message: `InsufficientInstanceCapacity: We currently do not have sufficient t3.large capacity`}, ClassInfra, "quota"},
		{"access denied", Failure{Stage: "apply", Message: `Error: creating IAM Role (x): operation error IAM: CreateRole, https response error StatusCode: 403, ` +
			`api error AccessDenied: User: arn:aws:sts::111111111111:assumed-role/iacp-apply/gh is not authorized to perform: iam:CreateRole with an explicit deny in a permissions boundary`},
			ClassInfra, "permission_denied"},
		{"smoke", Failure{Stage: "smoke", Message: "post-apply smoke test failed; see the job summary"}, ClassInfra, "smoke_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			if got.Class != tc.class || got.Reason != tc.reason {
				t.Fatalf("Classify = %s/%s, want %s/%s", got.Class, got.Reason, tc.class, tc.reason)
			}
		})
	}
}

func TestClassifyTransientWinsOverValidationCodes(t *testing.T) {
	// The eventual-consistency message is reported as InvalidParameterValueException, which
	// would otherwise be a validation error.
	got := Classify(Failure{Stage: "apply", Message: "InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda."})
	if got.Class != ClassTransient {
		t.Fatalf("got %+v", got)
	}
}

func TestClassifyBoundsTheScannedText(t *testing.T) {
	// A throttling line past the scan limit is not seen: the failure is classified by its stage.
	msg := strings.Repeat("x", maxScan) + "ThrottlingException"
	if got := Classify(Failure{Stage: "apply", Message: msg}); got.Class != ClassInfra {
		t.Fatalf("got %+v", got)
	}
}

func TestClassifyUnknownIsNeverTransient(t *testing.T) {
	for _, stage := range []string{"", "plan", "apply", "destroy", "smoke", "summary", "lock", "release", "weird"} {
		for _, msg := range []string{"", "something odd happened", "exit status 1"} {
			if got := Classify(Failure{Stage: stage, ExitCode: ptr(1), Message: msg}); got.Class == ClassTransient {
				t.Errorf("Classify(%q, %q) = transient", stage, msg)
			}
		}
	}
}

func TestIsApplyStage(t *testing.T) {
	for stage, want := range map[string]bool{"apply": true, "destroy": true, "plan": false, "smoke": false, "": false} {
		if IsApplyStage(stage) != want {
			t.Errorf("IsApplyStage(%q) != %v", stage, want)
		}
	}
}
