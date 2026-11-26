package requests

import (
	"context"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// TestUpgradeAgainstPostgres runs an upgrade through the real repository and catalog: the
// target version is resolved and validated, the spec it starts from is re-read under the
// stack row lock, and a retry with the same key replays it.
func TestUpgradeAgainstPostgres(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	writer := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	cat := catalog.NewService(catalog.NewPostgresRepository(f.pool, writer), platform.SystemClock{}, "https://example.test/iacp.git")
	bot := identity.Principal{Login: "module-release.yml", Type: identity.ActorGitHubActions}
	for _, v := range []string{"1.4.0", "2.0.0"} {
		cmd := catalog.IndexCommand{Module: "sqs", Version: v, GitTag: "modules/sqs/v" + v, CommitSHA: strings.Repeat("7a", 20),
			DisplayName: "SQS", Description: "queue", Category: "messaging", Owners: []string{"platform"},
			InputsSchema:   []byte(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
			Outputs:        []catalog.Output{{Name: "queue_arn"}, {Name: "queue_url"}},
			TFVersionRange: ">= 1.10", AWSProviderRange: "~> 5.80", ReleasedBy: "bot", UpgradeNotes: "Read me."}
		if _, _, err := cat.Index(ctx, bot, cmd); err != nil {
			t.Fatalf("index %s: %v", v, err)
		}
	}
	created, err := f.svc.Create(ctx, f.user, createCmd(validInput(), "upgrade-create-01"))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Request.Stack.ID
	for _, q := range []string{
		`UPDATE requests SET status = 'applied', approved_plan_hash = repeat('a', 64), completed_at = now() WHERE stack_id = $1`,
		`DELETE FROM locks WHERE stack_id = $1`,
		`UPDATE stacks SET status = 'active' WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, q, id); err != nil {
			t.Fatal(err)
		}
	}

	major := stackInput(upgradeInput("2.0.0"), id)
	if _, err := f.svc.Create(ctx, f.user, createCmd(major, "upgrade-major-001")); !hasCode(err, "acknowledge_breaking_required") {
		t.Fatalf("unacknowledged major: %v", err)
	}
	in := stackInput(upgradeInput("1.4.0"), id)
	up, err := f.svc.Create(ctx, f.user, createCmd(in, "upgrade-minor-001"))
	if err != nil {
		t.Fatal(err)
	}
	if up.Request.Kind != KindUpgrade || up.Request.FencingToken == nil || f.pendingPublishJobs(t, up.Request.ID) != 1 ||
		!strings.Contains(string(up.Request.Spec), `"change":"minor"`) {
		t.Fatalf("upgrade = %+v %s", up.Request, up.Request.Spec)
	}
	again, err := f.svc.Create(ctx, f.user, createCmd(in, "upgrade-minor-001"))
	if err != nil || !again.Replayed || again.Request.ID != up.Request.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	// While the minor is open, a major would copy its pending spec: stack_busy.
	major.AcknowledgeBreaking = true
	if _, err := f.svc.Create(ctx, f.user, createCmd(major, "upgrade-major-002")); !hasCode(err, "stack_busy") {
		t.Fatalf("major while the minor is open: %v", err)
	}
	// Once the minor is applied, the major starts from 1.4.0.
	for _, q := range []string{
		`UPDATE requests SET status = 'applied', approved_plan_hash = repeat('b', 64), completed_at = now() WHERE id = $1`,
		`DELETE FROM locks WHERE request_id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, q, up.Request.ID); err != nil {
			t.Fatal(err)
		}
	}
	next, err := f.svc.Create(ctx, f.user, createCmd(major, "upgrade-major-003"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.svc.Get(ctx, next.Request.ID)
	if err != nil || stored.QueuedBehind != nil ||
		!strings.Contains(string(stored.Spec), `"from_version": "1.4.0"`) || !strings.Contains(string(stored.Spec), `"version": "2.0.0"`) {
		t.Fatalf("major after minor = %+v %s, %v", stored, stored.Spec, err)
	}
	if acts := f.actions(t, up.Request.ID); len(acts) == 0 || acts[0] != "request.created" {
		t.Fatalf("audit = %v", acts)
	}
}
