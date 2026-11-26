package oauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// StateTTL is how long a login attempt may take between redirect and callback.
const StateTTL = 10 * time.Minute

// Login failures. They are matched with errors.Is and mapped to browser-visible codes by
// LoginErrorCode; details go to the log only.
var (
	ErrInvalidState      = errors.New("invalid or expired login state")
	ErrNotOrgMember      = errors.New("not an active member of the allowed organisation")
	ErrUserDisabled      = errors.New("user is disabled")
	ErrGitHubUnavailable = errors.New("github is unavailable")
)

// StateRecord is one pending login attempt.
type StateRecord struct {
	StateHash []byte
	Verifier  string
	ReturnTo  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// StateStore persists pending logins. Consume is single-use.
type StateStore interface {
	Save(ctx context.Context, r StateRecord) error
	// Consume deletes and returns the unexpired record for stateHash, or ErrInvalidState.
	Consume(ctx context.Context, stateHash []byte, now time.Time) (StateRecord, error)
	Purge(ctx context.Context, now time.Time) (int64, error)
}

// SyncResult is the outcome of syncing a login into the platform's users and memberships.
type SyncResult struct {
	UserID        uuid.UUID
	Login         string
	PlatformAdmin bool
}

// UserSyncer upserts the user and syncs team memberships (teamsync.Syncer). It returns
// ErrUserDisabled for a disabled account.
type UserSyncer interface {
	SyncLogin(ctx context.Context, u identity.GitHubUser) (SyncResult, error)
}

// SessionIssuer mints a session for a user (session.Service).
type SessionIssuer interface {
	Issue(ctx context.Context, userID uuid.UUID) (string, error)
}

// Auditor appends stand-alone audit events (audit.Recorder).
type Auditor interface {
	Record(ctx context.Context, e audit.Event) error
}

// Service runs the login flow.
type Service struct {
	gh       GitHubIdentity
	states   StateStore
	syncer   UserSyncer
	sessions SessionIssuer
	audit    Auditor
	redirect RedirectPolicy
	clock    platform.Clock
	rand     io.Reader
}

// NewService returns a Service.
func NewService(gh GitHubIdentity, states StateStore, syncer UserSyncer, sessions SessionIssuer, aud Auditor,
	redirect RedirectPolicy, clock platform.Clock) *Service {
	return &Service{gh: gh, states: states, syncer: syncer, sessions: sessions, audit: aud, redirect: redirect,
		clock: clock, rand: rand.Reader}
}

// WithEntropy replaces the entropy source (tests only).
func (s *Service) WithEntropy(r io.Reader) *Service { s.rand = r; return s }

// RedirectPolicy exposes the policy so handlers resolve error redirects the same way.
func (s *Service) RedirectPolicy() RedirectPolicy { return s.redirect }

// Begin starts a login: it stores the verifier and return target against the state and
// returns the GitHub URL plus the state, which the handler also binds to the browser with a
// cookie.
func (s *Service) Begin(ctx context.Context, returnTo string) (authURL, state string, err error) {
	if state, err = NewToken(s.rand); err != nil {
		return "", "", err
	}
	verifier, err := NewToken(s.rand)
	if err != nil {
		return "", "", err
	}
	now := s.clock.Now()
	rec := StateRecord{StateHash: HashState(state), Verifier: verifier, ReturnTo: s.redirect.Resolve(returnTo),
		CreatedAt: now, ExpiresAt: now.Add(StateTTL)}
	if err := s.states.Save(ctx, rec); err != nil {
		return "", "", fmt.Errorf("save login state: %w", err)
	}
	return s.gh.AuthorizeURL(state, Challenge(verifier)), state, nil
}

// Meta is request context recorded with audit events.
type Meta struct {
	IP            string
	CorrelationID string
}

// Result is a completed login.
type Result struct {
	SessionID string
	ReturnTo  string
	User      SyncResult
}

// Complete finishes a login. queryState and cookieState must match (login CSRF), the state
// must be pending and unexpired (single use), the GitHub user must belong to the allowed
// organisation, and only then is the user synced and a brand-new session issued.
func (s *Service) Complete(ctx context.Context, code, queryState, cookieState string, meta Meta) (Result, error) {
	if code == "" || len(code) > 512 || !StatesMatch(queryState, cookieState) {
		return Result{}, ErrInvalidState
	}
	rec, err := s.states.Consume(ctx, HashState(queryState), s.clock.Now())
	if err != nil {
		return Result{}, err
	}
	gu, err := s.gh.Authenticate(ctx, code, rec.Verifier)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrGitHubUnavailable, err)
	}
	if !gu.OrgMember {
		s.record(ctx, meta, audit.Event{ActorType: "user", ActorID: gu.Login, Action: "auth.login_denied",
			SubjectType: "user", SubjectID: gu.Login, Payload: payload(map[string]any{"reason": "not_org_member"})})
		return Result{}, ErrNotOrgMember
	}
	user, err := s.syncer.SyncLogin(ctx, gu)
	if err != nil {
		if errors.Is(err, ErrUserDisabled) {
			s.record(ctx, meta, audit.Event{ActorType: "user", ActorID: gu.Login, Action: "auth.login_denied",
				SubjectType: "user", SubjectID: gu.Login, Payload: payload(map[string]any{"reason": "user_disabled"})})
		}
		return Result{}, err
	}
	id, err := s.sessions.Issue(ctx, user.UserID)
	if err != nil {
		return Result{}, err
	}
	if err := s.record(ctx, meta, audit.Event{ActorType: "user", ActorID: user.UserID.String(), Action: "auth.login",
		SubjectType: "user", SubjectID: user.UserID.String(),
		Payload: payload(map[string]any{"login": user.Login, "platform_admin": user.PlatformAdmin})}); err != nil {
		return Result{}, fmt.Errorf("audit login: %w", err)
	}
	return Result{SessionID: id, ReturnTo: rec.ReturnTo, User: user}, nil
}

// record appends an audit event; failures of denial events are logged by the caller's
// error path rather than masking the denial.
func (s *Service) record(ctx context.Context, m Meta, e audit.Event) error {
	e.ActorIP, e.CorrelationID = m.IP, m.CorrelationID
	return s.audit.Record(ctx, e)
}

func payload(v map[string]any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
