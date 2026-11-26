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

// authorizeView loads the request and checks that by is a user with a role in its team.
// A caller who may not see it gets the same not-found as a request that does not exist.
func (s *Service) authorizeView(ctx context.Context, by identity.Principal, id uuid.UUID) (*Request, error) {
	notFound := apperr.NotFound("request %s not found", id)
	if by.UserID == uuid.Nil {
		return nil, notFound
	}
	req, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get request %s: %w", id, err)
	}
	team, ok := TeamFromStackPath(req.Stack.Path)
	if !ok {
		return nil, fmt.Errorf("request %s has a stack path without a team: %q", id, req.Stack.Path)
	}
	role, err := s.members.RoleInTeam(ctx, team, by.UserID)
	if apperr.IsKind(err, apperr.KindNotFound) || (err == nil && !CanView(role)) {
		return nil, notFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup membership of team %s: %w", team, err)
	}
	return req, nil
}

// StatusSnapshot authorizes by for the request and returns its current state with the
// feed position to resume from. The position is read before the request, so a change that
// commits in between is replayed by the feed rather than lost.
func (s *Service) StatusSnapshot(ctx context.Context, by identity.Principal, id uuid.UUID) (StatusSnapshot, error) {
	if _, err := s.authorizeView(ctx, by, id); err != nil {
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
	if _, err := s.authorizeView(ctx, by, id); err != nil {
		return nil, err
	}
	events, err := s.repo.StatusEventsAfter(ctx, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("status events of %s: %w", id, err)
	}
	return events, nil
}
