package requests

import (
	"context"
	"errors"
	"fmt"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// errNoLiveKey rolls back the probe claim of Replay.
var errNoLiveKey = errors.New("no live idempotency key")

// Replay returns the request an earlier call with the same Idempotency-Key created, so an
// endpoint that checks preconditions before Create (POST /v1/stacks/{id}/rollback) can
// answer a retried call with the original request instead of refusing it because that
// request now exists. found is false when the key is unused or expired; a key used with a
// different body is idempotency_key_reuse, as in Create.
//
// It probes with the same claim Create makes, in a transaction that is always rolled back
// when the key is free, so it reserves nothing.
func (s *Service) Replay(ctx context.Context, by identity.Principal, key, requestHash string) (*Request, bool, error) {
	if err := ValidateIdempotencyKey(key); err != nil {
		return nil, false, err
	}
	var out *Request
	err := s.repo.InTx(ctx, func(st Store) error {
		claimed, err := st.ClaimIdempotencyKey(ctx, NewIdempotencyClaim(by.UserID, key, requestHash, s.clock.Now()))
		if err != nil {
			return fmt.Errorf("probe idempotency key: %w", err)
		}
		if claimed {
			return errNoLiveKey
		}
		res, err := s.replay(ctx, st, by, CreateCommand{IdempotencyKey: key, RequestHash: requestHash})
		if err != nil {
			return err
		}
		out = res.Request
		return nil
	})
	switch {
	case errors.Is(err, errNoLiveKey):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return out, true, nil
}
