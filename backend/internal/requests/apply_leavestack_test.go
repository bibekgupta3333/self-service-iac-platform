package requests

import "testing"

// A failure recovery reports while another request holds the stack lock fails the request
// and leaves the stack to the lock holder.
func TestDecideApplyLeaveStack(t *testing.T) {
	p := failed(FailureApplyFailed)
	p.LeaveStack = true
	d, err := DecideApply(StatusApplying, KindUpdate, StackUpdating, p)
	if err != nil || len(d.Events) != 1 || d.Events[0] != EventApplyFailed || d.Stack != nil {
		t.Fatalf("decision %+v, %v", d, err)
	}
	d, _ = DecideApply(StatusApplying, KindUpdate, StackUpdating, failed(FailureApplyFailed))
	if d.Stack == nil || d.Stack.To != StackFailed {
		t.Fatalf("without LeaveStack the stack fails: %+v", d)
	}
}
