package runs

import (
	"context"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// noTx fails the test if the service reaches the database.
type noTx struct{ t *testing.T }

func (r noTx) InTx(context.Context, func(Store) error) error {
	r.t.Fatal("rejected reports must not open a transaction")
	return nil
}

func TestRecordRejectsBeforeTouchingTheDatabase(t *testing.T) {
	svc := NewService(noTx{t}, platform.SystemClock{})
	invalid := validReport()
	invalid.Stage = ""
	forged := &identity.Workflow{Ref: MainRef, JobWorkflowRef: repo + "/.github/workflows/apply.yml@" + MainRef, RunID: 1, RunAttempt: 1}
	tests := []struct {
		name string
		by   identity.Principal
		r    Report
		code string
	}{
		{name: "invalid", r: invalid, code: "validation_failed"},
		{name: "another run's identity", by: identity.Principal{Workflow: forged}, r: validReport(), code: "run_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Record(context.Background(), tt.by, tt.r)
			if e, ok := apperr.As(err); !ok || e.Code != tt.code {
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}
}
