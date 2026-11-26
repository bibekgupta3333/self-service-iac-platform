package budgets_test

import (
	"context"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/budgets"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

const seed = `
WITH bot AS (SELECT id FROM users WHERE github_login = 'iacp-bot'),
     tm AS (INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
            VALUES ('payments', 'Payments', 1, 'payments', 'cc-1'), ('search', 'Search', 2, 'search', 'cc-2')
            RETURNING id, slug),
     b AS (INSERT INTO budgets (team_id, environment_id, month, limit_usd, spent_usd, hard_block, created_by)
           SELECT tm.id, NULL, date '2026-11-01', 5000, 120.50, false, bot.id FROM tm, bot WHERE tm.slug = 'payments'
           UNION ALL
           SELECT tm.id, e.id, date '2026-11-01', 2000, 0, true, bot.id FROM tm, bot, environments e
           WHERE tm.slug = 'payments' AND e.name = 'development'
           UNION ALL
           SELECT tm.id, NULL, date '2026-10-01', 9, 0, true, bot.id FROM tm, bot WHERE tm.slug = 'search'
           RETURNING 1),
     m AS (INSERT INTO modules (name, display_name, description, category, owners, repo_path, status)
           VALUES ('s3', 'S3', 'bucket', 'storage', '{platform}', 'modules/s3', 'active'),
                  ('legacy', 'Legacy', 'old', 'misc', '{platform}', 'modules/legacy', 'yanked')
           RETURNING id, name)
INSERT INTO module_versions (module_id, version, major, minor, patch, git_tag, git_sha, manifest, inputs_schema,
                             outputs, tf_version_range, aws_provider_range, status, released_by)
SELECT m.id, v.version, v.major, v.minor, 0, 'modules/' || m.name || '/v' || v.version, repeat('a', 40),
       '{}', '{}', '[]', '>= 1.6', '>= 5.0', v.status::module_status, 'ci'
FROM m JOIN (VALUES ('s3', '1.0.0', 1, 0, 'active'), ('s3', '1.1.0', 1, 1, 'yanked'),
                    ('legacy', '0.1.0', 0, 1, 'active')) AS v(module, version, major, minor, status)
     ON v.module = m.name
`

func TestPostgresRepository(t *testing.T) {
	app, owner := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := budgets.NewPostgresRepository(app) // reads as iacp_app, like the API
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	env, err := repo.Environment(ctx, "development")
	if err != nil || len(env.AllowedRegions) != 1 || env.AllowedRegions[0] != "us-east-1" {
		t.Fatalf("environment = %+v, %v", env, err)
	}
	if _, err := repo.Environment(ctx, "production"); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("unseeded environment: %v", err)
	}

	b, err := repo.MonthlyBudget(ctx, "payments", "development", nov)
	if err != nil || b == nil || b.LimitUSD != 2000 || !b.HardBlock || b.Scope != budgets.ScopeEnvironment {
		t.Fatalf("environment budget wins: %+v, %v", b, err)
	}
	b, err = repo.MonthlyBudget(ctx, "payments", "staging", nov)
	if err != nil || b == nil || b.LimitUSD != 5000 || b.SpentUSD != 120.5 || b.HardBlock || b.Scope != budgets.ScopeTeam {
		t.Fatalf("team-wide fallback: %+v, %v", b, err)
	}
	for _, tc := range []struct{ team string }{{"search"}, {"unknown-team"}} {
		if b, err := repo.MonthlyBudget(ctx, tc.team, "development", nov); err != nil || b != nil {
			t.Fatalf("%s: no budget this month: %+v, %v", tc.team, b, err)
		}
	}

	yanked, err := repo.YankedVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []budgets.YankedVersion{{Module: "legacy", Version: "0.1.0"}, {Module: "s3", Version: "1.1.0"}}
	if len(yanked) != len(want) || yanked[0] != want[0] || yanked[1] != want[1] {
		t.Fatalf("yanked = %+v, want %+v", yanked, want)
	}

	doc, err := budgets.NewService(repo, platform.FixedClock{T: nov.Add(36 * time.Hour)}).
		Context(ctx, "environments/development/payments/order-events")
	if err != nil || doc.IACP.Context.Budget.LimitUSD != 2000 || len(doc.IACP.Context.YankedModuleVersions) != 2 {
		t.Fatalf("service = %+v, %v", doc, err)
	}
}
