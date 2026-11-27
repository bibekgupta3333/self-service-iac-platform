package recovery

import (
	"context"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestPostgresSurgeryAudit(t *testing.T) {
	pool, _, stackID, _ := seedApplying(t)
	ctx := context.Background()
	w := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	rec := NewSurgeryRecorder(NewPostgresRepository(pool, w), platform.SystemClock{})
	r := surgeryReport()
	r.StackPath = itStackPath
	bot := identity.Principal{Login: "acme/iacp/.github/workflows/state-surgery.yml@refs/heads/main", Type: identity.ActorGitHubActions,
		Workflow: gateWorkflow()}

	out, err := rec.Record(ctx, bot, r)
	if err != nil || out.StackID == nil || *out.StackID != stackID || out.Duplicate {
		t.Fatalf("%+v, %v", out, err)
	}
	if again, err := rec.Record(ctx, bot, r); err != nil || !again.Duplicate {
		t.Fatalf("again %+v, %v", again, err)
	}
	var n int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'state.surgery' AND subject_id = $1
		AND payload->>'operation' = 'state mv' AND payload->>'ticket' = 'INC-1234'`, stackID.String()).Scan(&n))
	if n != 1 {
		t.Fatalf("%d state.surgery events", n)
	}
	// The chain still verifies with the new event in it.
	res, err := audit.NewVerifier(pool).Verify(ctx, 0, 0)
	if err != nil || !res.Valid {
		t.Fatalf("verify %+v, %v", res, err)
	}
}
