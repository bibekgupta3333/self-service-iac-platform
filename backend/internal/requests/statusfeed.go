package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// Audit actions that carry a status change of a request.
const (
	ActionCreated     = "request.created"
	ActionTransitions = "request.transitioned"
)

// StatusActions are the audit actions the status feed is built from.
var StatusActions = []string{ActionCreated, ActionTransitions}

// StatusEvent is one status change of a request, read from the append-only audit log.
// Seq is the audit sequence number: strictly increasing, gap-free per request in commit
// order, so a client can resume after the last Seq it saw.
type StatusEvent struct {
	Seq    int64
	At     time.Time
	Status Status // the status the request entered
	From   Status // empty for the creation event
	Event  Event  // the state-machine event that caused it; empty for the creation event
}

// ParseStatusEvent turns an audit row into a StatusEvent. ok is false for actions that do
// not change the status and for payloads that lack the status fields. Pure.
func ParseStatusEvent(seq int64, at time.Time, action string, payload []byte) (StatusEvent, bool) {
	switch action {
	case ActionCreated:
		return StatusEvent{Seq: seq, At: at, Status: StatusDraft}, true
	case ActionTransitions:
		var p struct {
			Event Event  `json:"event"`
			From  Status `json:"from"`
			To    Status `json:"to"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || !p.To.Valid() {
			return StatusEvent{}, false
		}
		return StatusEvent{Seq: seq, At: at, Status: p.To, From: p.From, Event: p.Event}, true
	}
	return StatusEvent{}, false
}

// StatusSnapshot is a request's current state together with the feed position it is
// consistent with: every status event with Seq <= Seq is already reflected in Request.
type StatusSnapshot struct {
	Request *Request
	Seq     int64
}

// CanView reports whether a caller with role in the request's team may watch the request.
// Any team role may; platform admins hold the admin role. Pure.
func CanView(role Role) bool { return role.AtLeast(RoleViewer) }

// RequestViewer decides which requests a caller may see, from identity.Visibility
// (requests.Access): the same helper behind GET /v1/requests and GET /v1/stacks, so one
// caller sees the same set on every route, dev mode's platform admin included.
type RequestViewer interface {
	Visible(ctx context.Context, p identity.Principal) (identity.Visibility, error)
	CanView(ctx context.Context, v identity.Visibility, id uuid.UUID) (bool, error)
}

// WithAccess makes the status feed authorize through the shared visibility helper. Without
// it the feed falls back to the caller's role in the request's team (RoleInTeam), which
// ignores the principal's platform-admin flag; the server always sets it.
func (s *Service) WithAccess(a RequestViewer) *Service {
	s.access = a
	return s
}

// authorizeView checks that by may see the request. A caller who may not gets the same
// not-found as a request that does not exist.
func (s *Service) authorizeView(ctx context.Context, by identity.Principal, id uuid.UUID) error {
	notFound := apperr.NotFound("request %s not found", id)
	if by.UserID == uuid.Nil {
		return notFound
	}
	if s.access != nil {
		v, err := s.access.Visible(ctx, by)
		if err != nil {
			return fmt.Errorf("resolve visible teams: %w", err)
		}
		ok, err := s.access.CanView(ctx, v, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound
		}
		return nil
	}
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get request %s: %w", id, err)
	}
	team, ok := TeamFromStackPath(req.Stack.Path)
	if !ok {
		return fmt.Errorf("request %s has a stack path without a team: %q", id, req.Stack.Path)
	}
	role, err := s.roleIn(ctx, by, team)
	if apperr.IsKind(err, apperr.KindNotFound) || (err == nil && !CanView(role)) {
		return notFound
	}
	if err != nil {
		return fmt.Errorf("lookup membership of team %s: %w", team, err)
	}
	return nil
}

// StatusSnapshot authorizes by for the request and returns its current state with the
// feed position to resume from. The position is read before the request, so a change that
// commits in between is replayed by the feed rather than lost.
func (s *Service) StatusSnapshot(ctx context.Context, by identity.Principal, id uuid.UUID) (StatusSnapshot, error) {
	if err := s.authorizeView(ctx, by, id); err != nil {
		return StatusSnapshot{}, err
	}
	seq, err := s.repo.LatestStatusSeq(ctx, id)
	if err != nil {
		return StatusSnapshot{}, fmt.Errorf("latest status event of %s: %w", id, err)
	}
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return StatusSnapshot{}, fmt.Errorf("get request %s: %w", id, err)
	}
	return StatusSnapshot{Request: req, Seq: seq}, nil
}

// StatusEventsAfter authorizes by again (membership can change while a stream is open)
// and returns up to limit status events with Seq > after, oldest first.
func (s *Service) StatusEventsAfter(ctx context.Context, by identity.Principal, id uuid.UUID, after int64, limit int) ([]StatusEvent, error) {
	if err := s.authorizeView(ctx, by, id); err != nil {
		return nil, err
	}
	events, err := s.repo.StatusEventsAfter(ctx, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("status events of %s: %w", id, err)
	}
	return events, nil
}
