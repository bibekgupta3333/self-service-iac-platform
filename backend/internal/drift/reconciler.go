package drift

import (
	"context"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RequestReconciler adapts the request service to Reconciler.
type RequestReconciler struct{ svc *requests.Service }

var _ Reconciler = (*RequestReconciler)(nil)

// NewRequestReconciler returns a RequestReconciler.
func NewRequestReconciler(svc *requests.Service) *RequestReconciler {
	return &RequestReconciler{svc: svc}
}

// ReconcileStack implements Reconciler.
func (r *RequestReconciler) ReconcileStack(ctx context.Context, by identity.Principal, stackID uuid.UUID,
	justification, key string) (Reconciled, error) {
	req, err := r.svc.ReconcileStack(ctx, by, stackID, justification, key)
	if err != nil {
		return Reconciled{}, err
	}
	return Reconciled{RequestID: req.ID, ShortID: req.ShortID, Status: string(req.Status)}, nil
}
