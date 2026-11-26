// Package budgets serves the OPA policy context for a stack (GET
// /v1/internal/budget-context): environment, allowed regions, the team's monthly
// budget and the yanked module versions. This file holds the pure parts: stack path
// parsing and building the conftest --data document.
package budgets

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

// StackRef is a stack root environments/<env>/<team>/<stack>.
type StackRef struct {
	Environment string
	Team        string
	Stack       string
}

// Path is the repository path of the stack root.
func (r StackRef) Path() string {
	return strings.Join([]string{"environments", r.Environment, r.Team, r.Stack}, "/")
}

var (
	environments = []string{"development", "staging", "production"}
	teamRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}$`) // teams.slug CHECK
	stackRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`) // stacks.name CHECK
)

// ParseStackPath validates a stack root path. A trailing slash is tolerated. Pure.
func ParseStackPath(path string) (StackRef, error) {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	invalid := func(why string) (StackRef, error) {
		return StackRef{}, apperr.Invalid("invalid_stack_path",
			fmt.Sprintf("stack_path %q %s; want environments/<environment>/<team>/<stack>", path, why))
	}
	switch {
	case len(parts) != 4 || parts[0] != "environments":
		return invalid("is not a stack root")
	case !slices.Contains(environments, parts[1]):
		return invalid("names an unknown environment")
	case !teamRe.MatchString(parts[2]):
		return invalid("has an invalid team slug")
	case !stackRe.MatchString(parts[3]):
		return invalid("has an invalid stack name")
	}
	return StackRef{Environment: parts[1], Team: parts[2], Stack: parts[3]}, nil
}

// MonthOf returns the first instant of t's month in UTC (budgets.month). Pure.
func MonthOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Environment is the environment row a stack deploys to.
type Environment struct {
	Name           string
	AllowedRegions []string
}

// Scope says whether a budget row is specific to the stack's environment or covers
// every environment of the team.
type Scope string

// Budget scopes.
const (
	ScopeEnvironment Scope = "environment"
	ScopeTeam        Scope = "team"
)

// Budget is the team's budget for the current month.
type Budget struct {
	Month     time.Time
	LimitUSD  float64
	SpentUSD  float64  // committed estimate; 0 until cost estimates are recorded
	ActualUSD *float64 // recorded actual spend (POST /v1/internal/budget-actuals); nil until recorded
	HardBlock bool
	Scope     Scope
	Override  *Override // an admin override, nil when none was granted
}

// Override is an admin's waiver of the budget's hard limit until ExpiresAt. The policy
// ignores it in production (docs/07 section 6.7).
type Override struct {
	GrantedBy string // GitHub login of the admin
	Reason    string
	ExpiresAt time.Time
}

// YankedVersion is a module version that must not be newly deployed.
type YankedVersion struct {
	Module  string
	Version string
}

// Tag is the git tag of the version (modules/<name>/vX.Y.Z).
func (y YankedVersion) Tag() string { return fmt.Sprintf("modules/%s/v%s", y.Module, y.Version) }

// Document is the conftest --data file: policies read it as data.iacp.context.
type Document struct {
	IACP struct {
		Context Context `json:"context"`
	} `json:"iacp"`
}

// Context is data.iacp.context (docs/07 §6.1).
type Context struct {
	Environment          string             `json:"environment"`
	Team                 string             `json:"team"`
	Stack                string             `json:"stack"`
	StackPath            string             `json:"stack_path"`
	AllowedRegions       []string           `json:"allowed_regions"`
	Budget               *BudgetDTO         `json:"budget"`
	BudgetOverride       *OverrideDTO       `json:"budget_override"` // null unless an admin granted one
	Exceptions           []ExceptionDTO     `json:"exceptions"`
	YankedModuleVersions []YankedVersionDTO `json:"yanked_module_versions"`
	Now                  string             `json:"now"`
}

// BudgetDTO is the budget as the policies see it.
type BudgetDTO struct {
	Month     string  `json:"month"` // YYYY-MM
	LimitUSD  float64 `json:"limit_usd"`
	SpentUSD  float64 `json:"spent_usd"`
	// ActualUSD is the recorded actual spend, null until one is recorded. The budget policy
	// counts the larger of spent_usd and actual_usd as already used.
	ActualUSD *float64 `json:"actual_usd"`
	HardBlock bool     `json:"hard_block"`
	Scope     Scope    `json:"scope"`
}

// OverrideDTO is the budget override as the policies see it.
type OverrideDTO struct {
	GrantedBy string `json:"granted_by"`
	Reason    string `json:"reason"`
	ExpiresAt string `json:"expires_at"` // RFC 3339, UTC
}

// ExceptionDTO is an approved policy exception (none are served until policy
// exceptions are stored, WBS 8.x; the list is always present so policies can rely on it).
type ExceptionDTO struct {
	RuleID          string  `json:"rule_id"`
	ResourceAddress *string `json:"resource_address"`
	ExpiresAt       string  `json:"expires_at"`
}

// YankedVersionDTO is one yanked module version.
type YankedVersionDTO struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Tag     string `json:"tag"`
}

// BuildContext assembles the policy document. budget may be nil (no budget set for the
// month). Output order is deterministic. Pure.
func BuildContext(ref StackRef, env Environment, budget *Budget, yanked []YankedVersion, now time.Time) Document {
	var d Document
	c := Context{
		Environment:          ref.Environment,
		Team:                 ref.Team,
		Stack:                ref.Stack,
		StackPath:            ref.Path(),
		AllowedRegions:       append([]string{}, env.AllowedRegions...),
		Exceptions:           []ExceptionDTO{},
		YankedModuleVersions: yankedDTOs(yanked),
		Now:                  now.UTC().Format(time.RFC3339),
	}
	if budget != nil {
		c.Budget = &BudgetDTO{Month: budget.Month.Format("2006-01"), LimitUSD: budget.LimitUSD,
			SpentUSD: budget.SpentUSD, ActualUSD: budget.ActualUSD, HardBlock: budget.HardBlock, Scope: budget.Scope}
		if o := budget.Override; o != nil {
			c.BudgetOverride = &OverrideDTO{GrantedBy: o.GrantedBy, Reason: o.Reason,
				ExpiresAt: o.ExpiresAt.UTC().Format(time.RFC3339)}
		}
	}
	d.IACP.Context = c
	return d
}

func yankedDTOs(ys []YankedVersion) []YankedVersionDTO {
	out := make([]YankedVersionDTO, 0, len(ys))
	for _, y := range ys {
		out = append(out, YankedVersionDTO{Module: y.Module, Version: y.Version, Tag: y.Tag()})
	}
	slices.SortFunc(out, func(a, b YankedVersionDTO) int { return strings.Compare(a.Tag, b.Tag) })
	return slices.Compact(out)
}
