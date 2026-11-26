package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

var ttlAt = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type fakeTTL struct {
	extended []struct {
		by    identity.Principal
		id    uuid.UUID
		hours int
		corr  string
	}
	warned, torn []uuid.UUID
	corr         string
	teardown     ttl.DestroyResult
	teardownErr  error
}

func (f *fakeTTL) Extend(_ context.Context, by identity.Principal, id uuid.UUID, hours int, corr string) (ttl.ExtendResult, error) {
	if id != lockStack {
		return ttl.ExtendResult{}, apperr.NotFound("stack %s not found", id)
	}
	if hours > 72 {
		return ttl.ExtendResult{}, apperr.Conflict("ttl_extension_cap", "cap reached")
	}
	f.extended = append(f.extended, struct {
		by    identity.Principal
		id    uuid.UUID
		hours int
		corr  string
	}{by, id, hours, corr})
	return ttl.ExtendResult{ExpiresAt: ttlAt.Add(time.Duration(hours) * time.Hour), ExtendedHours: hours, RemainingExtHours: 100 - hours}, nil
}

func (f *fakeTTL) Due(context.Context) ([]ttl.DueItem, error) {
	mk := func(n byte, env string, exp time.Time, p ttl.Phase) ttl.DueItem {
		return ttl.DueItem{Phase: p, Stack: ttl.Stack{ID: uuid.UUID{15: n}, Team: "payments", Environment: env,
			Path: "environments/" + env + "/payments/s" + string('0'+n), ExpiresAt: &exp}}
	}
	return []ttl.DueItem{mk(1, "development", ttlAt.Add(-time.Hour), ttl.PhaseTeardown), mk(2, "staging", ttlAt.Add(5*time.Hour), ttl.PhaseWarn)}, nil
}

func (f *fakeTTL) Warn(_ context.Context, id uuid.UUID, corr string) (ttl.WarnResult, error) {
	f.warned, f.corr = append(f.warned, id), corr
	return ttl.WarnResult{Warned: true}, nil
}

func (f *fakeTTL) Teardown(_ context.Context, id uuid.UUID, corr string) (ttl.DestroyResult, error) {
	f.torn, f.corr = append(f.torn, id), corr
	return f.teardown, f.teardownErr
}

type fixedRole rbac.Role

func (r fixedRole) HighestRole(context.Context, uuid.UUID) (rbac.Role, error) {
	return rbac.Role(r), nil
}

func ttlUserRouter(svc *fakeTTL, role rbac.Role) http.Handler {
	return NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal}, Ready: okPinger{},
		Public: []RouteRegistrar{NewTTLHandler(svc)}, Policies: DefaultPolicies(), Roles: fixedRole(role),
	})
}

func TestExtendRoute(t *testing.T) {
	url := "/v1/stacks/" + lockStack.String() + "/ttl/extend"

	t.Run("a developer extends", func(t *testing.T) {
		svc := &fakeTTL{}
		rec := do(t, ttlUserRouter(svc, rbac.RoleDeveloper), "POST", url, `{"hours":24}`, "X-Request-Id", "corr-ttl")
		if rec.Code != 200 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var b map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if b["stack_id"] != lockStack.String() || b["expires_at"] != "2026-11-27T12:00:00Z" || b["extended_hours"] != float64(24) ||
			b["remaining_extension_hours"] != float64(76) {
			t.Fatalf("body = %v", b)
		}
		if len(svc.extended) != 1 || svc.extended[0].by != devPrincipal || svc.extended[0].hours != 24 || svc.extended[0].corr != "corr-ttl" {
			t.Fatalf("extend calls = %+v", svc.extended)
		}
	})
	t.Run("a viewer is refused by the route policy before the service runs", func(t *testing.T) {
		svc := &fakeTTL{}
		rec := do(t, ttlUserRouter(svc, rbac.RoleViewer), "POST", url, `{"hours":24}`)
		if rec.Code != 403 || len(svc.extended) != 0 || !strings.Contains(rec.Body.String(), "stack.extend_ttl requires the developer role") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("input and service errors map to the problem statuses", func(t *testing.T) {
		h := ttlUserRouter(&fakeTTL{}, rbac.RoleDeveloper)
		for _, tc := range []struct {
			name, path, body string
			want             int
		}{
			{"no hours", url, `{}`, 422},
			{"unknown field", url, `{"hours":1,"x":1}`, 400},
			{"not json", url, `nope`, 400},
			{"bad stack id", "/v1/stacks/nope/ttl/extend", `{"hours":1}`, 404},
			{"unknown stack", "/v1/stacks/" + uuid.NewString() + "/ttl/extend", `{"hours":1}`, 404},
			{"cap reached", url, `{"hours":100}`, 409},
		} {
			if rec := do(t, h, "POST", tc.path, tc.body); rec.Code != tc.want {
				t.Errorf("%s = %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body)
			}
		}
		if rec := do(t, h, "GET", url, ""); rec.Code != 405 {
			t.Errorf("GET = %d, want 405", rec.Code)
		}
	})
}

func reaperRouter(svc *fakeTTL) http.Handler {
	return NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal}, Ready: okPinger{},
		Internal:         AnyAuthenticator{NewOIDCAuthenticator(fakeTokenVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		InternalHandlers: []RouteRegistrar{NewTTLReaperHandler(svc)},
	})
}

func TestReaperRoutes(t *testing.T) {
	reaper := []string{"Authorization", "Bearer reaper.jwt.token", "X-Request-Id", "corr-reap"}
	id := lockStack.String()

	t.Run("due splits warnings and teardowns", func(t *testing.T) {
		rec := do(t, reaperRouter(&fakeTTL{}), "GET", "/v1/internal/ttl/due", "", reaper...)
		if rec.Code != 200 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var b struct {
			Warnings, Teardowns []struct {
				StackID     string `json:"stack_id"`
				Path        string `json:"path"`
				Environment string `json:"environment"`
				ExpiresAt   string `json:"expires_at"`
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if len(b.Teardowns) != 1 || b.Teardowns[0].Environment != "development" || b.Teardowns[0].ExpiresAt != "2026-11-26T11:00:00Z" ||
			len(b.Warnings) != 1 || b.Warnings[0].Environment != "staging" || b.Warnings[0].StackID != (uuid.UUID{15: 2}).String() {
			t.Fatalf("due = %s", rec.Body)
		}
	})
	t.Run("warn passes the correlation id", func(t *testing.T) {
		svc := &fakeTTL{}
		rec := do(t, reaperRouter(svc), "POST", "/v1/internal/ttl/"+id+"/warn", "", reaper...)
		if rec.Code != 200 || len(svc.warned) != 1 || svc.corr != "corr-reap" || !strings.Contains(rec.Body.String(), `"warned":true`) {
			t.Fatalf("status = %d: %s (%+v)", rec.Code, rec.Body, svc)
		}
	})
	t.Run("teardown reports created and already pending", func(t *testing.T) {
		reqID := uuid.New()
		svc := &fakeTTL{teardown: ttl.DestroyResult{Created: true, RequestID: reqID, ShortID: "req-abc123"}}
		rec := do(t, reaperRouter(svc), "POST", "/v1/internal/ttl/"+id+"/teardown", "", reaper...)
		if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"status":"created"`) || !strings.Contains(rec.Body.String(), reqID.String()) ||
			svc.torn[0] != lockStack {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		svc.teardown = ttl.DestroyResult{}
		rec = do(t, reaperRouter(svc), "POST", "/v1/internal/ttl/"+id+"/teardown", "", reaper...)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"already_pending"`) {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("teardown refusals surface as conflicts", func(t *testing.T) {
		svc := &fakeTTL{teardownErr: apperr.Conflict("ttl_not_due", "not expired")}
		rec := do(t, reaperRouter(svc), "POST", "/v1/internal/ttl/"+id+"/teardown", "", reaper...)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "ttl_not_due") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		if rec := do(t, reaperRouter(svc), "POST", "/v1/internal/ttl/nope/teardown", "", reaper...); rec.Code != 404 {
			t.Fatalf("bad id = %d", rec.Code)
		}
	})
	t.Run("only the reaper workflow and the shared token may call", func(t *testing.T) {
		h := reaperRouter(&fakeTTL{})
		for token, want := range map[string]int{
			"reaper.jwt.token": 200, testInternalToken: 200, "apply.jwt.token": 403, "release.jwt.token": 403, "prvalidate.jwt.token": 403,
		} {
			for _, rt := range []struct{ method, path string }{
				{"GET", "/v1/internal/ttl/due"}, {"POST", "/v1/internal/ttl/" + id + "/warn"}, {"POST", "/v1/internal/ttl/" + id + "/teardown"},
			} {
				rec := do(t, h, rt.method, rt.path, "", "Authorization", "Bearer "+token)
				if rec.Code != want && !(want == 200 && rec.Code == 202) {
					t.Errorf("%s %s with %.12s = %d, want %d: %s", rt.method, rt.path, token, rec.Code, want, rec.Body)
				}
			}
		}
		if rec := do(t, h, "GET", "/v1/internal/ttl/due", ""); rec.Code != 401 {
			t.Errorf("no token = %d, want 401", rec.Code)
		}
	})
	t.Run("a user session never opens the machine routes", func(t *testing.T) {
		h := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal}, Ready: okPinger{},
			InternalHandlers: []RouteRegistrar{NewTTLReaperHandler(&fakeTTL{})}})
		if rec := do(t, h, "GET", "/v1/internal/ttl/due", ""); rec.Code != 401 {
			t.Fatalf("dev user on a machine route = %d", rec.Code)
		}
	})
}
