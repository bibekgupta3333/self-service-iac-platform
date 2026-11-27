package recovery

import (
	"context"
	"encoding/json"
	"strings"
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
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// TestRollbackThroughTheRequestPath opens a rollback against Postgres with the real
// request service: history lookup, idempotent replay, the busy check and the stored spec.
func TestRollbackThroughTheRequestPath(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	var userID uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (42, 'priya-s') RETURNING id`).Scan(&userID))
	if _, err := pool.Exec(ctx, `
		WITH tm AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		            VALUES ('payments', 'Payments', 7, 'payments', 'cc-4102') RETURNING id)
		INSERT INTO memberships (team_id, user_id, role) SELECT id, $1, 'maintainer' FROM tm`, userID); err != nil {
		t.Fatal(err)
	}
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	cat := catalog.NewService(catalog.NewPostgresRepository(pool, writer), platform.SystemClock{}, "https://example.test/iacp.git")
	if _, _, err := cat.Index(ctx, identity.Principal{Login: "module-release.yml", Type: identity.ActorGitHubActions}, catalog.IndexCommand{
		Module: "sqs", Version: "1.3.0", GitTag: "modules/sqs/v1.3.0", CommitSHA: strings.Repeat("9c", 20),
		DisplayName: "SQS", Description: "queue", Category: "messaging", Owners: []string{"platform"},
		InputsSchema:   []byte(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
		Outputs:        []catalog.Output{{Name: "queue_arn"}},
		TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot"}); err != nil {
		t.Fatal(err)
	}
	reqRepo := requests.NewPostgresRepository(pool, writer)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, 0)
	reqSvc := requests.NewService(reqRepo, cat, reqRepo, lockSvc, platform.SystemClock{}, platform.RandomIDs{})
	user := identity.Principal{UserID: userID, Login: "priya-s", Type: identity.ActorUser}
	queue := func(name string) requests.ResourceInput {
		return requests.ResourceInput{Name: name, Module: "sqs", Version: "1.3.0", Inputs: json.RawMessage(`{"name":"` + strings.ReplaceAll(name, "_", "-") + `"}`)}
	}
	// finish marks a request applied at the given time and frees the stack, as the apply
	// pipeline would.
	finish := func(id, stack uuid.UUID, at time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE requests SET status = 'applied', approved_plan_hash = repeat('a', 64),
			completed_at = $2 WHERE id = $1`, id, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, stack); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE stacks SET status = 'active' WHERE id = $1`, stack); err != nil {
			t.Fatal(err)
		}
	}

	created, err := reqSvc.Create(ctx, user, requests.CreateCommand{IdempotencyKey: "rb-create-0001", RequestHash: "h-create",
		Input: requests.CreateInput{Kind: requests.KindCreate, Team: "payments", Environment: "development", Stack: "orders",
			Resources: []requests.ResourceInput{queue("orders_queue")}}})
	if err != nil {
		t.Fatal(err)
	}
	stackID := created.Request.Stack.ID
	finish(created.Request.ID, stackID, time.Now().Add(-2*time.Hour))
	updated, err := reqSvc.Create(ctx, user, requests.CreateCommand{IdempotencyKey: "rb-update-0001", RequestHash: "h-update",
		Input: requests.CreateInput{Kind: requests.KindUpdate, StackID: &stackID,
			Resources: []requests.ResourceInput{queue("orders_queue"), queue("orders_dlq")}}})
	if err != nil {
		t.Fatal(err)
	}
	finish(updated.Request.ID, stackID, time.Now().Add(-time.Hour))

	svc := NewRollbackService(reqRepo, reqRepo, NewPostgresRepository(pool, writer), reqSvc)
	cmd := RollbackCommand{StackID: stackID, Reason: "the DLQ broke the consumer", IdempotencyKey: "rb-rollback-001",
		RequestHash: requests.RequestHash("POST", "/v1/stacks/x/rollback", []byte("1")), CorrelationID: "it-rollback"}
	res, err := svc.Rollback(ctx, user, cmd)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Request
	if r.Kind != requests.KindUpdate || res.Replayed || r.FencingToken == nil {
		t.Fatalf("rollback request %+v", r)
	}
	stored, err := reqSvc.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var spec requests.NormalizedSpec
	if err := json.Unmarshal(stored.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.RollbackOf != created.Request.ShortID || len(spec.Resources) != 1 || spec.Resources[0].Name != "orders_queue" {
		t.Fatalf("spec %s", stored.Spec)
	}
	if !strings.HasPrefix(stored.Justification, "Rollback to the configuration applied by "+created.Request.ShortID) {
		t.Fatalf("justification %q", stored.Justification)
	}
	var payload []byte
	mustScan(t, pool.QueryRow(ctx, `SELECT payload FROM audit_events WHERE subject_id = $1 AND action = 'request.created'`, r.ID.String()).Scan(&payload))
	var created0 map[string]any
	if err := json.Unmarshal(payload, &created0); err != nil || created0["rollback_of"] != created.Request.ShortID {
		t.Fatalf("audit payload %s", payload)
	}

	// The same call again returns the same request.
	again, err := svc.Rollback(ctx, user, cmd)
	if err != nil || !again.Replayed || again.Request.ID != r.ID {
		t.Fatalf("replay %+v, %v", again, err)
	}
	// A new rollback while this one is open: busy.
	cmd.IdempotencyKey, cmd.RequestHash = "rb-rollback-002", requests.RequestHash("POST", "/v1/stacks/x/rollback", []byte("2"))
	if _, err := svc.Rollback(ctx, user, cmd); !apperr.IsKind(err, apperr.KindConflict) || code(err) != "stack_busy" {
		t.Fatalf("second rollback: %v", err)
	}

	// The open rollback ends; then the target's module version is yanked: the rollback is
	// refused with an explanation that points at a roll-forward, and nothing is created.
	if _, err := pool.Exec(ctx, `UPDATE requests SET status = 'cancelled', completed_at = now() WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM locks WHERE stack_id = $1`, stackID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE module_versions SET status = 'yanked'
		WHERE module_id = (SELECT id FROM modules WHERE name = 'sqs') AND version = '1.3.0'`); err != nil {
		t.Fatal(err)
	}
	var before int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE stack_id = $1`, stackID).Scan(&before))
	cmd.IdempotencyKey, cmd.RequestHash = "rb-rollback-yank", requests.RequestHash("POST", "/v1/stacks/x/rollback", []byte("y"))
	_, err = svc.Rollback(ctx, user, cmd)
	if !apperr.IsKind(err, apperr.KindInvalid) || code(err) != "rollback_target_yanked" ||
		!strings.Contains(err.Error(), created.Request.ShortID) {
		t.Fatalf("yanked target: %v", err)
	}
	var after int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE stack_id = $1`, stackID).Scan(&after))
	if after != before {
		t.Fatal("a refused rollback must create no request")
	}

	// Destroyed stacks cannot be rolled back.
	if _, err := pool.Exec(ctx, `UPDATE stacks SET status = 'destroyed', destroyed_at = now() WHERE id = $1`, stackID); err != nil {
		t.Fatal(err)
	}
	cmd.IdempotencyKey, cmd.RequestHash = "rb-rollback-003", requests.RequestHash("POST", "/v1/stacks/x/rollback", []byte("3"))
	if _, err := svc.Rollback(ctx, user, cmd); code(err) != "stack_destroyed" {
		t.Fatalf("destroyed: %v", err)
	}
}
