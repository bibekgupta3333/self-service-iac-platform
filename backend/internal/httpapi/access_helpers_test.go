package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// allowAllAccess lets handler tests run without a database: every team is visible.
type allowAllAccess struct{}

func (allowAllAccess) Visible(context.Context, identity.Principal) (identity.Visibility, error) {
	return identity.Visibility{All: true}, nil
}

func (allowAllAccess) CanView(context.Context, identity.Visibility, uuid.UUID) (bool, error) {
	return true, nil
}
