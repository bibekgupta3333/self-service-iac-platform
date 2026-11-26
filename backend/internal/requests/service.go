package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/inputschema"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// Team is the subset of a team the request service needs.
type Team struct {
	ID   uuid.UUID
	Slug string
}

// Environment is the subset of an environment the request service needs.
type Environment struct {
	ID             uuid.UUID
	Name           string
	DefaultRegion  string
	AllowedRegions []string
	AllowEphemeral bool
	MaxTTLHours    *int
}

// NewStack is a stack row created by a create request.
type NewStack struct {
	ID            uuid.UUID
	TeamID        uuid.UUID
	EnvironmentID uuid.UUID
	Name          string
	Path          string
	StateKey      string
	Region        string
	IsEphemeral   bool
	ExpiresAt     *time.Time
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
}

// IdempotencyWindow is how long an Idempotency-Key is remembered (docs/03 §5).
const IdempotencyWindow = 24 * time.Hour

// IdempotencyClaim is an attempt to reserve an Idempotency-Key for one user.
type IdempotencyClaim struct {
	UserID      uuid.UUID
	Key         string
	RequestHash string
	At          time.Time
	// ExpiredBefore: a stored key created before this instant is forgotten and may be reused.
	ExpiredBefore time.Time
}

// NewIdempotencyClaim builds a claim at now with the standard window. Pure.
func NewIdempotencyClaim(userID uuid.UUID, key, requestHash string, now time.Time) IdempotencyClaim {
	return IdempotencyClaim{UserID: userID, Key: key, RequestHash: requestHash, At: now, ExpiredBefore: now.Add(-IdempotencyWindow)}
}

// IdempotencyRecord is a stored, completed Idempotency-Key. Claim and completion commit
// in the same transaction, so a visible key always points at a request.
type IdempotencyRecord struct {
	RequestHash string
	RequestID   uuid.UUID
}

// Store is the transactional persistence port used while creating a request.
// Every method runs inside the same database transaction.
type Store interface {
	// ClaimIdempotencyKey drops the key if it expired, then inserts it; it returns false
	// if a live key already exists. A concurrent claim of the same key blocks until the
	// first transaction finishes.
	ClaimIdempotencyKey(ctx context.Context, c IdempotencyClaim) (bool, error)
	GetIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (IdempotencyRecord, error)
	CompleteIdempotencyKey(ctx context.Context, userID uuid.UUID, key string, requestID uuid.UUID) error
	TeamBySlug(ctx context.Context, slug string) (Team, error)
	EnvironmentByName(ctx context.Context, name string) (Environment, error)
	StackExists(ctx context.Context, environmentID, teamID uuid.UUID, name string) (bool, error)
	InsertStack(ctx context.Context, s NewStack) error
	InsertRequest(ctx context.Context, r *Request) error
	GetRequest(ctx context.Context, id uuid.UUID) (*Request, error)
	AppendAudit(ctx context.Context, e audit.Event) error
	// Locks returns the stack-lock store bound to the same transaction.
	Locks() locks.Store
	GetRequestByShortID(ctx context.Context, shortID string) (*Request, error)
	// LockStackTarget loads an existing stack FOR UPDATE (apperr NotFound when it does not exist).
	LockStackTarget(ctx context.Context, id uuid.UUID) (StackTarget, error)
	// OpenRequestKinds lists the kinds of the stack's requests that are not terminal yet.
	OpenRequestKinds(ctx context.Context, stackID uuid.UUID) ([]Kind, error)
	// LockRequest loads the request FOR UPDATE.
	LockRequest(ctx context.Context, id uuid.UUID) (*Request, error)
	// UpdateRequest writes r's mutable fields if the stored status is still from;
	// otherwise it returns a concurrent_update conflict.
	UpdateRequest(ctx context.Context, r *Request, from Status) error
	// Enqueue adds an outbox job in the same transaction.
	Enqueue(ctx context.Context, m outbox.Message) (bool, error)
}

// StackLocker takes the per-stack platform lock inside the request-creation transaction
// (implemented by locks.Service).
type StackLocker interface {
	AcquireIn(ctx context.Context, st locks.Store, cmd locks.AcquireCommand) (locks.Result, error)
	// ReleaseIn frees the lock held by a request reaching a terminal status and promotes
	// the next waiter, in the caller's transaction.
	ReleaseIn(ctx context.Context, st locks.Store, cmd locks.TokenCommand) (locks.ReleaseResult, error)
}

// ListFilter narrows List.
type ListFilter struct {
	Team      string
	Status    Status
	Kind      Kind
	CreatedBy *uuid.UUID
	Limit     int
	After     *Cursor
}

// Repository is the persistence port of the request service.
type Repository interface {
	// InTx runs fn with a Store bound to one transaction.
	InTx(ctx context.Context, fn func(Store) error) error
	Get(ctx context.Context, id uuid.UUID) (*Request, error)
	List(ctx context.Context, f ListFilter) ([]*Request, error)
	// StackHolder returns the stack at a canonical path with its lock and the lock holder's
	// request; apperr NotFound (stack_not_found) when no stack has that path.
	StackHolder(ctx context.Context, path string) (StackHolder, error)
	// StackTarget loads an existing stack for an update or destroy (apperr NotFound when missing).
	StackTarget(ctx context.Context, id uuid.UUID) (StackTarget, error)
	// StatusEventsAfter lists up to limit status changes of a request with Seq > after, oldest first.
	StatusEventsAfter(ctx context.Context, id uuid.UUID, after int64, limit int) ([]StatusEvent, error)
	// LatestStatusSeq is the Seq of the request's newest status change (0 when none).
	LatestStatusSeq(ctx context.Context, id uuid.UUID) (int64, error)
}

// VersionLookup resolves pinned module versions (implemented by catalog.Service).
type VersionLookup interface {
	GetVersion(ctx context.Context, name, version string) (catalog.ModuleVersion, error)
}

// CreateCommand is a validated-at-the-edge create call.
type CreateCommand struct {
	Input          CreateInput
	IdempotencyKey string
	RequestHash    string // RequestHash(method, path, body)
	CorrelationID  string
}

// CreateResult is the outcome of Create.
type CreateResult struct {
	Request  *Request
	Replayed bool // true when an earlier call with the same Idempotency-Key is returned
}

// Service implements request use cases.
type Service struct {
	repo     Repository
	versions VersionLookup
	members  Memberships
	locker   StackLocker
	clock    platform.Clock
	ids      platform.IDGenerator
	// publishing enqueues request.publish jobs for requests holding the stack lock.
	publishing bool
}

// Memberships resolves a user's role in a team (users.is_platform_admin counts as admin).
type Memberships interface {
	// RoleInTeam returns RoleNone when the user is not a member, and an apperr NotFound
	// error when the team does not exist.
	RoleInTeam(ctx context.Context, teamSlug string, userID uuid.UUID) (Role, error)
}

// NewService returns a request Service.
func NewService(repo Repository, versions VersionLookup, members Memberships, locker StackLocker,
	clock platform.Clock, ids platform.IDGenerator) *Service {
	return &Service{repo: repo, versions: versions, members: members, locker: locker, clock: clock, ids: ids}
}

// EnablePublishing makes Create (and lock promotion) enqueue a request.publish outbox job
// for every request that holds the stack lock. Without it requests stay in draft, which
// is the behaviour with IACP_PUBLISHER=none.
func (s *Service) EnablePublishing() *Service {
	s.publishing = true
	return s
}

// Create validates and records a change request. With the same Idempotency-Key and
// body it returns the original request; with a different body it fails with
// idempotency_key_reuse.
func (s *Service) Create(ctx context.Context, by identity.Principal, cmd CreateCommand) (CreateResult, error) {
	if err := ValidateIdempotencyKey(cmd.IdempotencyKey); err != nil {
		return CreateResult{}, err
	}
	if by.UserID == uuid.Nil {
		return CreateResult{}, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
			Message: "requests must be created by a user"}
	}
	// Read-only checks that use other connections (membership, catalog) run before the
	// transaction. Inside it, the request only uses its own connection; otherwise callers
	// holding a transaction and waiting for a second connection could exhaust the pool
	// while the idempotency row lock makes them wait on each other.
	err := s.precheck(ctx, by, cmd.Input)
	if err != nil {
		return CreateResult{}, err
	}
	var res CreateResult
	err = s.repo.InTx(ctx, func(st Store) error {
		claim := NewIdempotencyClaim(by.UserID, cmd.IdempotencyKey, cmd.RequestHash, s.clock.Now())
		claimed, err := st.ClaimIdempotencyKey(ctx, claim)
		if err != nil {
			return fmt.Errorf("claim idempotency key: %w", err)
		}
		if !claimed {
			res, err = s.replay(ctx, st, by, cmd)
			return err
		}
		req, err := s.create(ctx, st, by, cmd)
		if err != nil {
			return err
		}
		if err := st.CompleteIdempotencyKey(ctx, by.UserID, cmd.IdempotencyKey, req.ID); err != nil {
			return fmt.Errorf("complete idempotency key: %w", err)
		}
		if !req.IsQueued() {
			if err := s.enqueuePublish(ctx, st, req.ID, req.ShortID, by, cmd.CorrelationID); err != nil {
				return err
			}
		}
		res = CreateResult{Request: req}
		return nil
	})
	if err != nil {
		return CreateResult{}, err
	}
	return res, nil
}

func (s *Service) replay(ctx context.Context, st Store, by identity.Principal, cmd CreateCommand) (CreateResult, error) {
	rec, err := st.GetIdempotencyKey(ctx, by.UserID, cmd.IdempotencyKey)
	if err != nil {
		return CreateResult{}, fmt.Errorf("read idempotency key: %w", err)
	}
	if rec.RequestHash != cmd.RequestHash {
		return CreateResult{}, apperr.Invalid("idempotency_key_reuse",
			"Idempotency-Key was already used with a different request body")
	}
	req, err := st.GetRequest(ctx, rec.RequestID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("load replayed request: %w", err)
	}
	return CreateResult{Request: req, Replayed: true}, nil
}

// precheck validates the input shape, the caller's team role and the module versions and
// their inputs. It runs outside the transaction.
func (s *Service) precheck(ctx context.Context, by identity.Principal, in CreateInput) error {
	if errs := ValidateShape(in); len(errs) > 0 {
		return apperr.Invalid("validation_failed", "request is invalid", errs...)
	}
	if in.Kind.TargetsExistingStack() {
		return s.precheckExisting(ctx, by, in)
	}
	role, err := s.members.RoleInTeam(ctx, in.Team, by.UserID)
	if err != nil {
		return fieldLookupError(err, "/team", fmt.Sprintf("unknown team %q", in.Team))
	}
	if err := authorizeKind(role, in.Kind, in.Environment, in.Team); err != nil {
		return err
	}
	return s.checkResources(ctx, in.Resources)
}

// precheckExisting is precheck for an update or destroy: the stack must exist, the caller
// needs the role in its team, the stack must be in a state that takes the kind (checked
// again inside the transaction, with the open requests), and a destroy must repeat the
// stack's name.
func (s *Service) precheckExisting(ctx context.Context, by identity.Principal, in CreateInput) error {
	t, err := s.repo.StackTarget(ctx, *in.StackID)
	if err != nil {
		return stackLookupError(err, *in.StackID)
	}
	role, err := s.members.RoleInTeam(ctx, t.TeamSlug, by.UserID)
	if err != nil {
		return fmt.Errorf("lookup membership of team %s: %w", t.TeamSlug, err)
	}
	if role == RoleNone {
		// No role in the team: the stack is hidden, exactly as if it did not exist.
		return stackNotFound(*in.StackID)
	}
	if err := authorizeKind(role, in.Kind, t.Environment, t.TeamSlug); err != nil {
		return err
	}
	if err := CheckStackAllowsKind(in.Kind, t.Status, nil); err != nil {
		return err
	}
	if in.Kind == KindDestroy && in.ConfirmStackName != t.Name {
		return apperr.Invalid("validation_failed", "the confirmation does not match the stack name",
			apperr.FieldError{Pointer: "/confirm_stack_name", Code: "mismatch",
				Message: fmt.Sprintf("must be %q, the name of the stack to destroy", t.Name)})
	}
	if !in.Kind.RequiresResources() {
		return nil
	}
	return s.checkResources(ctx, in.Resources)
}

// authorizeKind is the one decision whether a caller with role in team may create a request
// of kind against environment env (docs/03 §6.4; see RequiredRole). Create, update and
// destroy all go through it, so swapping in another policy matrix is a change to this
// function only. Pure.
func authorizeKind(role Role, kind Kind, env, team string) error {
	if need := RequiredRole(kind, env); !role.AtLeast(need) {
		return apperr.Forbidden(fmt.Sprintf("creating a %s request for team %s needs the %s role or higher", kind, team, need))
	}
	return nil
}

func stackNotFound(id uuid.UUID) error {
	return &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found", Message: fmt.Sprintf("stack %s not found", id)}
}

// stackLookupError maps a failed stack lookup: a missing stack is the same not-found as a
// hidden one.
func stackLookupError(err error, id uuid.UUID) error {
	if apperr.IsKind(err, apperr.KindNotFound) {
		return stackNotFound(id)
	}
	return fmt.Errorf("lookup stack %s: %w", id, err)
}

// checkResources resolves the pinned module versions and validates every resource's inputs.
func (s *Service) checkResources(ctx context.Context, resources []ResourceInput) error {
	resolved, err := s.resolve(ctx, resources)
	if err != nil {
		return err
	}
	if errs := ValidateResources(resolved); len(errs) > 0 {
		return apperr.Invalid("validation_failed", fmt.Sprintf("%d resource inputs are invalid", len(errs)), errs...)
	}
	return nil
}

func (s *Service) create(ctx context.Context, st Store, by identity.Principal, cmd CreateCommand) (*Request, error) {
	if cmd.Input.Kind.TargetsExistingStack() {
		return s.createForStack(ctx, st, by, cmd)
	}
	in := cmd.Input
	team, err := st.TeamBySlug(ctx, in.Team)
	if err != nil {
		return nil, fieldLookupError(err, "/team", fmt.Sprintf("unknown team %q", in.Team))
	}
	env, err := st.EnvironmentByName(ctx, in.Environment)
	if err != nil {
		return nil, fieldLookupError(err, "/environment", fmt.Sprintf("unknown environment %q", in.Environment))
	}
	region := in.Region
	if region == "" {
		region = env.DefaultRegion
	}
	now := s.clock.Now()
	if errs := ValidateEnvironment(in, env, region); len(errs) > 0 {
		return nil, apperr.Invalid("policy_preflight_denied", "request violates environment rules", errs...)
	}

	exists, err := st.StackExists(ctx, env.ID, team.ID, in.Stack)
	if err != nil {
		return nil, fmt.Errorf("check stack: %w", err)
	}
	if exists {
		return nil, apperr.Conflict("stack_exists",
			fmt.Sprintf("stack %s/%s/%s already exists; use an update request", in.Environment, in.Team, in.Stack))
	}

	path, stateKey := StackPaths(in.Environment, in.Team, in.Stack)
	stack := NewStack{ID: s.ids.NewID(), TeamID: team.ID, EnvironmentID: env.ID, Name: in.Stack, Path: path,
		StateKey: stateKey, Region: region, CreatedBy: by.UserID, CreatedAt: now}
	if in.Ephemeral != nil {
		exp := now.Add(time.Duration(in.Ephemeral.TTLHours) * time.Hour)
		stack.IsEphemeral, stack.ExpiresAt = true, &exp
	}
	if err := st.InsertStack(ctx, stack); err != nil {
		return nil, fmt.Errorf("insert stack: %w", err)
	}

	return s.record(ctx, st, by, cmd, StackRef{ID: stack.ID, Path: path, Status: string(StackProvisioning)}, team.ID,
		Normalize(in, region))
}

// createForStack records an update or destroy of an existing stack. It locks the stack row
// first (the order every lock operation uses), re-checks the stack's state against its
// open requests, and then takes the stack lock or queues like a create.
func (s *Service) createForStack(ctx context.Context, st Store, by identity.Principal, cmd CreateCommand) (*Request, error) {
	in := cmd.Input
	t, err := st.LockStackTarget(ctx, *in.StackID)
	if err != nil {
		return nil, stackLookupError(err, *in.StackID)
	}
	open, err := st.OpenRequestKinds(ctx, t.ID)
	if err != nil {
		return nil, fmt.Errorf("list open requests: %w", err)
	}
	if err := CheckStackAllowsKind(in.Kind, t.Status, open); err != nil {
		return nil, err
	}
	return s.record(ctx, st, by, cmd, StackRef{ID: t.ID, Path: t.Path, Status: string(t.Status)}, t.TeamID, NormalizeForStack(in, t))
}

// record inserts the draft request for stack, audits request.created and takes the stack
// lock or queues behind its holder, in the caller's transaction. Create, update and
// destroy share it, so idempotency, audit and queueing behave the same for every kind.
func (s *Service) record(ctx context.Context, st Store, by identity.Principal, cmd CreateCommand, stack StackRef,
	teamID uuid.UUID, spec NormalizedSpec) (*Request, error) {
	now := s.clock.Now()
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode spec: %w", err)
	}
	req := NewDraft(s.ids.NewID(), s.ids.NewShortID("req"), stack, cmd.Input.Kind, raw, cmd.Input.Justification, by.UserID, now)
	if err := st.InsertRequest(ctx, req); err != nil {
		return nil, fmt.Errorf("insert request: %w", err)
	}

	payload, err := json.Marshal(map[string]any{"short_id": req.ShortID, "kind": req.Kind, "stack_path": stack.Path})
	if err != nil {
		return nil, fmt.Errorf("encode audit payload: %w", err)
	}
	if err := st.AppendAudit(ctx, audit.Event{
		OccurredAt: now, ActorType: string(by.Type), ActorID: by.AuditActorID(), Action: "request.created",
		SubjectType: "request", SubjectID: req.ID.String(), TeamID: &teamID, CorrelationID: cmd.CorrelationID,
		Payload: payload,
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	// docs/03 §4.1: acquire the stack lock or queue, in the same transaction.
	lock, err := s.locker.AcquireIn(ctx, st.Locks(), locks.AcquireCommand{StackID: stack.ID, RequestID: req.ID, By: by,
		CorrelationID: cmd.CorrelationID})
	if err != nil {
		return nil, fmt.Errorf("acquire stack lock: %w", err)
	}
	req.QueuedBehind = lock.QueuedBehind
	if lock.Outcome != locks.OutcomeQueued {
		token := lock.Lock.FencingToken
		req.FencingToken = &token
	}
	return req, nil
}

func (s *Service) resolve(ctx context.Context, resources []ResourceInput) ([]ResolvedResource, error) {
	out := make([]ResolvedResource, 0, len(resources))
	var errs []apperr.FieldError
	for i, r := range resources {
		v, err := s.versions.GetVersion(ctx, r.Module, r.Version)
		if err != nil {
			if apperr.IsKind(err, apperr.KindNotFound) {
				errs = append(errs, apperr.FieldError{Pointer: fmt.Sprintf("/resources/%d/version", i),
					Code: "module_version_unavailable", Message: fmt.Sprintf("%s@%s is not in the catalog", r.Module, r.Version)})
				continue
			}
			return nil, fmt.Errorf("resolve %s@%s: %w", r.Module, r.Version, err)
		}
		schema, err := inputschema.Compile(v.InputsSchema)
		if err != nil {
			return nil, fmt.Errorf("compile inputs schema of %s@%s: %w", r.Module, r.Version, err)
		}
		out = append(out, ResolvedResource{Input: r, Version: v, Schema: schema})
	}
	if len(errs) > 0 {
		return nil, apperr.Invalid("module_version_unavailable", "requested module versions are unavailable", errs...)
	}
	return out, nil
}

// ValidateEnvironment applies environment rules (region allow-list, ephemeral TTL). Pure.
func ValidateEnvironment(in CreateInput, env Environment, region string) []apperr.FieldError {
	var errs []apperr.FieldError
	if !slices.Contains(env.AllowedRegions, region) {
		errs = append(errs, apperr.FieldError{Pointer: "/region", Code: "region_not_allowed",
			Message: fmt.Sprintf("region %q is not allowed in %s (allowed: %v)", region, env.Name, env.AllowedRegions)})
	}
	if in.Ephemeral != nil {
		switch {
		case !env.AllowEphemeral:
			errs = append(errs, apperr.FieldError{Pointer: "/ephemeral", Code: "ephemeral_not_allowed",
				Message: fmt.Sprintf("ephemeral stacks are not allowed in %s", env.Name)})
		case env.MaxTTLHours != nil && in.Ephemeral.TTLHours > *env.MaxTTLHours:
			errs = append(errs, apperr.FieldError{Pointer: "/ephemeral/ttl_hours", Code: "maximum",
				Message: fmt.Sprintf("must be <= %d in %s", *env.MaxTTLHours, env.Name)})
		}
	}
	return errs
}

func fieldLookupError(err error, ptr, msg string) error {
	if apperr.IsKind(err, apperr.KindNotFound) {
		return apperr.Invalid("validation_failed", msg, apperr.FieldError{Pointer: ptr, Code: "unknown", Message: msg})
	}
	return fmt.Errorf("lookup %s: %w", ptr, err)
}

// Get returns one request.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Request, error) {
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get request %s: %w", id, err)
	}
	return r, nil
}

// MaxPageSize bounds List.
const MaxPageSize = 200

// List returns requests newest first, plus the cursor of the next page (nil at the end).
func (s *Service) List(ctx context.Context, f ListFilter) ([]*Request, *Cursor, error) {
	if f.Status != "" && !f.Status.Valid() {
		return nil, nil, &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_request", Message: fmt.Sprintf("unknown status %q", f.Status)}
	}
	if f.Kind != "" && !f.Kind.Valid() {
		return nil, nil, &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_request", Message: fmt.Sprintf("unknown kind %q", f.Kind)}
	}
	switch {
	case f.Limit <= 0:
		f.Limit = 50
	case f.Limit > MaxPageSize:
		f.Limit = MaxPageSize
	}
	want := f.Limit
	f.Limit++ // fetch one extra row to learn whether another page exists
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, nil, fmt.Errorf("list requests: %w", err)
	}
	if len(rows) <= want {
		return rows, nil, nil
	}
	rows = rows[:want]
	last := rows[len(rows)-1]
	return rows, &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
}
