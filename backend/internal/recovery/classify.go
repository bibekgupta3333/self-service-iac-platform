// Package recovery returns a stack to a known-good state after a failed or harmful change
// (docs/06 §5): it classifies failed pipeline runs, retries transient apply failures,
// opens rollback requests and records break-glass state surgery in the audit log.
//
// The decisions are pure functions (Classify, PlanRetry, Backoff, SelectRollbackTarget,
// ValidateSurgeryReport). The services around them (Retrier, Dispatcher, RollbackService,
// SurgeryRecorder) take small ports and do the I/O through adapters.
package recovery

import (
	"regexp"
	"slices"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tferror"
)

// Class is the kind of a pipeline failure (enum failure_class).
type Class string

// Failure classes.
const (
	// ClassTransient: the platform or the cloud had a moment (throttling, a timeout, a
	// provider 5xx, state-lock contention, a lost runner). Running again is expected to work.
	ClassTransient Class = "transient"
	// ClassConfig: the change itself is wrong (validation, policy deny, plan error, plan
	// mismatch). Running it again fails the same way; the request must change.
	ClassConfig Class = "config"
	// ClassInfra: the apply reached real infrastructure and failed there (partial apply,
	// quota, permissions, a failed smoke test). A human decides: retry, roll forward or roll back.
	ClassInfra Class = "infra"
)

// Classification is the outcome of Classify.
type Classification struct {
	Class Class `json:"class"`
	// Reason is a stable snake_case code: throttling, timeout, provider_5xx,
	// lock_contention, eventual_consistency, runner_lost, policy_denied, plan_mismatch,
	// validation, plan_error, unclassified, quota, permission_denied, partial_apply, smoke_failed.
	Reason string `json:"reason"`
}

// Failure is what a failed pipeline stage told the platform.
type Failure struct {
	Stage    string // stage name of the run report (plan, apply, destroy, smoke, summary, ...)
	ExitCode *int   // terraform exit code, when the stage ran Terraform
	Message  string // error message or log excerpt; may be empty
}

// maxScan bounds how much error text is matched. Terraform's own error is at the start of
// an excerpt; a megabyte of log would only slow the regular expressions down.
const maxScan = 64 << 10

type rule struct {
	reason string
	re     *regexp.Regexp
}

// statusRules are the HTTP-status forms the excerpt carries as "StatusCode: <nnn>" (and an
// AWS SDK v1 "status code: <nnn>" in raw text), checked after the vocabulary entries of
// their class, so a named error code gives the reason ("RequestLimitExceeded ... 503" is
// throttling).
var statusRules = map[Class][]rule{
	ClassTransient: {
		{"throttling", regexp.MustCompile(`(?i)\bstatus ?code:? ?429\b`)},
		{"provider_5xx", regexp.MustCompile(`(?i)\bstatus ?code:? ?5\d\d\b`)},
	},
}

// policyRule matches the conftest output of a policy stage. It is not Terraform output, so
// it is not in the vocabulary; a policy stage reports its own short message.
var policyRule = rule{"policy_denied", regexp.MustCompile(`(?i)(^|\n)\s*deny: |policy (check )?denied|\bFAIL - |conftest .*fail`)}

// rulesFor builds the rules of one class from tferror.Vocabulary, the single list of
// signals the pipeline excerpt can carry: a phrase the classifier matches is always one the
// excerpt keeps (case-insensitive substring, in Vocabulary order). Pure.
func rulesFor(class Class) []rule {
	var out []rule
	for _, v := range tferror.Vocabulary {
		if Class(v.Class) == class {
			out = append(out, rule{v.Reason, regexp.MustCompile(`(?i)` + regexp.QuoteMeta(v.Text))})
		}
	}
	return append(out, statusRules[class]...)
}

var (
	// transientRules are checked first and win over everything else: "ThrottlingException"
	// in an otherwise unknown apply error is still throttling. Eventual consistency comes
	// before the AWS validation code it is reported under (InvalidParameterValueException).
	transientRules = rulesFor(ClassTransient)
	// configRules match a wrong change: re-running it fails the same way.
	configRules = append([]rule{policyRule}, rulesFor(ClassConfig)...)
	// infraRules refine an apply-stage failure that is neither transient nor a config error.
	infraRules = rulesFor(ClassInfra)
)

// applyStages run terraform apply: a failure there may have changed infrastructure.
var applyStages = []string{"apply", "destroy"}

// IsApplyStage reports whether stage ran terraform apply (or destroy). Pure.
func IsApplyStage(stage string) bool { return slices.Contains(applyStages, stage) }

// signalExit is the smallest exit status of a process killed by a signal (128 + n):
// 137 is SIGKILL (out of memory, a cancelled runner), 143 SIGTERM.
const signalExit = 128

// Classify maps a failed stage to a failure class and reason. Pure and total: every input
// gets a class, and unknown failures are never transient, so nothing unknown is retried.
//
// Order:
//  1. transient patterns, anywhere (throttling during plan is as transient as during apply);
//  2. a process killed by a signal (exit status above 128): the runner went away;
//  3. config patterns (policy deny, plan mismatch, validation and name conflicts);
//  4. by stage: an apply or destroy stage is infra (quota, permission_denied, otherwise
//     partial_apply), smoke is infra (smoke_failed), a policy stage is policy_denied, a plan
//     stage plan_error, and anything else before apply config/unclassified.
func Classify(f Failure) Classification {
	text := f.Message
	if len(text) > maxScan {
		text = text[:maxScan]
	}
	if r, ok := match(transientRules, text); ok {
		return Classification{Class: ClassTransient, Reason: r}
	}
	if f.ExitCode != nil && *f.ExitCode > signalExit {
		return Classification{Class: ClassTransient, Reason: "runner_lost"}
	}
	if r, ok := match(configRules, text); ok {
		return Classification{Class: ClassConfig, Reason: r}
	}
	stage := strings.ToLower(f.Stage)
	switch {
	case IsApplyStage(stage):
		if r, ok := match(infraRules, text); ok {
			return Classification{Class: ClassInfra, Reason: r}
		}
		return Classification{Class: ClassInfra, Reason: "partial_apply"}
	case stage == "smoke":
		return Classification{Class: ClassInfra, Reason: "smoke_failed"}
	case stage == "policy" || stage == "conftest" || stage == "opa":
		return Classification{Class: ClassConfig, Reason: "policy_denied"}
	case stage == "plan":
		return Classification{Class: ClassConfig, Reason: "plan_error"}
	}
	return Classification{Class: ClassConfig, Reason: "unclassified"}
}

func match(rules []rule, text string) (string, bool) {
	if text == "" {
		return "", false
	}
	for _, r := range rules {
		if r.re.MatchString(text) {
			return r.reason, true
		}
	}
	return "", false
}
