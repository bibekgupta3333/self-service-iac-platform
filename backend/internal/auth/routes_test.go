package auth

import (
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

func wfFor(file, ref, event string) *identity.Workflow {
	return &identity.Workflow{Repository: testRepo, Ref: ref, EventName: event,
		JobWorkflowRef: testRepo + "/.github/workflows/" + file + "@" + ref, RunID: 1, RunAttempt: 1}
}

func TestAuthorizeRoute(t *testing.T) {
	const main, pr = "refs/heads/main", "refs/pull/812/merge"
	tests := []struct {
		name  string
		class RouteClass
		wf    *identity.Workflow
		ok    bool
	}{
		{"shared token", RouteLocks, nil, true},
		{"apply on main may lock", RouteLocks, wfFor("apply.yml", main, "push"), true},
		{"ttl reaper on schedule may lock", RouteLocks, wfFor("ttl-reaper.yml", main, "schedule"), true},
		{"pr-validate may not lock", RouteLocks, wfFor("pr-validate.yml", pr, "pull_request"), false},
		{"pr-validate on main may not lock", RouteLocks, wfFor("pr-validate.yml", main, "push"), false},
		{"apply from a PR ref may not lock", RouteLocks, wfFor("apply.yml", pr, "pull_request"), false},
		{"apply via workflow_run may not lock", RouteLocks, wfFor("apply.yml", main, "workflow_run"), false},
		{"apply via pull_request_target may not lock", RouteLocks, wfFor("apply.yml", main, "pull_request_target"), false},
		{"module release may index", RouteCatalogIndex, wfFor("module-release.yml", main, "push"), true},
		{"apply may not index", RouteCatalogIndex, wfFor("apply.yml", main, "push"), false},
		{"pr-validate may not index", RouteCatalogIndex, wfFor("pr-validate.yml", pr, "pull_request"), false},
		{"pr-validate may report runs", RouteRuns, wfFor("pr-validate.yml", pr, "pull_request"), true},
		{"module release may not report runs", RouteRuns, wfFor("module-release.yml", main, "push"), false},
		{"pr-validate may read the budget context", RouteBudgetContext, wfFor("pr-validate.yml", pr, "pull_request"), true},
		{"apply may read the budget context", RouteBudgetContext, wfFor("apply.yml", main, "push"), true},
		{"shared token may read the budget context", RouteBudgetContext, nil, true},
		{"drift may not read the budget context", RouteBudgetContext, wfFor("drift.yml", main, "schedule"), false},
		{"module release may not read the budget context", RouteBudgetContext, wfFor("module-release.yml", main, "push"), false},
		{"budget context refuses workflow_run", RouteBudgetContext, wfFor("apply.yml", main, "workflow_run"), false},
		{"apply on main may look up the request", RouteRequestLookup, wfFor("apply.yml", main, "push"), true},
		{"apply dispatch on main may look up the request", RouteRequestLookup, wfFor("apply.yml", main, "workflow_dispatch"), true},
		{"shared token may look up the request", RouteRequestLookup, nil, true},
		{"pr-validate may not look up the request", RouteRequestLookup, wfFor("pr-validate.yml", pr, "pull_request"), false},
		{"pr-validate on main may not look up the request", RouteRequestLookup, wfFor("pr-validate.yml", main, "push"), false},
		{"apply from a PR ref may not look up the request", RouteRequestLookup, wfFor("apply.yml", pr, "pull_request"), false},
		{"apply on a schedule may not look up the request", RouteRequestLookup, wfFor("apply.yml", main, "schedule"), false},
		{"ttl reaper may not look up the request", RouteRequestLookup, wfFor("ttl-reaper.yml", main, "schedule"), false},
		{"ttl reaper on schedule may use the ttl routes", RouteTTL, wfFor("ttl-reaper.yml", main, "schedule"), true},
		{"ttl reaper dispatched on main may use the ttl routes", RouteTTL, wfFor("ttl-reaper.yml", main, "workflow_dispatch"), true},
		{"shared token may use the ttl routes", RouteTTL, nil, true},
		{"ttl reaper from a PR ref may not", RouteTTL, wfFor("ttl-reaper.yml", pr, "pull_request"), false},
		{"ttl reaper definition from a branch may not", RouteTTL, wfFor("ttl-reaper.yml", "refs/heads/feature", "workflow_dispatch"), false},
		{"ttl reaper on push may not", RouteTTL, wfFor("ttl-reaper.yml", main, "push"), false},
		{"ttl reaper via workflow_run may not", RouteTTL, wfFor("ttl-reaper.yml", main, "workflow_run"), false},
		{"apply may not use the ttl routes", RouteTTL, wfFor("apply.yml", main, "push"), false},
		{"drift may not use the ttl routes", RouteTTL, wfFor("drift.yml", main, "schedule"), false},
		{"pr-validate may not use the ttl routes", RouteTTL, wfFor("pr-validate.yml", pr, "pull_request"), false},
		{"unknown class denies", RouteClass("other"), wfFor("apply.yml", main, "push"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeRoute(tt.class, tt.wf)
			if tt.ok != (err == nil) {
				t.Fatalf("AuthorizeRoute = %v", err)
			}
			if err != nil {
				if e, ok := apperr.As(err); !ok || e.Code != "workflow_not_allowed" || e.Kind != apperr.KindForbidden {
					t.Fatalf("want workflow_not_allowed, got %v", err)
				}
			}
		})
	}
}
