package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// RequestRef is the request a report belongs to.
type RequestRef struct {
	ID      uuid.UUID
	StackID uuid.UUID
	Status  string
}

// Run is a pipeline_runs row to insert or merge into.
type Run struct {
	RequestID   *uuid.UUID
	StackID     uuid.UUID
	Kind        Kind
	Status      Status
	GitHub      GitHubRun
	WorkflowRef string
	Stage       json.RawMessage
	LogURL      string
	At          time.Time
}

// Store is the transactional persistence port.
type Store interface {
	// StackIDByPath resolves and locks (FOR KEY SHARE) the stack row. It is called first, so
	// the lock order stacks → locks matches the lock service and cannot deadlock with it.
	StackIDByPath(ctx context.Context, path string) (uuid.UUID, error)
	RequestByShortID(ctx context.Context, shortID string) (RequestRef, error)
	// StackLock returns the stack's lock row, locked FOR SHARE so it cannot be released
	// while the report commits; nil when the stack is not locked.
	StackLock(ctx context.Context, stackID uuid.UUID) (*locks.Lock, error)
	// UpsertRun inserts the run or appends the stage to the existing one (same run id,
	// attempt and stack). A finished run keeps its status.
	UpsertRun(ctx context.Context, r Run) (id uuid.UUID, status Status, err error)
}

// Repository is the persistence port of the run service.
type Repository interface {
	InTx(ctx context.Context, fn func(Store) error) error
}

// Recorded is the outcome of Record.
type Recorded struct {
	RunID         uuid.UUID
	RunStatus     Status
	RequestStatus *string
}

// Service records run reports.
type Service struct {
	repo  Repository
	clock platform.Clock
}

// NewService returns a Service.
func NewService(repo Repository, clock platform.Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

// Record validates a report from by, checks its fencing token against the stack lock
// and stores it. Request status transitions driven by reports arrive with WBS 5.2/5.3.
func (s *Service) Record(ctx context.Context, by identity.Principal, r Report) (Recorded, error) {
	if errs := Validate(r); len(errs) > 0 {
		return Recorded{}, apperr.Invalid("validation_failed", "run report is invalid", errs...)
	}
	if err := CheckProvenance(r, by.Workflow); err != nil {
		return Recorded{}, err
	}
	var out Recorded
	err := s.repo.InTx(ctx, func(st Store) error {
		stackID, err := st.StackIDByPath(ctx, r.StackPath)
		if err != nil {
			return err
		}
		run := Run{StackID: stackID, Kind: r.Pipeline, Status: RunStatus(r.Stage, r.Status), GitHub: r.GitHub,
			WorkflowRef: by.Login, LogURL: r.LogURL, At: s.clock.Now()}
		if r.RequestShortID != "" {
			req, err := st.RequestByShortID(ctx, r.RequestShortID)
			if err != nil {
				return err
			}
			if req.StackID != stackID {
				return apperr.Invalid("validation_failed", "request does not target this stack",
					apperr.FieldError{Pointer: "/request_short_id", Code: "stack_mismatch", Message: "request belongs to another stack"})
			}
			run.RequestID, out.RequestStatus = &req.ID, &req.Status
		}
		if r.FencingToken != nil {
			lock, err := st.StackLock(ctx, stackID)
			if err != nil {
				return err
			}
			if run.RequestID != nil {
				err = locks.CheckToken(lock, *run.RequestID, *r.FencingToken)
			} else {
				err = locks.CheckStackToken(lock, *r.FencingToken)
			}
			if err != nil {
				return err
			}
		}
		if run.Stage, err = StageEntry(r, run.At); err != nil {
			return err
		}
		out.RunID, out.RunStatus, err = st.UpsertRun(ctx, run)
		if err != nil {
			return fmt.Errorf("record run: %w", err)
		}
		return nil
	})
	if err != nil {
		return Recorded{}, err
	}
	return out, nil
}
