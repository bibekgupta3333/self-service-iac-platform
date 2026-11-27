package tferror

// Signal is one entry of the failure vocabulary: a fixed AWS error code or Terraform phrase
// and the class and reason recovery gives a failure that carries it.
type Signal struct {
	Text   string
	Class  string // transient, config or infra
	Reason string
}

// Vocabulary is the complete list of signals. recovery builds its classifier rules from it,
// so a phrase the classifier knows is always one the excerpt can carry, and vice versa.
// Order matters: within a class the first matching entry gives the reason (eventual
// consistency before throttling; the classes themselves are checked transient, config,
// infra). Keep in step with VOCABULARY in tools/stack_pipeline/tf_error.py; testdata/
// vectors.json pins the list for both.
var Vocabulary = []Signal{
	// transient: running again is expected to work
	{"role cannot be assumed", "transient", "eventual_consistency"},
	{"role defined for the function cannot be assumed", "transient", "eventual_consistency"},
	{"InvalidInstanceProfile", "transient", "eventual_consistency"},
	{"Invalid IAM Instance Profile", "transient", "eventual_consistency"},
	{"ThrottlingException", "transient", "throttling"},
	{"Throttling", "transient", "throttling"},
	{"RequestLimitExceeded", "transient", "throttling"},
	{"TooManyRequestsException", "transient", "throttling"},
	{"Rate exceeded", "transient", "throttling"},
	{"SlowDown", "transient", "throttling"},
	{"ProvisionedThroughputExceeded", "transient", "throttling"},
	{"RequestThrottled", "transient", "throttling"},
	{"PriorRequestNotComplete", "transient", "throttling"},
	{"InternalError", "transient", "provider_5xx"},
	{"InternalFailure", "transient", "provider_5xx"},
	{"InternalServerError", "transient", "provider_5xx"},
	{"ServiceUnavailable", "transient", "provider_5xx"},
	{"ServiceFailure", "transient", "provider_5xx"},
	{"Bad Gateway", "transient", "provider_5xx"},
	{"Service Unavailable", "transient", "provider_5xx"},
	{"Gateway Timeout", "transient", "provider_5xx"},
	{"i/o timeout", "transient", "timeout"},
	{"context deadline exceeded", "transient", "timeout"},
	{"TLS handshake timeout", "transient", "timeout"},
	{"connection reset by peer", "transient", "timeout"},
	{"connection refused", "transient", "timeout"},
	{"no such host", "transient", "timeout"},
	{"RequestTimeout", "transient", "timeout"},
	{"request canceled", "transient", "timeout"},
	{"unexpected EOF", "transient", "timeout"},
	{"use of closed network connection", "transient", "timeout"},
	{"Error acquiring the state lock", "transient", "lock_contention"},
	{"Error locking state", "transient", "lock_contention"},
	{"ConditionalCheckFailedException", "transient", "lock_contention"},
	{"PreconditionFailed", "transient", "lock_contention"},
	{"runner has received a shutdown signal", "transient", "runner_lost"},
	{"lost communication with the server", "transient", "runner_lost"},
	{"The hosted runner encountered an error", "transient", "runner_lost"},

	// config: the change itself is wrong
	{"plan_mismatch", "config", "plan_mismatch"},
	{"differs from the approved plan", "config", "plan_mismatch"},
	{"plan hash mismatch", "config", "plan_mismatch"},
	{"Invalid value for variable", "config", "validation"},
	{"Invalid value for input", "config", "validation"},
	{"Unsupported argument", "config", "validation"},
	{"Missing required argument", "config", "validation"},
	{"Reference to undeclared", "config", "validation"},
	{"Unsupported attribute", "config", "validation"},
	{"Invalid reference", "config", "validation"},
	{"InvalidParameterCombination", "config", "validation"},
	{"InvalidParameterValue", "config", "validation"},
	{"ValidationException", "config", "validation"},
	{"ValidationError", "config", "validation"},
	{"MalformedPolicyDocument", "config", "validation"},
	{"InvalidRequest", "config", "validation"},
	{"AlreadyExists", "config", "validation"},
	{"already exists", "config", "validation"},
	{"ResourceInUseException", "config", "validation"},
	{"DuplicateRecord", "config", "validation"},
	{"ResourceConflictException", "config", "validation"},

	// infra: the apply reached real infrastructure and failed there
	{"LimitExceeded", "infra", "quota"},
	{"InsufficientInstanceCapacity", "infra", "quota"},
	{"QuotaExceeded", "infra", "quota"},
	{"TooManyBuckets", "infra", "quota"},
	{"ServiceQuota", "infra", "quota"},
	{"AccessDenied", "infra", "permission_denied"},
	{"UnauthorizedOperation", "infra", "permission_denied"},
	{"is not authorized to perform", "infra", "permission_denied"},
	{"explicit deny", "infra", "permission_denied"},
	{"AuthorizationError", "infra", "permission_denied"},
	{"InvalidClientTokenId", "infra", "permission_denied"},
}
