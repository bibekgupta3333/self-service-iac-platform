package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Scope says what kind of role a route policy checks.
type Scope int

const (
	// ScopePlatform requires a platform admin (platform-wide actions with no team).
	ScopePlatform Scope = iota + 1
	// ScopeAnyTeam requires a role in at least one team that allows the action.
	ScopeAnyTeam
)

// RoutePolicy is the RBAC requirement of one route.
type RoutePolicy struct {
	Action rbac.Action
	Scope  Scope
}

// PolicyTable maps "METHOD /v1/route/{pattern}" (the chi pattern) to its policy. A route
// without an entry needs authentication only; team- and environment-scoped checks happen in
// the services, which call rbac.Decide with the caller's role in the target team.
type PolicyTable map[string]RoutePolicy

// DefaultPolicies covers the user routes that exist today. New routes must add themselves.
func DefaultPolicies() PolicyTable {
	return PolicyTable{
		"GET /v1/audit/verify":  {Action: rbac.ActionAuditViewAll, Scope: ScopePlatform},
		"GET /v1/requests":      {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
		"GET /v1/requests/{id}": {Action: rbac.ActionRead, Scope: ScopeAnyTeam},
	}
}

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
				next.ServeHTTP(w, r)
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
func auditDenials(rec EventRecorder) func(http.Handler) http.Handler {
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
			route := r.Method + " " + routePattern(r)
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			payload, _ := json.Marshal(map[string]any{"method": r.Method, "route": routePattern(r), "login": p.Login})
			if err := rec.Record(r.Context(), audit.Event{ActorType: string(p.Type), ActorID: p.AuditActorID(), ActorIP: ip,
				Action: "rbac.denied", SubjectType: "route", SubjectID: route, CorrelationID: RequestIDFrom(r.Context()),
				Payload: payload}); err != nil {
				observability.Logger(r.Context()).ErrorContext(r.Context(), "audit rbac denial failed", "error", err)
			}
		})
	}
}
