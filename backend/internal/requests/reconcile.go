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

// ReconcileStack opens an update request that re-applies the stack's current recorded
// configuration (the spec of its newest create or update that is not failed, rejected or
// cancelled) to revert drift. It goes through Create, so it is authorized against the
// caller's role in the stack's team, queues behind any request holding the stack lock,
// opens its PR and is reviewed and applied like any other update. key makes a retry return
// the request the first call opened.
func (s *Service) ReconcileStack(ctx context.Context, by identity.Principal, stackID uuid.UUID, justification, key string) (*Request, error) {
	var (
		raw json.RawMessage
		ok  bool
	)
	err := s.repo.InTx(ctx, func(st Store) error {
		var err error
		raw, ok, err = st.LatestSpec(ctx, stackID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load the stack's recorded configuration: %w", err)
	}
	if !ok {
		return nil, apperr.Conflict("no_recorded_spec", "the stack has no recorded configuration to reconcile to")
	}
	var spec NormalizedSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode the stack's recorded configuration: %w", err)
	}
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
