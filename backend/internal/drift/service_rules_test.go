package drift

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func TestRecordRefusesAStackAnOpenRequestHolds(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	f.repo.stack.Locked = true
	if _, err := f.svc.Record(context.Background(), f.bot, validReport()); !hasCode(err, "stack_not_checkable") {
		t.Fatalf("err = %v", err)
	}
	if len(f.repo.reports) != 0 || f.repo.stack.Health != requests.HealthUnknown {
		t.Fatal("a refused report changes nothing")
	}
}

func TestCleanScanNeverUpgradesUnknownOrDegraded(t *testing.T) {
	for _, start := range []requests.StackHealth{requests.HealthUnknown, requests.HealthHealthy, requests.HealthDegraded} {
		f := newFixture(t, rbac.RoleDeveloper)
		f.repo.stack.Health = start
		clean := Report{StackPath: testPath, GitHub: validReport().GitHub, Result: ResultClean, TerraformExitCode: ExitClean}
		if _, err := f.svc.Record(context.Background(), f.bot, clean); err != nil {
			t.Fatal(err)
		}
		if f.repo.stack.Health != start {
			t.Errorf("a clean scan moved %s to %s", start, f.repo.stack.Health)
		}
	}
	f := newFixture(t, rbac.RoleDeveloper)
	f.repo.stack.Health = requests.HealthDegraded
	if _, err := f.svc.Record(context.Background(), f.bot, validReport()); err != nil {
		t.Fatal(err)
	}
	if f.repo.stack.Health != requests.HealthDrifted {
		t.Fatalf("a drift finding sets drifted, got %s", f.repo.stack.Health)
	}
}

func TestErrorTextIsSummarisedAndKeptOutOfTheAuditLog(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	r := Report{StackPath: testPath, GitHub: validReport().GitHub, Result: ResultError, TerraformExitCode: ExitError,
		Error: boxedLog + "\nError: creating bucket password=hunter2"}
	res, err := f.svc.Record(context.Background(), f.bot, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.Error != "Error: reading SSM Parameter (/db/password): operation error SSM: GetParameter, api error ValidationException: bad request" {
		t.Fatalf("stored error = %q", res.Entry.Error)
	}
	for _, e := range f.repo.audits {
		if strings.Contains(string(e.Payload), "SSM") || strings.Contains(string(e.Payload), "hunter2") {
			t.Fatalf("the audit payload carries terraform text: %s", e.Payload)
		}
	}
}

func TestAnIgnoredFindingIsNotReopenedByTheSameDifference(t *testing.T) {
	f := newFixture(t, rbac.RoleMaintainer)
	id := seedOpen(t, f)
	if _, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionIgnore, Reason: "known provider noise"}); err != nil {
		t.Fatal(err)
	}
	f.bot.Workflow.RunID = 920
	again := validReport()
	again.GitHub.RunID = 920
	res, err := f.svc.Record(context.Background(), f.bot, again)
	if err != nil || !res.Suppressed || res.Entry.ID == id {
		t.Fatalf("same difference = %+v, %v", res, err)
	}
	// It is stored as an ignored copy (visible in the history, counted as the newest run),
	// not as an open report.
	if st := res.Entry.Status; len(f.repo.reports) != 2 || st == nil || *st != StatusIgnored || res.Entry.ResolutionNote != "known provider noise" ||
		f.repo.stack.Health != requests.HealthDrifted {
		t.Fatalf("suppressed scan stored as %+v, %d reports, health %s", res.Entry, len(f.repo.reports), f.repo.stack.Health)
	}
	for _, r := range f.repo.reports {
		if *r.Status == StatusOpen {
			t.Fatal("an ignored finding must not open a report")
		}
	}

	// A different difference is a new finding.
	f.bot.Workflow.RunID = 921
	other := validReport()
	other.GitHub.RunID = 921
	other.PlanHash = strings.Repeat("d", 64)
	res, err = f.svc.Record(context.Background(), f.bot, other)
	if err != nil || res.Suppressed || res.Entry.Status == nil || *res.Entry.Status != StatusOpen || len(f.repo.reports) != 3 {
		t.Fatalf("different difference = %+v, %v", res, err)
	}
}

func TestAnIgnoredFindingThatClearsAndReturnsIsNew(t *testing.T) {
	f := newFixture(t, rbac.RoleMaintainer)
	id := seedOpen(t, f)
	if _, err := f.svc.Resolve(context.Background(), f.user, id, ResolveInput{Action: ActionIgnore, Reason: "known provider noise"}); err != nil {
		t.Fatal(err)
	}
	f.bot.Workflow.RunID = 930
	clean := Report{StackPath: testPath, GitHub: validReport().GitHub, Result: ResultClean, TerraformExitCode: ExitClean}
	clean.GitHub.RunID = 930
	if _, err := f.svc.Record(context.Background(), f.bot, clean); err != nil {
		t.Fatal(err)
	}
	f.bot.Workflow.RunID = 931
	back := validReport()
	back.GitHub.RunID = 931
	res, err := f.svc.Record(context.Background(), f.bot, back)
	if err != nil || res.Suppressed || res.Entry.Status == nil || *res.Entry.Status != StatusOpen {
		t.Fatalf("the finding came back after a clean scan: %+v, %v", res, err)
	}
}

func TestResolveUpdatesOnlyTheReportsItLocked(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	first := seedOpen(t, f)
	f.bot.Workflow.RunID = 940
	r := validReport()
	r.GitHub.RunID = 940
	r.PlanHash = strings.Repeat("e", 64)
	second, err := f.svc.Record(context.Background(), f.bot, r)
	if err != nil {
		t.Fatal(err)
	}
	res := Resolution{ReportID: first, IDs: []uuid.UUID{first}, Status: StatusCodified, At: f.svc.clock.Now()}
	if n, err := f.repo.Resolve(context.Background(), res); err != nil || n != 1 {
		t.Fatalf("Resolve = %d, %v", n, err)
	}
	if st := f.repo.reports[1].Status; second.Entry.ID != f.repo.reports[1].ID || *st != StatusOpen {
		t.Fatal("a report that was not locked with the target must stay open for its own decision")
	}
}

func TestAStaleReportMovesNothing(t *testing.T) {
	f := newFixture(t, rbac.RoleDeveloper)
	// A newer run (950) found drift.
	f.bot.Workflow.RunID = 950
	newer := validReport()
	newer.GitHub.RunID = 950
	if _, err := f.svc.Record(context.Background(), f.bot, newer); err != nil {
		t.Fatal(err)
	}
	// A delayed report of an older run (900) says clean: it is kept, but changes nothing.
	f.bot.Workflow.RunID = 900
	clean := Report{StackPath: testPath, GitHub: validReport().GitHub, Result: ResultClean, TerraformExitCode: ExitClean}
	res, err := f.svc.Record(context.Background(), f.bot, clean)
	if err != nil || res.Entry.Result != ResultClean {
		t.Fatalf("stale clean = %+v, %v", res, err)
	}
	if f.repo.stack.Health != requests.HealthDrifted {
		t.Fatalf("a stale clean report restored health to %s", f.repo.stack.Health)
	}
	if st := f.repo.reports[0].Status; st == nil || *st != StatusOpen {
		t.Fatal("a stale clean report must not close the newer open report")
	}
}
