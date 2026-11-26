package budgets_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

const adminSeed = `
WITH tm AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
            VALUES ('payments', 'Payments', 1, 'payments', 'cc-1') RETURNING id),
     u AS (INSERT INTO users (github_user_id, github_login, is_platform_admin)
           VALUES (11, 'ada', false), (12, 'bob', false), (13, 'root', true), (14, 'gone', false) RETURNING id, github_login)
INSERT INTO memberships (team_id, user_id, role)
SELECT tm.id, u.id, CASE u.github_login WHEN 'ada' THEN 'admin'::team_role ELSE 'developer'::team_role END
FROM tm, u WHERE u.github_login IN ('ada', 'bob', 'gone');
UPDATE users SET disabled_at = now() WHERE github_login = 'gone';
`

func fp(v float64) *float64 { return &v }

// fixedIDs hands out one id forever: the audit chain's primary key then refuses the second event,
// which is how the rollback test makes the audit write fail.
type fixedIDs struct{ id uuid.UUID }

func (f fixedIDs) NewID() uuid.UUID                { return f.id }
func (f fixedIDs) NewShortID(prefix string) string { return prefix + "-fixed" }

const stackSeed = `
INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, status, created_by, destroyed_at)
SELECT t.id, e.id, v.name, 'environments/development/payments/' || v.name, 'development/payments/' || v.name,
       'us-east-1', v.status::stack_status, u.id, CASE WHEN v.status = 'destroyed' THEN now() END
FROM teams t, environments e, users u,
     (VALUES ('alpha', 'active'), ('beta', 'active'), ('gamma', 'destroyed')) AS v(name, status)
WHERE t.slug = 'payments' AND e.name = 'development' AND u.github_login = 'ada'
`

func TestPostgresAdminRepository(t *testing.T) {
	app, owner := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, adminSeed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	userID := func(login string) uuid.UUID {
		var id uuid.UUID
		if err := owner.QueryRow(ctx, `SELECT id FROM users WHERE github_login = $1`, login).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	clock := platform.FixedClock{T: nov.Add(10 * 24 * time.Hour)}
	repo := budgets.NewPostgresAdminRepository(app, audit.NewWriter(clock, platform.RandomIDs{}), clock)
	ada := identity.Principal{UserID: userID("ada"), Login: "ada", Type: identity.ActorUser}

	// Roles: team admin, plain developer, platform admin (admin everywhere), disabled user.
	for login, want := range map[string]rbac.Role{"ada": rbac.RoleAdmin, "bob": rbac.RoleDeveloper, "root": rbac.RoleAdmin, "gone": rbac.RoleNone} {
		if got, err := repo.RoleInTeam(ctx, "payments", userID(login)); err != nil || got != want {
			t.Errorf("%s role = %q, %v; want %q", login, got, err, want)
		}
	}
	if _, err := repo.RoleInTeam(ctx, "ghost", ada.UserID); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("unknown team: %v", err)
	}

	// Create, then replace, then clear the override.
	in := func(limit float64, env string, override *budgets.OverrideInput) budgets.SetInput {
		in := budgets.SetInput{LimitUSD: &limit, Override: override}
		if env != "" {
			in.Environment = &env
		}
		return in
	}
	write := func(month string, si budgets.SetInput) budgets.Change {
		w, err := budgets.NewWrite("payments", month, si, clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		ch, err := repo.Set(ctx, w, ada, "req-1")
		if err != nil {
			t.Fatalf("Set: %v", err)
		}
		return ch
	}

	ch := write("2026-11", in(2000.25, "", nil))
	if ch.Before != nil || ch.After.LimitUSD != 2000.25 || ch.After.Environment != "" || !ch.After.HardBlock ||
		len(ch.After.AlertThresholds) != 3 || ch.After.UpdatedBy != "ada" || ch.After.Override != nil {
		t.Fatalf("create = %+v", ch)
	}
	exp := clock.Now().Add(48 * time.Hour)
	if ch.After.SingleChangeCapUSD != 5000 {
		t.Fatalf("default single-change cap = %v", ch.After.SingleChangeCapUSD)
	}
	override := func(max float64) *budgets.OverrideInput {
		return &budgets.OverrideInput{Reason: "launch week capacity ok", ExpiresAt: exp, MaxDeltaUSD: fp(max)}
	}
	ch = write("2026-11", in(3000, "", override(750)))
	if ch.Before == nil || ch.Before.LimitUSD != 2000.25 || ch.After.ID != ch.Before.ID || ch.After.LimitUSD != 3000 ||
		ch.After.Override == nil || ch.After.Override.GrantedBy != "ada" || !ch.After.Override.ExpiresAt.Equal(exp) ||
		ch.After.Override.MaxDeltaUSD != 750 {
		t.Fatalf("replace with override = %+v", ch)
	}
	// Sending the same override again, as another admin, keeps the original grantor and grants nothing new;
	// changing it makes the new admin the grantor.
	bob := identity.Principal{UserID: userID("bob"), Login: "bob", Type: identity.ActorUser}
	w2, err := budgets.NewWrite("payments", "2026-11", in(3000, "", override(750)), clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ch, err = repo.Set(ctx, w2, bob, ""); err != nil || ch.After.Override == nil || ch.After.Override.GrantedBy != "ada" ||
		ch.After.UpdatedBy != "bob" {
		t.Fatalf("re-sent override = %+v, %v", ch.After, err)
	}
	w2, _ = budgets.NewWrite("payments", "2026-11", in(3000, "", override(900)), clock.Now())
	if ch, err = repo.Set(ctx, w2, bob, ""); err != nil || ch.After.Override.GrantedBy != "bob" || ch.After.Override.MaxDeltaUSD != 900 {
		t.Fatalf("changed override = %+v, %v", ch.After, err)
	}
	ch = write("2026-11", in(3000, "", nil))
	if ch.After.Override != nil {
		t.Fatalf("an omitted override must clear it: %+v", ch.After)
	}
	ca := 1234.5
	ch = write("2026-11", budgets.SetInput{LimitUSD: fp(3000), SingleChangeCapUSD: &ca})
	if ch.After.SingleChangeCapUSD != 1234.5 {
		t.Fatalf("single-change cap = %v", ch.After.SingleChangeCapUSD)
	}
	env := write("2026-11", in(500, "development", nil)) // a second, environment-specific row
	if env.Before != nil || env.After.Environment != "development" {
		t.Fatalf("environment budget = %+v", env)
	}

	// The policy context reads what the admin API wrote, as iacp_app.
	ctxRepo := budgets.NewPostgresRepository(app)
	b, err := ctxRepo.MonthlyBudget(ctx, "payments", "development", nov)
	if err != nil || b == nil || b.LimitUSD != 500 || b.Scope != budgets.ScopeEnvironment {
		t.Fatalf("context budget = %+v, %v", b, err)
	}

	// Unknown environment and team.
	w, _ := budgets.NewWrite("payments", "2026-11", in(1, "", nil), clock.Now())
	w.Team = "ghost"
	if _, err := repo.Set(ctx, w, ada, ""); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("unknown team: %v", err)
	}
	w, _ = budgets.NewWrite("payments", "2026-11", in(1, "production", nil), clock.Now())
	if _, err := repo.Set(ctx, w, ada, ""); !apperr.IsKind(err, apperr.KindInvalid) {
		t.Errorf("unconfigured environment: %v", err)
	}

	// Listing: newest month first, team-wide before environment rows.
	write("2026-12", in(10, "", nil))
	rows, err := repo.List(ctx, "payments", nil)
	if err != nil || len(rows) != 3 || rows[0].Month.Format("2006-01") != "2026-12" || rows[1].Environment != "" || rows[2].Environment != "development" {
		t.Fatalf("list = %+v, %v", rows, err)
	}
	dec := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if rows, err := repo.List(ctx, "payments", &dec); err != nil || len(rows) != 1 {
		t.Fatalf("list month = %+v, %v", rows, err)
	}
	if _, err := repo.List(ctx, "ghost", nil); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("list unknown team: %v", err)
	}

	// Actuals update an existing budget only, and count toward utilisation.
	bot := identity.Principal{Login: "internal-token", Type: identity.ActorSystem}
	actual := 1500.5
	aw, err := budgets.NewActualWrite(budgets.ActualInput{Team: "payments", Month: "2026-11", ActualUSD: &actual}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	s, err := repo.RecordActual(ctx, aw, bot, "")
	if err != nil || s.ActualUSD == nil || *s.ActualUSD != 1500.5 || s.UsedUSD() != 1500.5 || s.Utilisation() != 50.02 {
		t.Fatalf("actual = %+v, %v", s, err)
	}
	aw.Month = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := repo.RecordActual(ctx, aw, bot, ""); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("actual without a budget: %v", err)
	}
	b, _ = ctxRepo.MonthlyBudget(ctx, "payments", "staging", nov)
	if b == nil || b.ActualUSD == nil || *b.ActualUSD != 1500.5 {
		t.Fatalf("context actual = %+v", b)
	}

	// Every write is audited, in order, within the chain, tagged with the team.
	rowsAudit, err := owner.Query(ctx, `SELECT action, actor_id, team_id IS NOT NULL FROM audit_events
		WHERE subject_type = 'budget' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rowsAudit.Close()
	var actions []string
	for rowsAudit.Next() {
		var action, actor string
		var hasTeam bool
		if err := rowsAudit.Scan(&action, &actor, &hasTeam); err != nil || !hasTeam {
			t.Fatalf("audit row: %v %v", err, hasTeam)
		}
		actions = append(actions, action)
	}
	want := []string{"budget.set", "budget.set", "budget.override.granted", "budget.set", "budget.set", "budget.override.granted",
		"budget.set", "budget.override.revoked", "budget.set", "budget.set", "budget.set", "budget.actual.recorded"}
	if len(actions) != len(want) {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("audit actions = %v, want %v", actions, want)
		}
	}

	// Committed cost: recorded per active stack, summed for the team without the stack being changed,
	// and a destroyed stack counts for nothing.
	if _, err := owner.Exec(ctx, stackSeed); err != nil {
		t.Fatalf("seed stacks: %v", err)
	}
	record := func(path string, usd float64) error {
		c, err := budgets.NewCommittedWrite(budgets.CommittedInput{StackPath: path, MonthlyUSD: &usd})
		if err != nil {
			t.Fatal(err)
		}
		_, err = repo.RecordCommitted(ctx, c, bot, "")
		return err
	}
	if err := record("environments/development/payments/alpha", 100.5); err != nil {
		t.Fatal(err)
	}
	if err := record("environments/development/payments/beta", 200); err != nil {
		t.Fatal(err)
	}
	if err := record("environments/development/payments/gamma", 999); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("a destroyed stack takes no committed cost: %v", err)
	}
	if err := record("environments/development/payments/ghost", 1); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Errorf("an unknown stack: %v", err)
	}
	if _, err := owner.Exec(ctx, `UPDATE stacks SET committed_monthly_usd = 999 WHERE name = 'gamma'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		env, exclude string
		want         float64
	}{
		{"", "environments/development/payments/alpha", 200},       // team-wide, without the stack being changed
		{"development", "", 300.5},                                 // one environment, every stack
		{"staging", "", 0},                                         // another environment has none
		{"", "environments/development/payments/new-stack", 300.5}, // a stack not yet created excludes nothing
	} {
		if got, err := ctxRepo.CommittedUSD(ctx, "payments", tc.env, tc.exclude); err != nil || got != tc.want {
			t.Errorf("CommittedUSD(%q, %q) = %v, %v; want %v", tc.env, tc.exclude, got, err, tc.want)
		}
	}
	if got, err := ctxRepo.CommittedUSD(ctx, "ghost", "", ""); err != nil || got != 0 {
		t.Errorf("unknown team = %v, %v", got, err)
	}

	// A failing audit append rolls the write back with it: no budget row, no committed cost.
	var firstAudit uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT id FROM audit_events ORDER BY seq LIMIT 1`).Scan(&firstAudit); err != nil {
		t.Fatal(err)
	}
	broken := budgets.NewPostgresAdminRepository(app, audit.NewWriter(clock, fixedIDs{firstAudit}), clock)
	wf, err := budgets.NewWrite("payments", "2027-01", in(77, "", nil), clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Set(ctx, wf, ada, ""); err == nil {
		t.Fatal("Set succeeded although its audit event could not be written")
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM budgets WHERE month = date '2027-01-01'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the budget write survived a failed audit append: %d rows, %v", n, err)
	}
	// An existing row is not changed by a failed update either.
	wf, _ = budgets.NewWrite("payments", "2026-12", in(55, "", nil), clock.Now())
	if _, err := broken.Set(ctx, wf, ada, ""); err == nil {
		t.Fatal("update succeeded although its audit event could not be written")
	}
	var limit float64
	if err := owner.QueryRow(ctx, `SELECT limit_usd::float8 FROM budgets WHERE month = date '2026-12-01'`).Scan(&limit); err != nil || limit != 10 {
		t.Fatalf("the update survived a failed audit append: limit %v, %v", limit, err)
	}
	cw, _ := budgets.NewCommittedWrite(budgets.CommittedInput{StackPath: "environments/development/payments/alpha", MonthlyUSD: fp(5)})
	if _, err := broken.RecordCommitted(ctx, cw, bot, ""); err == nil {
		t.Fatal("RecordCommitted succeeded although its audit event could not be written")
	}
	if got, _ := ctxRepo.CommittedUSD(ctx, "payments", "development", ""); got != 300.5 {
		t.Fatalf("committed cost changed by a failed audit append: %v", got)
	}
	aw2, _ := budgets.NewActualWrite(budgets.ActualInput{Team: "payments", Month: "2026-11", ActualUSD: fp(1)}, clock.Now())
	if _, err := broken.RecordActual(ctx, aw2, bot, ""); err == nil {
		t.Fatal("RecordActual succeeded although its audit event could not be written")
	}
	if s, err := repo.List(ctx, "payments", &nov); err != nil || s[0].ActualUSD == nil || *s[0].ActualUSD != 1500.5 {
		t.Fatalf("actual changed by a failed audit append: %+v, %v", s, err)
	}

	// iacp_app cannot rewrite or delete audit rows, so a budget change cannot be hidden.
	if _, err := app.Exec(ctx, `DELETE FROM audit_events WHERE subject_type = 'budget'`); err == nil {
		t.Error("iacp_app deleted audit rows")
	}

	// The constraints reject what the validation would (defence in depth).
	bad := []string{
		`UPDATE budgets SET limit_usd = 1000000.01`,
		`UPDATE budgets SET actual_usd = -1`,
		`UPDATE budgets SET override_by = (SELECT id FROM users WHERE github_login = 'ada')`, // no reason or expiry
		`UPDATE budgets SET alert_thresholds = '{0}'`,
		`UPDATE budgets SET alert_thresholds = '{201}'`,
		`UPDATE budgets SET single_change_cap_usd = 1000000.01`,
		`UPDATE budgets SET single_change_cap_usd = -1`,
		// an override needs its reason, expiry and bound together, and a positive bound
		`UPDATE budgets SET override_by = (SELECT id FROM users WHERE github_login = 'ada'), override_reason = repeat('x', 20),
		        override_expires_at = now()`,
		`UPDATE budgets SET override_by = (SELECT id FROM users WHERE github_login = 'ada'), override_reason = repeat('x', 20),
		        override_expires_at = now(), override_max_delta_usd = 0`,
		`UPDATE stacks SET committed_monthly_usd = -1`,
	}
	for _, q := range bad {
		if _, err := owner.Exec(ctx, q); err == nil {
			t.Errorf("accepted: %s", q)
		}
	}
}
