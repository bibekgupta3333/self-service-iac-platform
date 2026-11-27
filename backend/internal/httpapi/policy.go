package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ratelimit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Scope says what kind of role a route policy checks.
type Scope int

const (
	// ScopePlatform requires a platform admin (platform-wide actions with no team).
	ScopePlatform Scope = iota + 1
	// ScopeAnyTeam requires a role in at least one team that allows the action.
	ScopeAnyTeam
	// ScopeAuthenticated allows any authenticated user. It is explicit, so that "no check
	// needed" is a decision recorded in the table rather than the absence of an entry.
	ScopeAuthenticated
)

// RoutePolicy is the RBAC requirement of one route.
type RoutePolicy struct {
	Action rbac.Action
	Scope  Scope
}

// PolicyTable maps "METHOD /v1/route/{pattern}" (the chi pattern) to its policy. A user route
// without an entry is refused with 403 and fails TestEveryUserRouteHasAPolicy, so every new
// route must add itself. Team- and environment-scoped checks happen in the services, which
// call rbac.Decide with the caller's role in the target team.
type PolicyTable map[string]RoutePolicy

// DefaultPolicies covers the user routes that exist today. New routes must add themselves.
func DefaultPolicies() PolicyTable {
	return PolicyTable{
		"GET /v1/audit/verify":  {Action: rbac.ActionAuditViewAll, Scope: ScopePlatform},
		"GET /v1/requests":      {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		"GET /v1/requests/{id}": {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		// The status stream of one request: read, limited to the request's team by the service.
		"GET /v1/requests/{id}/events": {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		// Stacks of the caller's teams; the queries are scoped by identity.Visibility.
		"GET /v1/stacks":      {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		"GET /v1/stacks/{id}": {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		// Drift history of one visible stack. Resolving needs the developer role at least
		// (reconcile, codify); the service raises it to maintainer for ignore and checks the
		// caller's role in the report's team (docs/07 matrix).
		"GET /v1/stacks/{id}/drift":   {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		"POST /v1/drift/{id}/resolve": {Action: rbac.ActionDriftReconcile, Scope: ScopeAnyTeam},
		// Extending an ephemeral stack's TTL needs the stack.extend_ttl role (developer) in the
		// stack's team; the service checks it against the stack's team and environment and
		// answers 404 for a stack of another team.
		"POST /v1/stacks/{id}/ttl/extend": {Action: rbac.ActionTTLExtend, Scope: ScopeAnyTeam},
		// The catalog is shared reference data (docs/03: "any").
		"GET /v1/catalog/modules":                           {Action: rbac.ActionRead, Scope: ScopeAuthenticated},
		"GET /v1/catalog/modules/{name}":                    {Action: rbac.ActionRead, Scope: ScopeAuthenticated},
		"GET /v1/catalog/modules/{name}/versions":           {Action: rbac.ActionRead, Scope: ScopeAuthenticated},
		"GET /v1/catalog/modules/{name}/versions/{version}": {Action: rbac.ActionRead, Scope: ScopeAuthenticated},
		// POST /v1/requests is checked in the service against the caller's role in the
		// target team (rbac.Decide); the entry only records that the decision was made.
		"POST /v1/requests": {Action: rbac.ActionRequestCreate, Scope: ScopeAuthenticated},
		// The caller's own profile and session.
		"GET /v1/me": {Action: rbac.ActionRead, Scope: ScopeAuthenticated},
		// Team budgets are admin-only (docs/07 matrix). The route policy requires the admin role
		// in some team; BudgetAdminService then requires it in the team named in the path.
		"GET /v1/teams/{slug}/budgets":         {Action: rbac.ActionBudgetSet, Scope: ScopeAnyTeam},
		"GET /v1/teams/{slug}/budgets/{month}": {Action: rbac.ActionBudgetSet, Scope: ScopeAnyTeam},
		"PUT /v1/teams/{slug}/budgets/{month}": {Action: rbac.ActionBudgetSet, Scope: ScopeAnyTeam},
	}
}

// DenialAuditPerMinute bounds rbac.denied audit events per user per minute; further denials
// are counted and reported by the user's next audited one (payload suppressed_before).
const DenialAuditPerMinute = 20

// RoleLookup resolves the caller's most privileged team role (identity.UserStore).
type RoleLookup interface {
	HighestRole(ctx context.Context, userID uuid.UUID) (rbac.Role, error)
}

// authorize enforces the PolicyTable. It runs after routing, so the chi pattern is known.
func authorize(table PolicyTable, roles RoleLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pol, ok := table[r.Method+" "+routePattern(r)]
			if !ok {
				// Fail closed: a route nobody wrote a policy for is refused, and the
				// route-coverage test fails at build time.
				observability.Logger(r.Context()).ErrorContext(r.Context(), "route has no access policy",
					"route", r.Method+" "+routePattern(r))
				writeError(w, r, apperr.Forbidden("this route has no access policy"))
				return
			}
			p, err := identity.FromContext(r.Context())
			if err == nil {
				err = checkPolicy(r.Context(), pol, p, roles)
			}
			if err != nil {
				writeError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func checkPolicy(ctx context.Context, pol RoutePolicy, p identity.Principal, roles RoleLookup) error {
	if pol.Scope == ScopeAuthenticated {
		return nil
	}
	role := rbac.RoleNone
	switch {
	case p.PlatformAdmin:
		role = rbac.RoleAdmin
	case pol.Scope == ScopeAnyTeam:
		var err error
		if role, err = roles.HighestRole(ctx, p.UserID); err != nil {
			return &apperr.Error{Kind: apperr.KindUnavailable, Code: "rbac_unavailable", Message: "role lookup failed"}
		}
	}
	d := rbac.Decide(role, pol.Action, rbac.Resource{Environment: rbac.EnvDevelopment})
	if d.Allowed {
		return nil
	}
	return apperr.Forbidden(fmt.Sprintf("%s requires the %s role", pol.Action, d.Need))
}

func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return r.URL.Path
}

// auditDenials records an rbac.denied audit event for every 403 an authenticated user
// receives from the handlers below it (route policies and service-level role checks alike).
// An audit failure is logged and never changes the response.
func auditDenials(rec EventRecorder, lim *ratelimit.Limiter, proxies TrustedProxies) func(http.Handler) http.Handler {
	if lim == nil {
		lim = ratelimit.New(DenialAuditPerMinute, time.Minute, 10_000, platform.SystemClock{})
	}
	var mu sync.Mutex
	suppressed := map[string]int{} // denials not audited since the user's last audited one
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if ww.Status() != http.StatusForbidden {
				return
			}
			p, err := identity.FromContext(r.Context())
			if err != nil || p.Type != identity.ActorUser {
				return
			}
			actor := p.AuditActorID()
			if !lim.Allow(actor) {
				// Bounded volume: a user hammering a forbidden route cannot flood the chain.
				// The count is carried by the next audited denial.
				mu.Lock()
				if len(suppressed) < 10_000 {
					suppressed[actor]++
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			skipped := suppressed[actor]
			delete(suppressed, actor)
			mu.Unlock()
			route := r.Method + " " + routePattern(r)
			ip := proxies.Request(r)
			body := map[string]any{"method": r.Method, "route": routePattern(r), "login": p.Login}
			if skipped > 0 {
				body["suppressed_before"] = skipped
			}
			payload, _ := json.Marshal(body)
			if err := rec.Record(r.Context(), audit.Event{ActorType: string(p.Type), ActorID: actor, ActorIP: ip,
				Action: "rbac.denied", SubjectType: "route", SubjectID: route, CorrelationID: RequestIDFrom(r.Context()),
				Payload: payload}); err != nil {
				observability.Logger(r.Context()).ErrorContext(r.Context(), "audit rbac denial failed", "error", err)
			}
		})
	}
}
