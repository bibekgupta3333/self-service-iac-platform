package requests

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// ReconcileInput builds the update request that re-asserts spec on stack: the same
// resources the stack's newest recorded request asked for, so the plan reverts whatever
// changed out of band. It names the stack by id only, like every update. Pure.
func ReconcileInput(stackID uuid.UUID, spec NormalizedSpec, justification string) CreateInput {
	return CreateInput{Kind: KindUpdate, StackID: &stackID, Justification: justification, Resources: spec.Resources}
}

// ReconcileStack opens an update request that re-applies the stack's last applied
// configuration (the spec of stacks.last_applied_request_id, never an unapplied draft or
// an update still under review) to revert drift, and only while no request is open. It goes through Create, so it is authorized against the
// caller's role in the stack's team, queues behind any request holding the stack lock,
// opens its PR and is reviewed and applied like any other update. key makes a retry return
// the request the first call opened.
func (s *Service) ReconcileStack(ctx context.Context, by identity.Principal, stackID uuid.UUID, justification, key string) (*Request, error) {
	var (
		raw      json.RawMessage
		ok       bool
		open     []Kind
		replayed *Request
	)
	err := s.repo.InTx(ctx, func(st Store) error {
		var err error
		// A retry with a key that already opened a request returns that request, even though
		// it is now the open one. A lookup error means "no such key"; real failures surface
		// in the calls below.
		if rec, kerr := st.GetIdempotencyKey(ctx, by.UserID, key); kerr == nil && rec.RequestID != uuid.Nil {
			replayed, err = st.GetRequest(ctx, rec.RequestID)
			return err
		}
		if open, err = st.OpenRequestKinds(ctx, stackID); err != nil {
			return err
		}
		raw, ok, err = st.LastAppliedSpec(ctx, stackID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load the stack's applied configuration: %w", err)
	}
	if replayed != nil {
		return replayed, nil
	}
	if len(open) > 0 {
		return nil, apperr.Conflict("stack_busy",
			"a request is open for the stack; it re-asserts the configuration when it applies, so a reconcile now would race it")
	}
	if !ok {
		return nil, apperr.Conflict("no_recorded_spec", "no request has applied to the stack, so there is no applied configuration to reconcile to")
	}
	var spec NormalizedSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode the stack's applied configuration: %w", err)
	}
	if len(spec.Resources) == 0 {
		return nil, apperr.Conflict("no_recorded_spec", "the stack's last applied configuration has no resources to reconcile to")
	}
	return s.reconcileCreate(ctx, by, stackID, spec, justification, key)
}

// reconcileCreate sends the update through Create. The request hash covers the stack and
// the key only, so a replay (which ignores the spec) returns the first request.
func (s *Service) reconcileCreate(ctx context.Context, by identity.Principal, stackID uuid.UUID, spec NormalizedSpec,
	justification, key string) (*Request, error) {
	res, err := s.Create(ctx, by, CreateCommand{
		Input:          ReconcileInput(stackID, spec, justification),
		IdempotencyKey: key,
		RequestHash:    RequestHash("POST", "/v1/drift/reconcile", []byte(stackID.String()+"|"+key)),
	})
	if err != nil {
		return nil, err
	}
	return res.Request, nil
}
