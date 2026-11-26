package requests

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
)

func TestParseLookupPath(t *testing.T) {
	const ok = "environments/development/payments/order-events"
	tests := []struct {
		path string
		want bool
	}{
		{ok, true},
		{"environments/production/payments/o1", true},
		{ok + "/", false}, // one stack, one spelling
		{"", false},
		{"development/payments/order-events", false},
		{"environments/dev/payments/order-events", false},
		{"environments/development/Payments/order-events", false},
		{"environments/development/payments/order_events", false},
		{"environments/development/payments/order-events/../x", false},
		{"environments/development/payments", false},
		{"/environments/development/payments/order-events", false},
		{"environments/development/payments/order-events\n", false},
		{"environments/development//order-events", false},
	}
	for _, tt := range tests {
		got, err := ParseLookupPath(tt.path)
		if (err == nil) != tt.want {
			t.Errorf("ParseLookupPath(%q) = %q, %v", tt.path, got, err)
		}
		if err != nil && !hasCode(err, "invalid_stack_path") {
			t.Errorf("ParseLookupPath(%q): code = %v", tt.path, err)
		}
		if err == nil && got != tt.path {
			t.Errorf("ParseLookupPath(%q) = %q", tt.path, got)
		}
	}
}

func holder(status Status, mutate func(h *StackHolder)) StackHolder {
	stack, req := uuid.MustParse("b2c1f0a9-3e4d-4c5b-8a7f-1e2d3c4b5a69"), uuid.MustParse("0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90")
	h := StackHolder{
		StackID: stack, StackPath: "environments/development/payments/order-events",
		Lock: &locks.Lock{StackID: stack, RequestID: req, FencingToken: 48214, ExpiresAt: t0.Add(time.Hour)},
		Request: &Request{ID: req, ShortID: "req-7k3p9x", Kind: KindUpdate, Status: status, ApprovedPlanHash: strings.Repeat("ab", 32), MergeCommitSHA: strings.Repeat("c", 40),
			Stack: StackRef{ID: stack}},
	}
	if mutate != nil {
		mutate(&h)
	}
	return h
}

func TestResolveApplyTarget(t *testing.T) {
	head, other := strings.Repeat("c", 40), strings.Repeat("d", 40)
	tests := []struct {
		name     string
		holder   StackHolder
		head     string
		wantCode string // "" means success
		wantKind apperr.Kind
	}{
		{name: "approved", holder: holder(StatusApproved, nil)},
		{name: "applying", holder: holder(StatusApplying, nil)},
		{name: "head matches the merge", holder: holder(StatusApplying, func(h *StackHolder) { h.Request.MergeCommitSHA = head }), head: head},
		{name: "merge not recorded, no head", holder: holder(StatusApproved, func(h *StackHolder) { h.Request.MergeCommitSHA = "" }),
			wantCode: "merge_not_recorded", wantKind: apperr.KindConflict},
		{name: "merge not recorded, head sent", holder: holder(StatusApproved, func(h *StackHolder) { h.Request.MergeCommitSHA = "" }), head: head,
			wantCode: "merge_not_recorded", wantKind: apperr.KindConflict},
		{name: "head differs from the merge", holder: holder(StatusApplying, func(h *StackHolder) { h.Request.MergeCommitSHA = head }),
			head: other, wantCode: "merge_commit_mismatch", wantKind: apperr.KindConflict},
		{name: "no lock", holder: holder(StatusApproved, func(h *StackHolder) { h.Lock, h.Request = nil, nil }),
			wantCode: "no_active_request", wantKind: apperr.KindNotFound},
		{name: "expired lease", holder: holder(StatusApproved, func(h *StackHolder) { h.Lock.ExpiresAt = t0 }),
			wantCode: "no_active_request", wantKind: apperr.KindNotFound},
		{name: "draft holder", holder: holder(StatusDraft, nil), wantCode: "request_not_approved", wantKind: apperr.KindConflict},
		{name: "plan_ready holder", holder: holder(StatusPlanReady, nil), wantCode: "request_not_approved", wantKind: apperr.KindConflict},
		{name: "applied holder", holder: holder(StatusApplied, nil), wantCode: "request_not_approved", wantKind: apperr.KindConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveApplyTarget(tt.holder, tt.head, t0)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				h := tt.holder
				if got.StackID != h.StackID || got.RequestID != h.Request.ID || got.FencingToken != 48214 ||
					got.ApprovedPlanHash != strings.Repeat("ab", 32) || got.RequestShortID != "req-7k3p9x" || got.Kind != KindUpdate {
					t.Fatalf("target = %+v", got)
				}
				return
			}
			e, ok := apperr.As(err)
			if !ok || e.Code != tt.wantCode || e.Kind != tt.wantKind {
				t.Fatalf("err = %v, want %s", err, tt.wantCode)
			}
		})
	}
}

func TestResolveApplyTargetRejectsMismatchedHolder(t *testing.T) {
	h := holder(StatusApproved, func(h *StackHolder) { h.Lock.RequestID = uuid.New() })
	if _, err := ResolveApplyTarget(h, "", t0); err == nil || apperr.IsKind(err, apperr.KindNotFound) || apperr.IsKind(err, apperr.KindConflict) {
		t.Fatalf("a lock that names another request is an internal inconsistency, got %v", err)
	}
}
