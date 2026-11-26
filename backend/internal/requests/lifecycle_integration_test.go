package requests

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

type lifecycleFixture struct {
	pool  *pgxpool.Pool
	svc   *Service
	repo  *PostgresRepository
	locks *locks.Service
	user  identity.Principal
}

// newLifecycleFixture seeds a developer in team payments and the sqs/lambda catalog, and
// returns a publishing-enabled request service on a fresh database.
func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		WITH tm AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		            VALUES ('payments', 'Payments', 7, 'payments', 'cc-4102') RETURNING id)
		INSERT INTO memberships (team_id, user_id, role) SELECT id, $1, 'developer' FROM tm`, userID); err != nil {
		t.Fatal(err)
	}
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	cat := catalog.NewService(catalog.NewPostgresRepository(pool, writer), platform.SystemClock{}, "https://example.test/iacp.git")
	bot := identity.Principal{Login: "module-release.yml", Type: identity.ActorGitHubActions}
	for _, cmd := range []catalog.IndexCommand{
		{Module: "sqs", Version: "1.3.0", GitTag: "modules/sqs/v1.3.0", CommitSHA: strings.Repeat("9c", 20),
			DisplayName: "SQS", Description: "queue", Category: "messaging", Owners: []string{"platform"},
			InputsSchema:   []byte(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
			Outputs:        []catalog.Output{{Name: "queue_arn"}, {Name: "queue_url"}},
			TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot"},
		{Module: "lambda", Version: "2.0.1", GitTag: "modules/lambda/v2.0.1", CommitSHA: strings.Repeat("8b", 20),
			DisplayName: "Lambda", Description: "function", Category: "compute", Owners: []string{"platform"},
			InputsSchema:   []byte(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"queue_arn":{"type":"string"}}}`),
			Outputs:        []catalog.Output{{Name: "function_arn"}},
			TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot"},
	} {
		if _, _, err := cat.Index(ctx, bot, cmd); err != nil {
			t.Fatalf("index: %v", err)
		}
	}
	repo := NewPostgresRepository(pool, writer)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, 0)
	svc := NewService(repo, cat, repo, lockSvc, platform.SystemClock{}, platform.RandomIDs{}).EnablePublishing()
	return lifecycleFixture{pool: pool, svc: svc, repo: repo, locks: lockSvc,
		user: identity.Principal{UserID: userID, Login: "priya-s", Type: identity.ActorUser}}
}

func (f lifecycleFixture) pendingPublishJobs(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE topic = 'request.publish'
		AND aggregate_id = $1 AND processed_at IS NULL AND dead_at IS NULL`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f lifecycleFixture) actions(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT action FROM audit_events WHERE subject_id = $1 ORDER BY seq`, id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestPublishedRequestLifecycle(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	res, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "lifecycle-key-01"))
	if err != nil {
		t.Fatal(err)
	}
	req := res.Request
	if f.pendingPublishJobs(t, req.ID) != 1 {
		t.Fatal("create must enqueue one publish job in its transaction")
	}
	target, err := f.svc.PublishTarget(ctx, req.ID)
	if err != nil || target.FencingToken == nil {
		t.Fatalf("publish target: %+v, %v", target, err)
	}

	// A stale token (files rendered under an older lease) is refused.
	pub := PublishedPR{Publisher: "local", Branch: "iacp/" + req.ShortID, CommitSHA: strings.Repeat("c", 40),
		TreeSHA: strings.Repeat("e", 40), Number: 812, URL: "file:///scratch#pull/812", FencingToken: *target.FencingToken + 1}
	if _, err := f.svc.RecordPublished(ctx, req.ID, pub, "corr-1"); !hasCode(err, "stale_fencing_token") {
		t.Fatalf("stale token: %v", err)
	}
	pub.FencingToken = *target.FencingToken
	got, err := f.svc.RecordPublished(ctx, req.ID, pub, "corr-1")
	if err != nil || got.Status != StatusPROpen || got.PR == nil || got.PR.Number != 812 {
		t.Fatalf("record published: %+v, %v", got, err)
	}
	// Replaying the same result is a no-op; the request is no longer publishable.
	if again, err := f.svc.RecordPublished(ctx, req.ID, pub, "corr-1"); err != nil || again.Status != StatusPROpen {
		t.Fatalf("replay: %+v, %v", again, err)
	}
	if _, err := f.svc.PublishTarget(ctx, req.ID); err == nil {
		t.Fatal("a pr_open request must not be published again")
	}
	stored, err := f.svc.Get(ctx, req.ID)
	if err != nil || stored.PR == nil || stored.PR.Branch != pub.Branch || stored.PR.URL != pub.URL || stored.RenderedTreeSHA != pub.TreeSHA {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	// Webhook events: wrong PR and disallowed transitions are ignored and audited.
	out, err := f.svc.ApplyGitHubEvent(ctx, GitHubEvent{RequestShortID: req.ShortID, PRNumber: 7, Event: EventPRClosed, DeliveryID: "d-1"})
	if err != nil || out.Applied || out.Reason != "pr_mismatch" {
		t.Fatalf("pr mismatch: %+v, %v", out, err)
	}
	out, err = f.svc.ApplyGitHubEvent(ctx, GitHubEvent{RequestShortID: req.ShortID, PRNumber: 812, Event: EventPRMerged,
		MergeCommitSHA: strings.Repeat("a", 40), DeliveryID: "d-2"})
	if err != nil || out.Applied || out.Reason != "invalid_transition" {
		t.Fatalf("unapproved merge: %+v, %v", out, err)
	}
	if out, err := f.svc.ApplyGitHubEvent(ctx, GitHubEvent{RequestShortID: "req-unknown1", PRNumber: 1, Event: EventPRClosed}); err != nil || out.Reason != "unknown_request" {
		t.Fatalf("unknown request: %+v, %v", out, err)
	}
	if ok, err := f.svc.RecordWorkflowHint(ctx, WorkflowHint{RequestShortID: req.ShortID, Workflow: ".github/workflows/pr-validate.yml",
		RunID: 11873399021, Conclusion: "success", DeliveryID: "d-3"}); err != nil || !ok {
		t.Fatalf("hint: %v, %v", ok, err)
	}

	// Closing the PR unmerged rejects the request and releases its lock.
	out, err = f.svc.ApplyGitHubEvent(ctx, GitHubEvent{RequestShortID: req.ShortID, PRNumber: 812, Event: EventPRClosed,
		Action: "closed", DeliveryID: "d-4"})
	if err != nil || !out.Applied || out.From != StatusPROpen || out.To != StatusRejected {
		t.Fatalf("close: %+v, %v", out, err)
	}
	if _, err := f.locks.Get(ctx, req.Stack.ID); err == nil {
		t.Fatal("the lock must be released when the request is rejected")
	}
	// A redelivery after the terminal transition is ignored.
	if out, err := f.svc.ApplyGitHubEvent(ctx, GitHubEvent{RequestShortID: req.ShortID, PRNumber: 812, Event: EventPRClosed, DeliveryID: "d-5"}); err != nil || out.Applied {
		t.Fatalf("redelivery: %+v, %v", out, err)
	}

	want := []string{"request.created", "request.publish_enqueued", "request.transitioned", "request.github_event_ignored",
		"request.github_event_ignored", "request.workflow_run_completed", "request.transitioned", "request.github_event_ignored"}
	if got := f.actions(t, req.ID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit trail:\n got %v\nwant %v", got, want)
	}
	if v, err := audit.NewVerifier(f.pool).Verify(ctx, 1, 0); err != nil || !v.Valid {
		t.Fatalf("audit chain: %+v, %v", v, err)
	}
}

// TestRecordPublishedRequiresTheCurrentLease reproduces the stale-lease case: the request
// row keeps its fencing token after the lock is released or taken over, so recording
// must check the lock row itself.
func TestRecordPublishedRequiresTheCurrentLease(t *testing.T) {
	tests := []struct {
		name string
		lose func(t *testing.T, f lifecycleFixture, req *Request, token int64)
	}{
		{name: "released", lose: func(t *testing.T, f lifecycleFixture, req *Request, token int64) {
			if _, err := f.locks.Release(context.Background(), locks.TokenCommand{StackID: req.Stack.ID, RequestID: req.ID,
				FencingToken: token, By: f.user}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "taken over after expiry", lose: func(t *testing.T, f lifecycleFixture, req *Request, _ int64) {
			ctx := context.Background()
			var other uuid.UUID
			if err := f.pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
				VALUES ('req-takeover01', $1, 'update', '{}', $2) RETURNING id`, req.Stack.ID, f.user.UserID).Scan(&other); err != nil {
				t.Fatal(err)
			}
			later := platform.FixedClock{T: time.Now().UTC().Add(8 * 24 * time.Hour)}
			writer := audit.NewWriter(later, platform.RandomIDs{})
			res, err := locks.NewService(locks.NewPostgresRepository(f.pool, writer), later, 0).
				Acquire(ctx, locks.AcquireCommand{StackID: req.Stack.ID, RequestID: other, By: f.user})
			if err != nil || res.Outcome != locks.OutcomeGranted {
				t.Fatalf("takeover: %+v, %v", res, err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			ctx := context.Background()
			res, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "stale-lease-01"))
			if err != nil {
				t.Fatal(err)
			}
			req := res.Request
			target, err := f.svc.PublishTarget(ctx, req.ID)
			if err != nil {
				t.Fatal(err)
			}
			token := *target.FencingToken
			tt.lose(t, f, req, token)

			if _, err := f.svc.PublishTarget(ctx, req.ID); !errors.Is(err, ErrNotPublishable) {
				t.Fatalf("PublishTarget after losing the lease: %v", err)
			}
			pub := PublishedPR{Publisher: "local", Branch: "iacp/" + req.ShortID, CommitSHA: strings.Repeat("c", 40),
				TreeSHA: strings.Repeat("e", 40), Number: 9, URL: "file:///x#pull/9", FencingToken: token}
			if _, err := f.svc.RecordPublished(ctx, req.ID, pub, "c"); !hasCode(err, "stale_fencing_token") {
				t.Fatalf("RecordPublished after losing the lease: %v", err)
			}
			if got, _ := f.svc.Get(ctx, req.ID); got.Status != StatusDraft || got.PR != nil {
				t.Fatalf("request must stay draft without a PR: %+v", got)
			}
		})
	}
}

func TestTerminalTransitionPromotesAndPublishesNextRequest(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	res, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "promote-key-01"))
	if err != nil {
		t.Fatal(err)
	}
	first := res.Request
	// A second request on the same stack (update requests arrive with WBS 4.5) queues behind.
	var secondID uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
		VALUES ('req-second001', $1, 'update', '{}', $2) RETURNING id`, first.Stack.ID, f.user.UserID).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	lr, err := f.locks.Acquire(ctx, locks.AcquireCommand{StackID: first.Stack.ID, RequestID: secondID, By: f.user})
	if err != nil || lr.Outcome != locks.OutcomeQueued {
		t.Fatalf("second request must queue: %+v, %v", lr, err)
	}
	if f.pendingPublishJobs(t, secondID) != 0 {
		t.Fatal("a queued request must not be published")
	}

	// The first request's publish keeps failing: the final failure ends it and promotes the second.
	if err := f.svc.RecordPublishFailure(ctx, first.ID, PublishFailure{Publisher: "github", Attempt: 1, Error: "502"}, "c"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.Get(ctx, first.ID); got.Status != StatusDraft {
		t.Fatalf("a non-final failure must keep the request in draft, got %s", got.Status)
	}
	if err := f.svc.RecordPublishFailure(ctx, first.ID, PublishFailure{Publisher: "github", Attempt: 6, Error: "502", Final: true}, "c"); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Get(ctx, first.ID)
	if err != nil || got.Status != StatusFailed || got.FailureReason != "publish_failed" || got.CompletedAt == nil || len(got.FailureDetail) == 0 {
		t.Fatalf("first = %+v, %v", got, err)
	}
	lock, err := f.locks.Get(ctx, first.Stack.ID)
	if err != nil || lock.RequestID != secondID {
		t.Fatalf("lock must pass to the second request: %+v, %v", lock, err)
	}
	if f.pendingPublishJobs(t, secondID) != 1 {
		t.Fatal("the promoted request must get a publish job in the same transaction")
	}
	second, err := f.svc.PublishTarget(ctx, secondID)
	if err != nil || second.FencingToken == nil || *second.FencingToken != lock.FencingToken {
		t.Fatalf("promoted request: %+v, %v", second, err)
	}
}
