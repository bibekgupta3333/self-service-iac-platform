package auth

import (
	"fmt"
	"slices"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// RouteClass groups machine routes that accept the same workflows (docs/03 §6.9).
type RouteClass string

// Route classes.
const (
	// RouteLocks: /v1/internal/stacks/{id}/lock*. Only the workflows that apply or
	// destroy under the stack lock, from main.
	RouteLocks RouteClass = "locks"
	// RouteCatalogIndex: /v1/internal/catalog/index. Only module-release.yml from main.
	RouteCatalogIndex RouteClass = "catalog_index"
	// RouteRuns: POST /v1/runs. Any pipeline workflow; runs.CheckProvenance then applies
	// the per-pipeline rules (workflow file, main ref, event).
	RouteRuns RouteClass = "runs"
	// RouteBudgetContext: GET /v1/internal/budget-context. Read-only policy input for
	// conftest, fetched by pr-validate.yml (pull_request) and apply.yml (main).
	RouteBudgetContext RouteClass = "budget_context"
)

// RoutePolicy is what an OIDC caller must be to use a route class.
type RoutePolicy struct {
	Workflows []string // allowed workflow file names (Workflow.File)
	MainOnly  bool     // ref and workflow definition must be refs/heads/main
	Events    []string // allowed event_name values
}

// RoutePolicies is the per-route allow-list. A pull-request token (pr-validate.yml on a
// refs/pull/* ref, whose workflow file the PR author controls) only reaches /v1/runs and
// the read-only budget context; it can never take locks or index the catalog.
var RoutePolicies = map[RouteClass]RoutePolicy{
	RouteLocks: {
		Workflows: []string{"apply.yml", "ttl-reaper.yml"},
		MainOnly:  true,
		Events:    []string{"push", "workflow_dispatch", "schedule"},
	},
	RouteCatalogIndex: {
		Workflows: []string{"module-release.yml"},
		MainOnly:  true,
		Events:    []string{"push", "workflow_dispatch"},
	},
	RouteRuns: {
		Workflows: []string{"pr-validate.yml", "apply.yml", "drift.yml", "ttl-reaper.yml"},
		Events:    []string{"pull_request", "push", "workflow_dispatch", "schedule"},
	},
	RouteBudgetContext: {
		Workflows: []string{"pr-validate.yml", "apply.yml"},
		Events:    []string{"pull_request", "push", "workflow_dispatch"},
	},
}

// AuthorizeRoute checks a caller's workflow identity against the route class policy.
// A nil workflow is a caller that used the shared internal token, which is not scoped
// to a workflow and is allowed. An unknown class denies. Pure.
func AuthorizeRoute(class RouteClass, wf *identity.Workflow) error {
	if wf == nil {
		return nil
	}
	p, ok := RoutePolicies[class]
	switch {
	case !ok:
		return workflowNotAllowed(fmt.Sprintf("route class %q accepts no workflow", class))
	case !slices.Contains(p.Workflows, wf.File()):
		return workflowNotAllowed(fmt.Sprintf("%s may not call %s routes", wf.JobWorkflowRef, class))
	case p.MainOnly && !wf.OnMain():
		return workflowNotAllowed(fmt.Sprintf("%s routes accept only workflows on %s", class, identity.MainRef))
	case !slices.Contains(p.Events, wf.EventName):
		return workflowNotAllowed(fmt.Sprintf("event %q may not call %s routes", wf.EventName, class))
	}
	return nil
}
