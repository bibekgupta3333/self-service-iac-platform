package requests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// The in-memory audit log doubles as the status feed: seq is the 1-based position.
func (m *memRepo) StatusEventsAfter(_ context.Context, id uuid.UUID, after int64, limit int) ([]StatusEvent, error) {
	var out []StatusEvent
	for i, e := range m.state.events {
		seq := int64(i + 1)
		if e.SubjectID != id.String() || seq <= after || len(out) >= limit {
			continue
		}
		if ev, ok := ParseStatusEvent(seq, e.OccurredAt, e.Action, e.Payload); ok {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *memRepo) LatestStatusSeq(ctx context.Context, id uuid.UUID) (int64, error) {
	all, _ := m.StatusEventsAfter(ctx, id, 0, 1<<20)
	if len(all) == 0 {
		return 0, nil
	}
	return all[len(all)-1].Seq, nil
}

func TestParseStatusEvent(t *testing.T) {
	at := t0
	tests := []struct {
		name    string
		action  string
		payload string
		want    StatusEvent
		ok      bool
	}{
		{"created", ActionCreated, `{"short_id":"req-1","kind":"create"}`, StatusEvent{Seq: 7, At: at, Status: StatusDraft}, true},
		{"transition", ActionTransitions, `{"event":"pr_opened","from":"draft","to":"pr_open","short_id":"req-1"}`,
			StatusEvent{Seq: 7, At: at, Status: StatusPROpen, From: StatusDraft, Event: EventPROpened}, true},
		{"terminal", ActionTransitions, `{"event":"apply_succeeded","from":"applying","to":"applied"}`,
			StatusEvent{Seq: 7, At: at, Status: StatusApplied, From: StatusApplying, Event: EventApplySucceeded}, true},
		{"unknown target status", ActionTransitions, `{"event":"x","from":"draft","to":"nope"}`, StatusEvent{}, false},
		{"not json", ActionTransitions, `nope`, StatusEvent{}, false},
		{"another action", "request.publish_failed", `{"to":"failed"}`, StatusEvent{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseStatusEvent(7, at, tt.action, []byte(tt.payload))
			if ok != tt.ok || got != tt.want {
				t.Fatalf("got %+v, %v", got, ok)
			}
		})
	}
}

func TestCanView(t *testing.T) {
	for role, want := range map[Role]bool{RoleNone: false, RoleViewer: true, RoleDeveloper: true, RoleMaintainer: true,
		RoleAdmin: true, Role("bogus"): false} {
		if CanView(role) != want {
			t.Errorf("CanView(%q) = %v", role, !want)
		}
	}
}

func transitionEvent(id uuid.UUID, at time.Time, event Event, from, to Status) audit.Event {
	p, _ := json.Marshal(map[string]any{"short_id": "req-x", "event": event, "from": from, "to": to})
	return audit.Event{OccurredAt: at, Action: ActionTransitions, SubjectType: "request", SubjectID: id.String(), Payload: p}
}

func TestStatusFeed(t *testing.T) {
	repo := newMemRepo()
	svc := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleViewer, otherUser: RoleDeveloper})
	res, err := newSvc(repo, fakeVersions{}).Create(context.Background(), dev, createCmd(validInput(), "key-feed-00001"))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Request.ID
	repo.state.events = append(repo.state.events,
		transitionEvent(id, t0.Add(time.Minute), EventPROpened, StatusDraft, StatusPROpen),
		audit.Event{OccurredAt: t0, Action: "request.publish_failed", SubjectType: "request", SubjectID: id.String(), Payload: []byte(`{}`)},
		transitionEvent(uuid.New(), t0, EventPROpened, StatusDraft, StatusPROpen), // another request
		transitionEvent(id, t0.Add(2*time.Minute), EventPRClosed, StatusPROpen, StatusRejected))
	ctx := context.Background()

	// A viewer of the team may watch: the snapshot is consistent with its position.
	snap, err := svc.StatusSnapshot(ctx, dev, id)
	if err != nil || snap.Request.ID != id || snap.Seq != 5 {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	all, err := svc.StatusEventsAfter(ctx, dev, id, 0, 10)
	if err != nil || len(all) != 3 || all[0].Status != StatusDraft || all[1].Status != StatusPROpen || all[2].Status != StatusRejected {
		t.Fatalf("events = %+v, %v", all, err)
	}
	if rest, _ := svc.StatusEventsAfter(ctx, dev, id, all[1].Seq, 10); len(rest) != 1 || rest[0].Event != EventPRClosed {
		t.Fatalf("resume after %d = %+v", all[1].Seq, rest)
	}
	if first, _ := svc.StatusEventsAfter(ctx, dev, id, 0, 1); len(first) != 1 {
		t.Fatalf("limit not applied: %+v", first)
	}

	// Everyone else gets the not-found of a request that does not exist.
	for name, by := range map[string]identity.Principal{
		"no role":        {UserID: uuid.New(), Type: identity.ActorUser},
		"machine caller": {Login: "apply.yml", Type: identity.ActorGitHubActions},
	} {
		if _, err := svc.StatusSnapshot(ctx, by, id); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Errorf("%s: snapshot err = %v", name, err)
		}
		if _, err := svc.StatusEventsAfter(ctx, by, id, 0, 10); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Errorf("%s: events err = %v", name, err)
		}
	}
	if _, err := svc.StatusSnapshot(ctx, dev, uuid.New()); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("unknown request: %v", err)
	}
	// Losing the role ends access on the next poll.
	revoked := newSvcWithMembers(repo, fakeVersions{}, fakeMembers{})
	if _, err := revoked.StatusEventsAfter(ctx, dev, id, 0, 10); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("revoked member: %v", err)
	}
}
