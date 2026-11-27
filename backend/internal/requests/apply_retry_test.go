package requests

import (
	"context"
	"testing"
)

type fakeRetry struct {
	retry bool
	calls int
}

func (f *fakeRetry) RetryIn(_ context.Context, _ *Request, _ ApplyProgress, _ string) (bool, error) {
	f.calls++
	return f.retry, nil
}

func applyWithRetry(t *testing.T, repo *memRepo, svc *Service, r *Request, p ApplyProgress, policy RetryPolicy) ApplyResult {
	t.Helper()
	var res ApplyResult
	err := repo.InTx(context.Background(), func(st Store) error {
		var err error
		res, err = svc.ApplyIn(context.Background(), st, ApplyInput{RequestID: r.ID, Progress: p, By: dev, CorrelationID: "run-77", Retry: policy})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A failure the retry policy takes over leaves the request applying and the stack as it is.
func TestApplyInLeavesARetriedFailureApplying(t *testing.T) {
	repo, svc, r := applyFixture(t, KindUpdate, StackActive, StatusApplying)
	repo.setStackStatus(r.Stack.ID, StackUpdating)
	policy := &fakeRetry{retry: true}
	res := applyWithRetry(t, repo, svc, r, failed(FailureApplyFailed), policy)
	if policy.calls != 1 || res.Status != StatusApplying || !res.Changed {
		t.Fatalf("result %+v, calls %d", res, policy.calls)
	}
	if r.Status != StatusApplying || r.CompletedAt != nil || repo.state.status[r.Stack.ID] != StackUpdating {
		t.Fatalf("request %s, stack %s", r.Status, repo.state.status[r.Stack.ID])
	}
}

// A failure the policy declines fails the request as before; successes never ask it.
func TestApplyInFailsWhenTheRetryPolicyDeclines(t *testing.T) {
	repo, svc, r := applyFixture(t, KindUpdate, StackActive, StatusApplying)
	repo.setStackStatus(r.Stack.ID, StackUpdating)
	policy := &fakeRetry{}
	applyWithRetry(t, repo, svc, r, failed(FailureApplyFailed), policy)
	if policy.calls != 1 || r.Status != StatusFailed || repo.state.status[r.Stack.ID] != StackFailed {
		t.Fatalf("request %s, stack %s, calls %d", r.Status, repo.state.status[r.Stack.ID], policy.calls)
	}

	repo, svc, r = applyFixture(t, KindUpdate, StackActive, StatusApplying)
	policy = &fakeRetry{retry: true}
	applyWithRetry(t, repo, svc, r, succeeded(HealthHealthy, `{}`), policy)
	if policy.calls != 0 || r.Status != StatusApplied {
		t.Fatalf("success asked the retry policy: %d, %s", policy.calls, r.Status)
	}
}
