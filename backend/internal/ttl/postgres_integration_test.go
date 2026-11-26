package ttl_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

type commentSink struct {
	number int
	body   string
}

func (c *commentSink) CommentOnPullRequest(_ context.Context, n int, body string) error {
	c.number, c.body = n, body
	return nil
}

func countAudit(t *testing.T, pool *pgxpool.Pool, action, subject string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1 AND subject_id = $2`,
		action, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestTTLLifecycleAgainstPostgres runs the whole TTL flow on a migrated database: request
// time bounds, due list, warning (audit + outbox + PR comment), extension and its cap,
// teardown through a real destroy request, and the production guards.
func TestTTLLifecycleAgainstPostgres(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Fields(sql)[0], err)
		}
	}

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center) VALUES ('payments', 'Payments', 7, 'payments', 'cc-4102')`)
	exec(`INSERT INTO memberships (team_id, user_id, role) SELECT id, $1, 'developer' FROM teams WHERE slug = 'payments'`, userID)
	user := identity.Principal{UserID: userID, Login: "priya-s", Type: identity.ActorUser}

	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	cat := catalog.NewService(catalog.NewPostgresRepository(pool, writer), platform.SystemClock{}, "https://example.test/iacp.git")
	if _, _, err := cat.Index(ctx, identity.Principal{Login: "module-release.yml", Type: identity.ActorGitHubActions}, catalog.IndexCommand{
		Module: "sqs", Version: "1.3.0", GitTag: "modules/sqs/v1.3.0", CommitSHA: strings.Repeat("9c", 20),
		DisplayName: "SQS", Description: "queue", Category: "messaging", Owners: []string{"platform"},
		InputsSchema: []byte(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
		Outputs:      []catalog.Output{{Name: "queue_arn"}}, TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot"}); err != nil {
		t.Fatal(err)
	}

	policy := ttl.Policy{MinHours: 1, MaxHours: 720, DefaultHours: 72, MaxExtensionHours: 100}
	now := time.Now().UTC().Truncate(time.Second)
	reqRepo := requests.NewPostgresRepository(pool, writer)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, locks.DefaultLease)
	newRequests := func(clock platform.Clock) *requests.Service {
		return requests.NewService(reqRepo, cat, reqRepo, lockSvc, clock, platform.RandomIDs{}).WithTTLPolicy(policy)
	}
	create := func(name string, createdAgo time.Duration, ttlHours int) uuid.UUID {
		t.Helper()
		in := requests.CreateInput{Kind: requests.KindCreate, Team: "payments", Environment: "development", Stack: name,
			Resources: []requests.ResourceInput{{Name: "orders_queue", Module: "sqs", Version: "1.3.0", Inputs: json.RawMessage(`{"name":"orders"}`)}}}
		if ttlHours >= 0 {
			in.Ephemeral = &requests.Ephemeral{TTLHours: ttlHours}
		}
		body, _ := json.Marshal(in)
		res, err := newRequests(platform.FixedClock{T: now.Add(-createdAgo)}).Create(ctx, user, requests.CreateCommand{
			Input: in, IdempotencyKey: "key-" + name + "-0001", RequestHash: requests.RequestHash("POST", "/v1/requests", body), CorrelationID: "seed"})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		// The create finished: the stack is active and the lock is free.
		id := res.Request.Stack.ID
		exec(`UPDATE requests SET status = 'cancelled', completed_at = now() WHERE stack_id = $1`, id)
		exec(`UPDATE stacks SET status = 'active' WHERE id = $1`, id)
		exec(`DELETE FROM locks WHERE stack_id = $1`, id)
		return id
	}

	expired := create("expired", 80*time.Hour, 72)  // expired 8 h ago
	nearing := create("nearing", 60*time.Hour, 72)  // expires in 12 h
	fresh := create("fresh", 0, 0)                  // default TTL: 72 h from now
	permanent := create("permanent", time.Hour, -1) // no TTL

	var ttlHours *int
	var isEphemeral bool
	if err := pool.QueryRow(ctx, `SELECT is_ephemeral, ttl_hours FROM stacks WHERE id = $1`, fresh).Scan(&isEphemeral, &ttlHours); err != nil ||
		!isEphemeral || ttlHours == nil || *ttlHours != 72 {
		t.Fatalf("default TTL not stored: %v %v %v", isEphemeral, ttlHours, err)
	}
	if err := pool.QueryRow(ctx, `SELECT is_ephemeral, ttl_hours FROM stacks WHERE id = $1`, permanent).Scan(&isEphemeral, &ttlHours); err != nil ||
		isEphemeral || ttlHours != nil {
		t.Fatalf("permanent stack carries a TTL: %v %v %v", isEphemeral, ttlHours, err)
	}

	// Schema guards.
	if _, err := pool.Exec(ctx, `UPDATE stacks SET ttl_hours = 5 WHERE id = $1`, permanent); err == nil {
		t.Error("a permanent stack must not accept a ttl_hours")
	}
	// Production never allows ephemeral stacks, whatever an INSERT or UPDATE says.
	exec(`INSERT INTO environments (name, aws_account_id, allowed_regions, allow_ephemeral, max_ttl_hours)
		VALUES ('production', '111111111111', ARRAY['us-east-1'], true, 48)`)
	exec(`UPDATE environments SET allow_ephemeral = true WHERE name = 'production'`)
	var prodAllows bool
	var prodMax *int
	if err := pool.QueryRow(ctx, `SELECT allow_ephemeral, max_ttl_hours FROM environments WHERE name = 'production'`).Scan(&prodAllows, &prodMax); err != nil ||
		prodAllows || prodMax != nil {
		t.Fatalf("production environment allows ephemeral: %v %v %v", prodAllows, prodMax, err)
	}

	ttlRepo := ttl.NewPostgresRepository(pool, writer)
	svc := ttl.NewService(ttlRepo, reqRepo, newRequests(platform.SystemClock{}), platform.SystemClock{}, policy).EnableComments("https://portal.example/stacks")

	// Due: the expired stack to tear down, the nearing one to warn about; nothing else.
	due, err := svc.Due(ctx)
	if err != nil || len(due) != 2 || due[0].Stack.ID != expired || due[0].Phase != ttl.PhaseTeardown ||
		due[1].Stack.ID != nearing || due[1].Phase != ttl.PhaseWarn {
		t.Fatalf("Due = %+v, %v", due, err)
	}

	// Warn: audit event, stamp, outbox job; once only.
	if res, err := svc.Warn(ctx, nearing, "corr-w"); err != nil || !res.Warned {
		t.Fatalf("Warn = %+v, %v", res, err)
	}
	if res, err := svc.Warn(ctx, nearing, "corr-w"); err != nil || res.Warned {
		t.Fatalf("second Warn = %+v, %v", res, err)
	}
	if n := countAudit(t, pool, "ttl.warned", nearing.String()); n != 1 {
		t.Fatalf("ttl.warned events = %d", n)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = $1 AND dedupe_key = $2`,
		outbox.TopicTTLWarning, "ttl-warn:"+nearing.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("warning jobs = %d, %v", jobs, err)
	}
	if due, _ = svc.Due(ctx); len(due) != 1 || due[0].Stack.ID != expired {
		t.Fatalf("a warned stack is not due again: %+v", due)
	}

	// The notifier comments on the stack's newest pull request.
	exec(`UPDATE requests SET github_pr_number = 77, github_branch = 'iacp/req-x', github_pr_url = 'https://example.test/pull/77' WHERE stack_id = $1`, nearing)
	sink := &commentSink{}
	payload, _ := json.Marshal(ttl.WarnPayload{StackID: nearing})
	if err := ttl.NewWarningNotifier(ttlRepo, sink, platform.SystemClock{}, "https://portal.example/stacks").
		Handle(ctx, outbox.Job{ID: 1, Topic: outbox.TopicTTLWarning, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if sink.number != 77 || !strings.Contains(sink.body, "environments/development/payments/nearing") ||
		!strings.Contains(sink.body, "https://portal.example/stacks/"+nearing.String()) {
		t.Fatalf("comment #%d: %s", sink.number, sink.body)
	}

	// Extend: the new expiry, the reset warning and the audit event; the cap holds.
	before := time.Now()
	res, err := svc.Extend(ctx, user, nearing, 60, "corr-e")
	if err != nil || res.ExtendedHours != 60 || res.RemainingExtHours != 40 || res.ExpiresAt.Before(before.Add(70*time.Hour)) {
		t.Fatalf("Extend = %+v, %v", res, err)
	}
	var warnedAt *time.Time
	var extended int
	if err := pool.QueryRow(ctx, `SELECT ttl_warned_at, ttl_extended_hours FROM stacks WHERE id = $1`, nearing).Scan(&warnedAt, &extended); err != nil ||
		warnedAt != nil || extended != 60 {
		t.Fatalf("stored: warned=%v extended=%d err=%v", warnedAt, extended, err)
	}
	if n := countAudit(t, pool, "ttl.extended", nearing.String()); n != 1 {
		t.Fatalf("ttl.extended events = %d", n)
	}
	if _, err := svc.Extend(ctx, user, nearing, 41, "c"); !apperr.IsKind(err, apperr.KindConflict) {
		t.Fatalf("over the cap: %v", err)
	}
	if _, err := svc.Extend(ctx, identity.Principal{UserID: uuid.New(), Type: identity.ActorUser}, nearing, 1, "c"); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("a user outside the team must get not found: %v", err)
	}
	if _, err := svc.Extend(ctx, user, permanent, 1, "c"); !apperr.IsKind(err, apperr.KindConflict) {
		t.Fatalf("permanent stack: %v", err)
	}

	// Teardown: a destroy request owned by the platform bot, once; refusals for the rest.
	td, err := svc.Teardown(ctx, expired, "corr-t")
	if err != nil || !td.Created || td.RequestID == uuid.Nil {
		t.Fatalf("Teardown = %+v, %v", td, err)
	}
	var kind, status, justification, login string
	var system bool
	if err := pool.QueryRow(ctx, `SELECT r.kind::text, r.status::text, r.justification, u.github_login, u.is_system
		FROM requests r JOIN users u ON u.id = r.created_by WHERE r.id = $1`, td.RequestID).
		Scan(&kind, &status, &justification, &login, &system); err != nil {
		t.Fatal(err)
	}
	if kind != "destroy" || status != "draft" || justification != "ttl expired" || login != "iacp-bot" || !system {
		t.Fatalf("destroy request: %s %s %q by %s (system=%v)", kind, status, justification, login, system)
	}
	if n := countAudit(t, pool, "ttl.expired", expired.String()); n != 1 {
		t.Fatalf("ttl.expired events = %d", n)
	}
	var holder uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT request_id FROM locks WHERE stack_id = $1`, expired).Scan(&holder); err != nil || holder != td.RequestID {
		t.Fatalf("the destroy request must hold the stack lock: %v %v", holder, err)
	}
	if again, err := svc.Teardown(ctx, expired, "corr-t"); err != nil || again.Created {
		t.Fatalf("second Teardown = %+v, %v", again, err)
	}
	if due, _ = svc.Due(ctx); len(due) != 0 {
		t.Fatalf("a stack with an open destroy is not due: %+v", due)
	}
	if _, err := svc.Extend(ctx, user, expired, 1, "c"); err == nil {
		t.Fatal("a stack being destroyed cannot be extended")
	}
	for name, id := range map[string]uuid.UUID{"unexpired": fresh, "permanent": permanent} {
		if _, err := svc.Teardown(ctx, id, "c"); !apperr.IsKind(err, apperr.KindConflict) {
			t.Errorf("Teardown of the %s stack = %v, want conflict", name, err)
		}
	}

	// Production is never torn down, even if a stack row there claims to be ephemeral.
	exec(`INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, is_ephemeral, ttl_hours, expires_at, created_by, created_at)
		SELECT t.id, e.id, 'rogue', 'environments/production/payments/rogue', 'production/payments/rogue/terraform.tfstate',
		       'us-east-1', true, 1, now() - interval '5 hours', $1, now() - interval '6 hours'
		FROM teams t, environments e WHERE t.slug = 'payments' AND e.name = 'production'`, userID)
	var rogue uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM stacks WHERE name = 'rogue'`).Scan(&rogue); err != nil {
		t.Fatal(err)
	}
	if due, _ = svc.Due(ctx); len(due) != 0 {
		t.Fatalf("production appears in the due list: %+v", due)
	}
	_, err = svc.Teardown(ctx, rogue, "c")
	if e, ok := apperr.As(err); !ok || e.Code != "production_not_ephemeral" {
		t.Fatalf("production teardown = %v", err)
	}
	var destroys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE stack_id = $1`, rogue).Scan(&destroys); err != nil || destroys != 0 {
		t.Fatalf("requests on the production stack = %d, %v", destroys, err)
	}
}
