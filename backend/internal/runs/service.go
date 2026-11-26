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
	// InsertRun inserts a new run with r.Stage as its only stage. inserted is false when a
	// run with the same run id, attempt and stack exists (ON CONFLICT DO NOTHING).
	InsertRun(ctx context.Context, r Run) (id uuid.UUID, inserted bool, err error)
	// LockRun loads the existing run FOR UPDATE.
	LockRun(ctx context.Context, stackID uuid.UUID, gh GitHubRun) (StoredRun, error)
	// UpdateRun writes the merged state of an existing run.
	UpdateRun(ctx context.Context, u RunUpdate) error
}

// StoredRun is an existing pipeline_runs row.
type StoredRun struct {
	ID     uuid.UUID
	Kind   Kind
	Status Status
	Stages []json.RawMessage
}

// RunUpdate is the new state of an existing run. FinishedAt is set only when the run
// finishes with this report; LogURL, JobID and RequestID only fill in missing values.
type RunUpdate struct {
	ID         uuid.UUID
	Status     Status
	Stages     []json.RawMessage
	FinishedAt *time.Time
	JobID      int64
	LogURL     string
	RequestID  *uuid.UUID
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
		out.RunID, out.RunStatus, err = s.store(ctx, st, run, r.Stage)
		return err
	})
	if err != nil {
		return Recorded{}, err
	}
	return out, nil
}

// store inserts the run, or merges the report into the existing run: the stage replaces
// any earlier entry of the same name (replays are idempotent), and a finished run keeps
// its status.
func (s *Service) store(ctx context.Context, st Store, run Run, stage string) (uuid.UUID, Status, error) {
	id, inserted, err := st.InsertRun(ctx, run)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert run: %w", err)
	}
	if inserted {
		return id, run.Status, nil
	}
	cur, err := st.LockRun(ctx, run.StackID, run.GitHub)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("load run: %w", err)
	}
	if cur.Kind != run.Kind {
		return uuid.Nil, "", apperr.Conflict("run_kind_mismatch",
			fmt.Sprintf("run %d/%d was already reported as %s", run.GitHub.RunID, run.GitHub.RunAttempt, cur.Kind))
	}
	stages, err := MergeStages(cur.Stages, stage, run.Stage)
	if err != nil {
		return uuid.Nil, "", err
	}
	u := RunUpdate{ID: cur.ID, Status: NextRunStatus(cur.Status, run.Status), Stages: stages, JobID: run.GitHub.JobID,
		LogURL: run.LogURL, RequestID: run.RequestID}
	if !cur.Status.Finished() && u.Status.Finished() {
		u.FinishedAt = &run.At
	}
	if err := st.UpdateRun(ctx, u); err != nil {
		return uuid.Nil, "", fmt.Errorf("update run: %w", err)
	}
	return cur.ID, u.Status, nil
}
