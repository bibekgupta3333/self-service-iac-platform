package ttl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Actor is the identity of automatic TTL actions in the audit trail and on requests.
const Actor = "system:ttl-reaper"

// MaxDue bounds one answer of Due; the reaper runs hourly, so a backlog drains over runs.
const MaxDue = 500

// Store is the transactional persistence port: every method runs in one transaction.
type Store interface {
	// LockStack loads the stack FOR UPDATE (apperr NotFound when missing).
	LockStack(ctx context.Context, id uuid.UUID) (Stack, error)
	// SetExpiry stores an extension and clears the warning, so the next window warns again.
	SetExpiry(ctx context.Context, id uuid.UUID, e Extension) error
	// MarkWarned records that the warning for the current expiry was sent.
	MarkWarned(ctx context.Context, id uuid.UUID, at time.Time) error
	// HasOpenDestroy reports a destroy request that is not terminal yet.
	HasOpenDestroy(ctx context.Context, id uuid.UUID) (bool, error)
	// Attempts lists the stack's unsuccessful TTL destroys, oldest first (see AttemptsSQL).
	Attempts(ctx context.Context, id uuid.UUID) ([]Attempt, error)
	// BlockedSince reports whether ttl.blocked was already recorded for the stack at or after since.
	BlockedSince(ctx context.Context, id uuid.UUID, since time.Time) (bool, error)
	AppendAudit(ctx context.Context, e audit.Event) error
	Enqueue(ctx context.Context, m outbox.Message) (bool, error)
}

// Cursor is the keyset position of a Candidates page.
type Cursor struct {
	ExpiresAt time.Time
	ID        uuid.UUID
}

// Repository is the persistence port of the TTL service.
type Repository interface {
	InTx(ctx context.Context, fn func(Store) error) error
	// Stack loads one stack without locking (apperr NotFound when missing).
	Stack(ctx context.Context, id uuid.UUID) (Stack, error)
	// Candidates lists live ephemeral stacks outside production that expire at or before
	// horizon and have no open destroy request, oldest expiry first, at most limit, starting
	// after the cursor (nil for the first page).
	Candidates(ctx context.Context, horizon time.Time, after *Cursor, limit int) ([]Stack, error)
	// SystemUserID is the platform bot's user id, owner of the requests the reaper opens.
	SystemUserID(ctx context.Context) (uuid.UUID, error)
	// LatestPullRequest is the number of the newest pull request opened for the stack.
	LatestPullRequest(ctx context.Context, id uuid.UUID) (number int, ok bool, err error)
}

// Memberships resolves a user's role in a team (requests.PostgresRepository).
type Memberships interface {
	RoleInTeam(ctx context.Context, teamSlug string, userID uuid.UUID) (rbac.Role, error)
}

// DestroyCommand asks the request service for a destroy on behalf of the platform.
type DestroyCommand struct {
	StackID       uuid.UUID
	By            identity.Principal
	CorrelationID string
}

// DestroyResult is what the request service did.
type DestroyResult struct {
	// Created is false when a destroy request was already open (nothing new was done).
	Created   bool
	RequestID uuid.UUID
	ShortID   string
	Queued    bool // the request waits behind another holder of the stack lock
	// Blocked is true when the stack used up its TTL destroy attempts: nothing was opened
	// and ttl.blocked was recorded (once per expiry).
	Blocked bool
}

// Destroyer opens the destroy request of an expired stack (requests.Service).
type Destroyer interface {
	OpenTTLDestroy(ctx context.Context, cmd DestroyCommand) (DestroyResult, error)
}

// Service implements the TTL use cases: extend, list what is due, warn, tear down.
type Service struct {
	repo      Repository
	members   Memberships
	destroyer Destroyer
	clock     platform.Clock
	policy    Policy
	// commentURL is set when warnings are also posted on the stack's PR (EnableComments).
	comments   bool
	commentURL string
}

// NewService returns a Service bounded by policy.
func NewService(repo Repository, members Memberships, destroyer Destroyer, clock platform.Clock, policy Policy) *Service {
	return &Service{repo: repo, members: members, destroyer: destroyer, clock: clock, policy: policy}
}

// EnableComments makes Warn enqueue a ttl.warning job that comments on the stack's pull
// request; stackURLBase (the portal's /stacks URL) is used for the extend link. Without it
// a warning is only an audit event.
func (s *Service) EnableComments(stackURLBase string) *Service {
	s.comments, s.commentURL = true, stackURLBase
	return s
}

// Policy returns the TTL bounds in force.
func (s *Service) Policy() Policy { return s.policy }

func stackNotFound(id uuid.UUID) error {
	return &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found", Message: fmt.Sprintf("stack %s not found", id)}
}

func lookupError(err error, id uuid.UUID) error {
	if apperr.IsKind(err, apperr.KindNotFound) {
		return stackNotFound(id)
	}
	return fmt.Errorf("lookup stack %s: %w", id, err)
}

func event(at time.Time, actorType, actorID, action string, st Stack, correlationID string, payload map[string]any) (audit.Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return audit.Event{}, fmt.Errorf("encode audit payload: %w", err)
	}
	team := st.TeamID
	return audit.Event{OccurredAt: at, ActorType: actorType, ActorID: actorID, Action: action, SubjectType: "stack",
		SubjectID: st.ID.String(), TeamID: &team, CorrelationID: correlationID, Payload: body}, nil
}

// ExtendResult is the outcome of Extend.
type ExtendResult struct {
	ExpiresAt         time.Time
	ExtendedHours     int
	RemainingExtHours int // hours of extension still available for this stack
}

// Extend adds hours to the TTL of an ephemeral stack. The caller needs the stack.extend_ttl
// role in the stack's team for its environment (a stack of another team is not found), the
// request must fit the policy (ttl.Policy.Extend), and a stack with a destroy request open
// can no longer be saved. The change and its ttl.extended audit event commit together.
func (s *Service) Extend(ctx context.Context, by identity.Principal, id uuid.UUID, hours int, correlationID string) (ExtendResult, error) {
	if by.UserID == uuid.Nil {
		return ExtendResult{}, &apperr.Error{Kind: apperr.KindUnauthenticated, Code: "unauthenticated",
			Message: "a TTL is extended by a user"}
	}
	// Read-only checks on other connections run before the transaction (see requests.Service).
	pre, err := s.repo.Stack(ctx, id)
	if err != nil {
		return ExtendResult{}, lookupError(err, id)
	}
	role, err := s.members.RoleInTeam(ctx, pre.Team, by.UserID)
	if err != nil {
		return ExtendResult{}, fmt.Errorf("lookup membership of team %s: %w", pre.Team, err)
	}
	if role == rbac.RoleNone {
		return ExtendResult{}, stackNotFound(id) // hidden, exactly as if it did not exist
	}
	if d := rbac.Decide(role, rbac.ActionTTLExtend, rbac.Resource{Environment: pre.Environment}); !d.Allowed {
		return ExtendResult{}, apperr.Forbidden(fmt.Sprintf("extending a TTL in team %s needs the %s role or higher", pre.Team, d.Need))
	}
	var res ExtendResult
	err = s.repo.InTx(ctx, func(st Store) error {
		cur, err := st.LockStack(ctx, id)
		if err != nil {
			return lookupError(err, id)
		}
		now := s.clock.Now()
		ext, err := s.policy.Extend(cur, hours, now)
		if err != nil {
			return err
		}
		if open, err := st.HasOpenDestroy(ctx, id); err != nil {
			return fmt.Errorf("check open destroy: %w", err)
		} else if open {
			return apperr.Conflict("destroy_pending", "a destroy request is open for the stack; its TTL can no longer be extended")
		}
		if err := st.SetExpiry(ctx, id, ext); err != nil {
			return fmt.Errorf("store extension: %w", err)
		}
		e, err := event(now, string(by.Type), by.AuditActorID(), "ttl.extended", cur, correlationID, map[string]any{
			"hours": hours, "previous_expires_at": cur.ExpiresAt.UTC(), "expires_at": ext.ExpiresAt.UTC(),
			"extended_hours_total": ext.ExtendedHours, "stack_path": cur.Path})
		if err != nil {
			return err
		}
		if err := st.AppendAudit(ctx, e); err != nil {
			return fmt.Errorf("audit ttl.extended: %w", err)
		}
		res = ExtendResult{ExpiresAt: ext.ExpiresAt, ExtendedHours: ext.ExtendedHours,
			RemainingExtHours: max(0, s.policy.MaxExtensionHours-ext.ExtendedHours)}
		return nil
	})
	if err != nil {
		return ExtendResult{}, err
	}
	return res, nil
}

// DueItem is a stack the reaper has to act on or report.
type DueItem struct {
	Stack Stack
	Phase Phase
}

// Paging of the due list: candidates are read in pages and classified page by page, so the
// stacks the backoff leaves out (in backoff, blocked) never use up the limit that bounds
// the actionable ones. MaxScan bounds the whole read.
const (
	duePage = 200
	MaxScan = 5000
)

// Due lists the stacks due for a teardown or a warning at the current time (teardowns
// first, at most MaxDue actionable stacks) and the blocked ones (at most MaxDue), which are
// reported and never retried. It also records ttl.blocked, once per expiry, for every
// blocked stack, so the audit trail names the stacks that need a person. Warn and Teardown
// re-check everything under lock.
func (s *Service) Due(ctx context.Context, correlationID string) ([]DueItem, error) {
	now := s.clock.Now()
	var actionable, blocked []Due
	var after *Cursor
	for scanned := 0; scanned < MaxScan && len(actionable) < MaxDue; {
		rows, err := s.repo.Candidates(ctx, WarnHorizon(now), after, duePage)
		if err != nil {
			return nil, fmt.Errorf("list ttl candidates: %w", err)
		}
		for _, d := range s.policy.Classify(rows, now) {
			if d.Phase == PhaseBlocked {
				blocked = append(blocked, d)
			} else {
				actionable = append(actionable, d)
			}
		}
		scanned += len(rows)
		if len(rows) < duePage {
			break
		}
		last := rows[len(rows)-1]
		after = &Cursor{ExpiresAt: *last.ExpiresAt, ID: last.ID}
	}
	sortDue(actionable)
	// Teardowns before warnings, as Classify orders a single page.
	actionable = teardownsFirst(actionable)
	if len(actionable) > MaxDue {
		actionable = actionable[:MaxDue]
	}
	if len(blocked) > MaxDue {
		blocked = blocked[:MaxDue]
	}
	out := make([]DueItem, 0, len(actionable)+len(blocked))
	for _, d := range append(actionable, blocked...) {
		out = append(out, DueItem{Stack: d.Stack, Phase: d.Phase})
	}
	for _, d := range blocked {
		// A failing audit append for one stack must not stop the warnings and teardowns of the
		// others: it is logged and retried by the next run.
		if err := s.recordBlocked(ctx, d.Stack.ID, correlationID); err != nil {
			observability.Logger(ctx).ErrorContext(ctx, "record ttl.blocked failed", "stack_id", d.Stack.ID, "error", err)
		}
	}
	return out, nil
}

// teardownsFirst stably moves teardowns in front of warnings. Pure.
func teardownsFirst(d []Due) []Due {
	var down, other []Due
	for _, x := range d {
		if x.Phase == PhaseTeardown {
			down = append(down, x)
		} else {
			other = append(other, x)
		}
	}
	return append(down, other...)
}

// recordBlocked appends ttl.blocked for a stack unless this expiry already has one. It locks
// the stack row and decides on the reloaded stack, so two concurrent callers cannot both
// append, and a stack that was extended or whose destroy changed since the candidate read is
// not recorded for an expiry that no longer applies.
func (s *Service) recordBlocked(ctx context.Context, id uuid.UUID, correlationID string) error {
	return s.repo.InTx(ctx, func(tx Store) error {
		cur, err := tx.LockStack(ctx, id)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if PhaseAt(cur, now) != PhaseTeardown {
			return nil
		}
		if cur.Attempts, err = tx.Attempts(ctx, id); err != nil {
			return err
		}
		if state, _ := s.policy.Backoff(cur, now); state != BackoffBlocked {
			return nil
		}
		done, err := tx.BlockedSince(ctx, id, *cur.ExpiresAt)
		if err != nil || done {
			return err
		}
		e, err := event(now, audit.ActorSystem, Actor, "ttl.blocked", cur, correlationID, map[string]any{
			"stack_path": cur.Path, "expires_at": cur.ExpiresAt.UTC(), "attempts": len(cur.Attempts)})
		if err != nil {
			return err
		}
		if err := tx.AppendAudit(ctx, e); err != nil {
			return fmt.Errorf("audit ttl.blocked: %w", err)
		}
		return nil
	})
}

// WarnResult is the outcome of Warn.
type WarnResult struct {
	Warned bool
	// Reason says why nothing was done when Warned is false.
	Reason string
}

// WarnPayload is the body of a ttl.warning outbox job.
type WarnPayload struct {
	StackID uuid.UUID `json:"stack_id"`
}

// Warn records the 24 h warning of a stack: it stamps ttl_warned_at (so the warning is sent
// once per expiry), appends ttl.warned to the audit trail and, when comments are enabled,
// enqueues the pull request comment. A stack that is not in its warning window any more
// (extended, already warned, expired, gone) is skipped, which makes the call idempotent.
func (s *Service) Warn(ctx context.Context, id uuid.UUID, correlationID string) (WarnResult, error) {
	var res WarnResult
	err := s.repo.InTx(ctx, func(st Store) error {
		cur, err := st.LockStack(ctx, id)
		if err != nil {
			return lookupError(err, id)
		}
		now := s.clock.Now()
		if phase := PhaseAt(cur, now); phase != PhaseWarn {
			res = WarnResult{Reason: "not_in_warning_window: " + string(phase)}
			return nil
		}
		if err := st.MarkWarned(ctx, id, now); err != nil {
			return fmt.Errorf("mark warned: %w", err)
		}
		e, err := event(now, audit.ActorSystem, Actor, "ttl.warned", cur, correlationID, map[string]any{
			"expires_at": cur.ExpiresAt.UTC(), "hours_left": HoursLeft(*cur.ExpiresAt, now), "stack_path": cur.Path})
		if err != nil {
			return err
		}
		if err := st.AppendAudit(ctx, e); err != nil {
			return fmt.Errorf("audit ttl.warned: %w", err)
		}
		if s.comments {
			payload, err := json.Marshal(WarnPayload{StackID: id})
			if err != nil {
				return fmt.Errorf("encode warning job: %w", err)
			}
			if _, err := st.Enqueue(ctx, outbox.Message{Topic: outbox.TopicTTLWarning, DedupeKey: "ttl-warn:" + id.String(),
				Payload: payload, AvailableAt: now}); err != nil {
				return fmt.Errorf("enqueue warning: %w", err)
			}
		}
		res = WarnResult{Warned: true}
		return nil
	})
	if err != nil {
		return WarnResult{}, err
	}
	return res, nil
}

// Teardown opens the destroy request of an expired stack as the platform's system actor
// (justification "ttl expired"). The destroyer refuses production, permanent and unexpired
// stacks under the stack lock, so this is safe to call with a stale due list.
func (s *Service) Teardown(ctx context.Context, id uuid.UUID, correlationID string) (DestroyResult, error) {
	bot, err := s.repo.SystemUserID(ctx)
	if err != nil {
		return DestroyResult{}, fmt.Errorf("resolve the system user: %w", err)
	}
	by := identity.Principal{UserID: bot, Login: Actor, Type: identity.ActorSystem}
	res, err := s.destroyer.OpenTTLDestroy(ctx, DestroyCommand{StackID: id, By: by, CorrelationID: correlationID})
	if err != nil {
		return DestroyResult{}, err
	}
	if res.Blocked {
		return DestroyResult{}, apperr.Conflict("ttl_blocked", fmt.Sprintf(
			"stack %s used up its %d TTL destroy attempts; it needs a person (extend it, or fix the failure and destroy it by hand)",
			id, s.policy.MaxAttempts))
	}
	return res, nil
}

// PullRequestCommenter posts a comment on a pull request (publish.Commenter).
type PullRequestCommenter interface {
	CommentOnPullRequest(ctx context.Context, number int, body string) error
}

// WarningNotifier is the outbox handler of ttl.warning jobs: it comments the warning on
// the pull request of the stack's newest request. A stack without a pull request, or one
// that was extended since the warning was recorded, is skipped. The warning itself is
// already in the audit trail, so a failure here only costs the comment.
type WarningNotifier struct {
	repo      Repository
	commenter PullRequestCommenter
	clock     platform.Clock
	stackURL  string // base URL of the portal's stack pages, "" for none
}

var _ outbox.Handler = (*WarningNotifier)(nil)

// NewWarningNotifier returns a WarningNotifier.
func NewWarningNotifier(repo Repository, c PullRequestCommenter, clock platform.Clock, stackURLBase string) *WarningNotifier {
	return &WarningNotifier{repo: repo, commenter: c, clock: clock, stackURL: stackURLBase}
}

// Handle implements outbox.Handler.
func (n *WarningNotifier) Handle(ctx context.Context, j outbox.Job) error {
	var p WarnPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.StackID == uuid.Nil {
		return fmt.Errorf("decode ttl warning job %d: %w", j.ID, outbox.ErrPermanent)
	}
	st, err := n.repo.Stack(ctx, p.StackID)
	if err != nil {
		if apperr.IsKind(err, apperr.KindNotFound) {
			return fmt.Errorf("ttl warning for %s: %w: %w", p.StackID, err, outbox.ErrPermanent)
		}
		return fmt.Errorf("load stack %s: %w", p.StackID, err)
	}
	log := observability.Logger(ctx)
	if Eligible(st) != nil || st.WarnedAt == nil {
		log.InfoContext(ctx, "ttl warning comment skipped", "stack_id", p.StackID, "reason", "no longer warned or eligible")
		return nil
	}
	number, ok, err := n.repo.LatestPullRequest(ctx, p.StackID)
	switch {
	case err != nil:
		return fmt.Errorf("find the pull request of stack %s: %w", p.StackID, err)
	case !ok:
		log.InfoContext(ctx, "ttl warning comment skipped", "stack_id", p.StackID, "reason", "stack has no pull request")
		return nil
	}
	url := ""
	if n.stackURL != "" {
		url = n.stackURL + "/" + p.StackID.String()
	}
	if err := n.commenter.CommentOnPullRequest(ctx, number, WarningComment(st, n.clock.Now(), url)); err != nil {
		return fmt.Errorf("comment ttl warning on #%d: %w", number, err)
	}
	log.InfoContext(ctx, "ttl warning commented", slog.String("stack", st.Path), slog.Int("pr", number))
	return nil
}

// ErrNoSystemUser is returned by repositories when the platform bot user is missing.
var ErrNoSystemUser = errors.New("system user iacp-bot not found")
