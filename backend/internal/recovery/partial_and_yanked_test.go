package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// A partial apply that ends in a throttling error is classed transient and retried once.
// The retried run re-plans against what the failed run created, so its plan no longer
// matches the approved hash: apply.yml fails before applying (plan_mismatch), that failure
// is a config error, and no further retry is scheduled. This pins the behaviour: transient
// patterns win over a partial apply, and the plan-hash gate makes the retry safe.
func TestPartialApplyRetryEndsInPlanMismatch(t *testing.T) {
	tgt := applyingTarget()
	repo := &memRepo{s: newMem(tgt)}
	r := NewRetrier(repo, platform.FixedClock{T: now}, platform.RandomIDs{})
	ctx := context.Background()

	partial := throttled(tgt, 1)
	partial.Message = "Error: creating SQS Queue (orders-dlq) [api error ThrottlingException; StatusCode: 400; Rate exceeded]"
	out, err := r.OnApplyFailure(ctx, partial)
	if err != nil || out.Decision != DecisionRetryScheduled || out.Class != ClassTransient {
		t.Fatalf("partial apply: %+v, %v", out, err)
	}

	for i, msg := range []string{
		"plan_mismatch: the re-plan (ab12) differs from the approved plan (cd34); re-validation required",
		"apply.yml did not finish (failure); see the workflow run log", // what the summary reports today
	} {
		f := throttled(tgt, int64(2+i))
		f.Stage, f.Message, f.ExitCode = "summary", msg, nil
		got, err := r.OnApplyFailure(ctx, f)
		if err != nil || got.Decision != DecisionNotTransient || got.Class != ClassConfig {
			t.Fatalf("retried run %d: %+v, %v", i, got, err)
		}
	}
	if repo.s.target.RetryCount != 1 || len(repo.s.jobs) != 1 {
		t.Fatalf("retry_count %d jobs %d: the mismatch must not be retried", repo.s.target.RetryCount, len(repo.s.jobs))
	}
}

func TestYankedTargetError(t *testing.T) {
	target := AppliedSpec{ShortID: "req-older01"}
	yanked := apperr.Invalid("validation_failed", "1 resource inputs are invalid",
		apperr.FieldError{Pointer: "/resources/0/version", Code: "module_version_unavailable", Message: "sqs@1.3.0 is yanked"})
	err := YankedTargetError(yanked, target)
	if code(err) != "rollback_target_yanked" || !apperr.IsKind(err, apperr.KindInvalid) || !strings.Contains(err.Error(), "req-older01") {
		t.Fatalf("yanked: %v", err)
	}
	if e, _ := apperr.As(err); len(e.Fields) != 1 || e.Fields[0].Pointer != "/resources/0/version" {
		t.Fatalf("fields kept: %+v", e)
	}
	missing := apperr.Invalid("module_version_unavailable", "requested module versions are unavailable")
	if code(YankedTargetError(missing, target)) != "rollback_target_yanked" {
		t.Fatal("a version gone from the catalog is explained the same way")
	}
	for _, other := range []error{apperr.Conflict("stack_busy", "busy"), apperr.Invalid("validation_failed", "x"), errors.New("db down")} {
		if YankedTargetError(other, target) != other {
			t.Errorf("%v must pass through", other)
		}
	}
}
