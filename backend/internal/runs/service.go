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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RequestRef is the request a report belongs to.
type RequestRef struct {
	ID      uuid.UUID
	StackID uuid.UUID
	Status  string
	// FencingToken is the token of the lease the request was granted (it is not cleared
	// when the lease is released).
	FencingToken *int64
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
	// StackIDByPath resolves and locks (FOR UPDATE) the stack row. It is called first, so
	// the lock order stacks → locks matches the lock service and cannot deadlock with it,
	// and reports for one stack are applied one at a time (they now move requests and the
	// stack itself).
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
	// FindRun loads the existing run without locking it; nil when there is none.
	FindRun(ctx context.Context, stackID uuid.UUID, gh GitHubRun) (*StoredRun, error)
	// UpdateRun writes the merged state of an existing run.
	UpdateRun(ctx context.Context, u RunUpdate) error
	// Applier is the request and stack side of the report, in the same transaction.
	Applier() requests.Applier
}

// StoredRun is an existing pipeline_runs row.
type StoredRun struct {
	ID        uuid.UUID
	RequestID *uuid.UUID
	Kind      Kind
	Status    Status
	Stages    []json.RawMessage
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

// Record validates a report from by, checks its fencing token against the stack lock,
// stores it in its run and, for the apply pipeline, drives the request and the stack
// (requests.Service.ApplyIn) in the same transaction. A validate report is stored and
// nothing more: a passed validation is never an approval.
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
		run := Run{StackID: stackID, Kind: r.Pipeline, GitHub: r.GitHub, WorkflowRef: by.Login, LogURL: r.LogURL,
			At: s.clock.Now()}
		var req RequestRef
		var released *StoredRun // set when the report rides on a released token
		if r.RequestShortID != "" {
			if req, err = st.RequestByShortID(ctx, r.RequestShortID); err != nil {
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
			switch {
			case err == nil:
			case AcceptsReleasedToken(r, req):
				// A terminal request whose lease the backend already released. Only a run
				// the API already recorded for this request may keep reporting; a new run id
				// or attempt is stale.
				if released, err = st.FindRun(ctx, stackID, r.GitHub); err == nil &&
					(released == nil || released.RequestID == nil || *released.RequestID != req.ID || released.Kind != r.Pipeline) {
					err = apperr.Conflict("stale_fencing_token", "the request is complete and this run never reported for it")
				}
			}
			if err != nil {
				return err
			}
		}
		if run.Stage, err = StageEntry(r, run.At); err != nil {
			return err
		}
		run.Status = SettleRunStatus(r.Pipeline, RunStatus(r.Stage, r.Status), r.Stage, []StageRecord{{Name: r.Stage, Status: r.Status}})
		var stages []json.RawMessage
		if released != nil && r.Stage != StageReleaseName {
			// A late report for a finished request: the audit trail records it (the lifecycle
			// answers request.run_report_ignored), the run's stage history stays as it was.
			out.RunID, out.RunStatus, stages = released.ID, released.Status, released.Stages
		} else if out.RunID, out.RunStatus, stages, err = s.store(ctx, st, run, r.Stage); err != nil {
			return err
		}
		if r.Pipeline.DrivesRequest() && run.RequestID != nil {
			return s.drive(ctx, st, by, r, run, out.RunStatus, stages, &out)
		}
		return nil
	})
	if err != nil {
		return Recorded{}, err
	}
	return out, nil
}

// drive applies an apply-pipeline report to its request and stack.
func (s *Service) drive(ctx context.Context, st Store, by identity.Principal, r Report, run Run, runStatus Status,
	stages []json.RawMessage, out *Recorded) error {
	records, err := ParseStages(stages)
	if err != nil {
		return err
	}
	res, err := st.Applier().Apply(ctx, requests.ApplyInput{RequestID: *run.RequestID, Progress: Progress(r, runStatus, records),
		By: by, CorrelationID: fmt.Sprintf("actions-run-%d-%d", r.GitHub.RunID, r.GitHub.RunAttempt)})
	if err != nil {
		return err
	}
	status := string(res.Status)
	out.RequestStatus = &status
	return nil
}

// store inserts the run, or merges the report into the existing run: the stage replaces
// any earlier entry of the same name (replays are idempotent), and a finished run keeps
// its status.
//
// run.Status is the status of this report alone; for an existing run it is settled again
// against all the run's stages (a failed summary that only the smoke test explains).
func (s *Service) store(ctx context.Context, st Store, run Run, stage string) (uuid.UUID, Status, []json.RawMessage, error) {
	id, inserted, err := st.InsertRun(ctx, run)
	if err != nil {
		return uuid.Nil, "", nil, fmt.Errorf("insert run: %w", err)
	}
	if inserted {
		return id, run.Status, []json.RawMessage{run.Stage}, nil
	}
	cur, err := st.LockRun(ctx, run.StackID, run.GitHub)
	if err != nil {
		return uuid.Nil, "", nil, fmt.Errorf("load run: %w", err)
	}
	if cur.Kind != run.Kind {
		return uuid.Nil, "", nil, apperr.Conflict("run_kind_mismatch",
			fmt.Sprintf("run %d/%d was already reported as %s", run.GitHub.RunID, run.GitHub.RunAttempt, cur.Kind))
	}
	stages, err := MergeStages(cur.Stages, stage, run.Stage)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	records, err := ParseStages(stages)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	reported := SettleRunStatus(run.Kind, run.Status, stage, records)
	u := RunUpdate{ID: cur.ID, Status: NextRunStatus(cur.Status, reported), Stages: stages, JobID: run.GitHub.JobID,
		LogURL: run.LogURL, RequestID: run.RequestID}
	if !cur.Status.Finished() && u.Status.Finished() {
		u.FinishedAt = &run.At
	}
	if err := st.UpdateRun(ctx, u); err != nil {
		return uuid.Nil, "", nil, fmt.Errorf("update run: %w", err)
	}
	return cur.ID, u.Status, stages, nil
}
