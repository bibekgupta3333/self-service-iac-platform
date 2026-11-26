package requests

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// With a tiny pool, concurrent creates that collide on one Idempotency-Key must not
// deadlock: a transaction may never wait for a second connection from the same pool.
func TestConcurrentCreatesOnASmallPool(t *testing.T) {
	big := dbtest.NewDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := big.Config().Copy()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login) VALUES (7, 'dev') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		WITH t AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		           VALUES ('payments', 'Payments', 7, 'payments', 'cc-1') RETURNING id)
		INSERT INTO memberships (team_id, user_id, role) SELECT id, $1, 'developer' FROM t`, userID); err != nil {
		t.Fatal(err)
	}
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	cat := catalog.NewService(catalog.NewPostgresRepository(pool, writer), platform.SystemClock{}, "https://example.test/iacp.git")
	bot := identity.Principal{Login: "release", Type: identity.ActorGitHubActions}
	for _, c := range []catalog.IndexCommand{
		{Module: "sqs", Version: "1.3.0", GitTag: "modules/sqs/v1.3.0", CommitSHA: strings.Repeat("9c", 20),
			DisplayName: "SQS", Description: "q", Category: "messaging", Owners: []string{"platform"},
			InputsSchema: []byte(`{"type":"object"}`), Outputs: []catalog.Output{{Name: "queue_arn"}, {Name: "queue_url"}},
			TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot"},
		{Module: "lambda", Version: "2.0.1", GitTag: "modules/lambda/v2.0.1", CommitSHA: strings.Repeat("8b", 20),
			DisplayName: "Lambda", Description: "f", Category: "compute", Owners: []string{"platform"},
			InputsSchema: []byte(`{"type":"object"}`), Outputs: []catalog.Output{{Name: "function_arn"}},
			TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot"},
	} {
		if _, _, err := cat.Index(ctx, bot, c); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewPostgresRepository(pool, writer)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, writer), platform.SystemClock{}, 0)
	svc := NewService(repo, cat, repo, lockSvc, platform.SystemClock{}, platform.RandomIDs{})
	user := identity.Principal{UserID: userID, Login: "dev", Type: identity.ActorUser}

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := validInput()
			key := "shared-key-0001"
			if i%2 == 1 { // half collide on one key, half are independent stacks
				in.Stack = fmt.Sprintf("stack-%d", i)
				key = fmt.Sprintf("own-key-%04d", i)
			}
			_, errs[i] = svc.Create(ctx, user, createCmd(in, key))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("create %d: %v (a context deadline here means the pool deadlocked)", i, err)
		}
	}
}
