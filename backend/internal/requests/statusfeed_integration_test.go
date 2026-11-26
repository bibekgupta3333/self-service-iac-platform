package requests

import (
	"context"
	"strings"
	"testing"
)

// TestStatusFeedReadsTheAuditLog drives a request through real transitions and reads them
// back through the feed, resuming from a position.
func TestStatusFeedReadsTheAuditLog(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	res, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "feed-key-000001"))
	if err != nil {
		t.Fatal(err)
	}
	req := res.Request
	target, err := f.svc.PublishTarget(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RecordPublished(ctx, req.ID, PublishedPR{Publisher: "local", Branch: "iacp/" + req.ShortID,
		CommitSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("e", 40), Number: 900, URL: "file:///x#pull/900",
		FencingToken: *target.FencingToken}, "c1"); err != nil {
		t.Fatal(err)
	}

	snap, err := f.svc.StatusSnapshot(ctx, f.user, req.ID)
	if err != nil || snap.Request.Status != StatusPROpen || snap.Seq == 0 {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	events, err := f.svc.StatusEventsAfter(ctx, f.user, req.ID, 0, 100)
	if err != nil || len(events) != 2 || events[0].Status != StatusDraft || events[1].Status != StatusPROpen ||
		events[1].From != StatusDraft || events[1].Event != EventPROpened || events[1].Seq != snap.Seq {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if err := func() error {
		_, err := f.svc.ApplyGitHubEvent(ctx, GitHubEvent{RequestShortID: req.ShortID, PRNumber: 900, Event: EventPRClosed,
			HeadSHA: strings.Repeat("c", 40), DeliveryID: "d-feed"})
		return err
	}(); err != nil {
		t.Fatal(err)
	}
	later, err := f.svc.StatusEventsAfter(ctx, f.user, req.ID, snap.Seq, 100)
	if err != nil || len(later) != 1 || later[0].Status != StatusRejected || !later[0].Status.IsTerminal() {
		t.Fatalf("after the snapshot = %+v, %v", later, err)
	}
	if none, err := f.svc.StatusEventsAfter(ctx, f.user, req.ID, later[0].Seq, 100); err != nil || len(none) != 0 {
		t.Fatalf("nothing after the last event: %+v, %v", none, err)
	}
	// A user with no role in the team sees nothing.
	var outsider = f.user
	if err := f.pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (99, 'outsider') RETURNING id`).Scan(&outsider.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.StatusSnapshot(ctx, outsider, req.ID); err == nil {
		t.Fatal("an outsider must not read the feed")
	}
}
