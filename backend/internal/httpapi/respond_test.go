package httpapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

func TestProblemFor(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{apperr.NotFound("x"), 404, "not_found"},
		{apperr.Invalid("validation_failed", "x"), 422, "validation_failed"},
		{badRequest("x"), 400, "bad_request"},
		{apperr.Conflict("stack_exists", "x"), 409, "stack_exists"},
		{apperr.Forbidden("x"), 403, "forbidden"},
		{&apperr.Error{Kind: apperr.KindMethodNotAllowed, Code: "method_not_allowed"}, 405, "method_not_allowed"},
		{&apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated"}, 401, "unauthenticated"},
		{fmt.Errorf("wrapped: %w", apperr.Forbidden("x")), 403, "forbidden"},
		{errors.New("db down"), 500, "internal"},
	}
	for _, tt := range tests {
		p := problemFor(tt.err)
		if p.Status != tt.status || p.Code != tt.code {
			t.Errorf("problemFor(%v) = %d %s, want %d %s", tt.err, p.Status, p.Code, tt.status, tt.code)
		}
	}
}
