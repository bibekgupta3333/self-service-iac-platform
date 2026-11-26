package requests

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestPostgresRequestFlow(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments', 'Payments', 7, 'payments', 'cc-4102')`); err != nil {
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
	svc := NewService(repo, cat, repo, lockSvc, platform.SystemClock{}, platform.RandomIDs{})
	user := identity.Principal{UserID: userID, Login: "priya-s", Type: identity.ActorUser}
	in := validInput()
	in.Ephemeral = &Ephemeral{TTLHours: 72}

	// Not a member yet: forbidden. A viewer is still forbidden; a developer may create.
	if _, err := svc.Create(ctx, user, createCmd(in, "member-check-01")); !apperr.IsKind(err, apperr.KindForbidden) {
		t.Fatalf("non-member: want forbidden, got %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (team_id, user_id, role)
		SELECT id, $1, 'viewer' FROM teams WHERE slug = 'payments'`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, user, createCmd(in, "member-check-02")); !apperr.IsKind(err, apperr.KindForbidden) {
		t.Fatalf("viewer: want forbidden, got %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE memberships SET role = 'developer' WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}

	// Concurrent calls with the same key create exactly one request.
	const n = 5
	results := make([]CreateResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.Create(ctx, user, createCmd(in, "6a1f0c7e-2b9d-4d3e"))
		}()
	}
	wg.Wait()
	created := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if !results[i].Replayed {
			created++
		}
		if results[i].Request.ID != results[0].Request.ID {
			t.Fatal("all calls must return the same request")
		}
	}
	if created != 1 {
		t.Fatalf("created %d requests, want 1", created)
	}

	req := results[0].Request
	got, err := svc.Get(ctx, req.ID)
	if err != nil || got.ShortID != req.ShortID || got.Stack.Path != "environments/development/payments/order-events" ||
		got.Stack.Status != "provisioning" || got.Status != StatusDraft || len(got.Spec) == 0 {
		t.Fatalf("get = %+v, %v", got, err)
	}

	if _, err := svc.Create(ctx, user, createCmd(in, "another-key-123")); !hasCode(err, "stack_exists") {
		t.Fatalf("want stack_exists, got %v", err)
	}
	changed := in
	changed.Justification = "changed"
	if _, err := svc.Create(ctx, user, createCmd(changed, "6a1f0c7e-2b9d-4d3e")); !hasCode(err, "idempotency_key_reuse") {
		t.Fatalf("want idempotency_key_reuse, got %v", err)
	}

	// A second stack for pagination and filters.
	second := validInput()
	second.Stack = "order-api"
	if _, err := svc.Create(ctx, user, createCmd(second, "third-key-1234")); err != nil {
		t.Fatal(err)
	}
	page, next, err := svc.List(ctx, ListFilter{Limit: 1, Team: "payments"})
	if err != nil || len(page) != 1 || next == nil || page[0].Stack.Path != "environments/development/payments/order-api" {
		t.Fatalf("page1 = %+v %v %v", page, next, err)
	}
	page, next, err = svc.List(ctx, ListFilter{Limit: 1, After: next, CreatedBy: &userID})
	if err != nil || len(page) != 1 || next != nil || page[0].ID != req.ID {
		t.Fatalf("page2 = %+v %v %v", page, next, err)
	}
	if page, _, _ := svc.List(ctx, ListFilter{Team: "checkout"}); len(page) != 0 {
		t.Fatalf("team filter returned %d rows", len(page))
	}

	res, err := audit.NewVerifier(pool).Verify(ctx, 1, 0)
	if err != nil || !res.Valid || res.Checked != 6 { // 2 index + 2 request.created + 2 lock.acquired
		t.Fatalf("audit = %+v, %v", res, err)
	}

	// Each create took its new stack's lock in the same transaction.
	var lockHolder uuid.UUID
	var lockToken int64
	if err := pool.QueryRow(ctx, `SELECT request_id, fencing_token FROM locks WHERE stack_id = $1`, req.Stack.ID).
		Scan(&lockHolder, &lockToken); err != nil || lockHolder != req.ID || req.FencingToken == nil || *req.FencingToken != lockToken {
		t.Fatalf("lock row = %s/%d (%v), request token = %v", lockHolder, lockToken, err, req.FencingToken)
	}
	if got, err := svc.Get(ctx, req.ID); err != nil || got.FencingToken == nil || *got.FencingToken != lockToken || got.IsQueued() {
		t.Fatalf("stored request token = %+v, %v", got, err)
	}

	t.Run("idempotency keys expire after 24h", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE idempotency_keys SET created_at = now() - interval '25 hours'
			WHERE key = '6a1f0c7e-2b9d-4d3e'`); err != nil {
			t.Fatal(err)
		}
		third := validInput()
		third.Stack = "order-worker"
		res, err := svc.Create(ctx, user, createCmd(third, "6a1f0c7e-2b9d-4d3e"))
		if err != nil || res.Replayed {
			t.Fatalf("expired key must be reusable with a new body: %+v, %v", res, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE idempotency_keys SET created_at = now() - interval '2 days'
			WHERE key = 'third-key-1234'`); err != nil {
			t.Fatal(err)
		}
		n, err := repo.PurgeExpiredIdempotencyKeys(ctx, time.Now().Add(-IdempotencyWindow))
		if err != nil || n != 1 {
			t.Fatalf("purged %d, %v; want 1", n, err)
		}
	})
}
