package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/recovery"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

type fakeRollback struct {
	got      []recovery.RollbackCommand
	err      error
	replayed bool
}

func (f *fakeRollback) Rollback(_ context.Context, _ identity.Principal, cmd recovery.RollbackCommand) (requests.CreateResult, error) {
	f.got = append(f.got, cmd)
	if f.err != nil {
		return requests.CreateResult{}, f.err
	}
	return requests.CreateResult{Replayed: f.replayed, Request: &requests.Request{ID: uuid.MustParse("0b6f8f43-2b0b-4c8e-9f3e-5d0c3c3f9a11"),
		ShortID: "req-rollbk1", Kind: requests.KindUpdate, Status: requests.StatusDraft}}, nil
}

func TestRollbackRoute(t *testing.T) {
	stack := "7d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6"
	path := "/v1/stacks/" + stack + "/rollback"
	newRouter := func(role rbac.Role, svc *fakeRollback) func(method, path, body string, headers ...string) (int, string, map[string]string) {
		h := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
			Ready: okPinger{}, Public: []RouteRegistrar{NewRollbackHandler(svc)}, Policies: DefaultPolicies(), Roles: flowRoles{role: role}})
		return func(method, path, body string, headers ...string) (int, string, map[string]string) {
			rec := do(t, h, method, path, body, headers...)
			hdr := map[string]string{"Location": rec.Header().Get("Location"), "Idempotent-Replayed": rec.Header().Get("Idempotent-Replayed")}
			return rec.Code, rec.Body.String(), hdr
		}
	}
	key := []string{"Idempotency-Key", "rollback-0001", "Content-Type", "application/json"}

	svc := &fakeRollback{}
	call := newRouter(rbac.RoleMaintainer, svc)
	code, body, hdr := call("POST", path, `{"reason":"error rates doubled"}`, key...)
	if code != 202 || hdr["Location"] != "/v1/requests/0b6f8f43-2b0b-4c8e-9f3e-5d0c3c3f9a11" || hdr["Idempotent-Replayed"] != "" {
		t.Fatalf("%d %s %v", code, body, hdr)
	}
	var dto map[string]any
	if err := json.Unmarshal([]byte(body), &dto); err != nil || dto["short_id"] != "req-rollbk1" || dto["kind"] != "update" {
		t.Fatalf("body %s", body)
	}
	got := svc.got[0]
	if got.StackID.String() != stack || got.Reason != "error rates doubled" || got.IdempotencyKey != "rollback-0001" ||
		got.RequestHash != requests.RequestHash("POST", path, []byte(`{"reason":"error rates doubled"}`)) {
		t.Fatalf("command %+v", got)
	}

	svc.replayed = true
	if code, _, hdr := call("POST", path, `{"reason":"error rates doubled"}`, key...); code != 202 || hdr["Idempotent-Replayed"] != "true" {
		t.Fatalf("replay %d %v", code, hdr)
	}

	cases := []struct {
		name string
		role rbac.Role
		path string
		body string
		hdrs []string
		err  error
		want int
		code string
	}{
		{"developer is refused by the route policy", rbac.RoleDeveloper, path, `{"reason":"x"}`, key, nil, 403, "forbidden"},
		{"viewer is refused", rbac.RoleViewer, path, `{"reason":"x"}`, key, nil, 403, "forbidden"},
		{"no idempotency key", rbac.RoleMaintainer, path, `{"reason":"x"}`, nil, nil, 400, "bad_request"},
		{"unknown field", rbac.RoleMaintainer, path, `{"reason":"x","spec":{}}`, key, nil, 400, ""},
		{"bad stack id", rbac.RoleMaintainer, "/v1/stacks/not-a-uuid/rollback", `{"reason":"x"}`, key, nil, 404, "not_found"},
		{"busy", rbac.RoleMaintainer, path, `{"reason":"x"}`, key, apperr.Conflict("stack_busy", "busy"), 409, "stack_busy"},
		{"nothing to restore", rbac.RoleMaintainer, path, `{"reason":"x"}`, key, apperr.Conflict("no_rollback_target", "none"), 409, "no_rollback_target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeRollback{err: tc.err}
			code, body, _ := newRouter(tc.role, svc)("POST", tc.path, tc.body, tc.hdrs...)
			if code != tc.want || (tc.code != "" && !strings.Contains(body, `"code":"`+tc.code+`"`)) {
				t.Fatalf("%d %s, want %d %s", code, body, tc.want, tc.code)
			}
			if tc.want == 403 && len(svc.got) != 0 {
				t.Fatal("a refused caller must not reach the service")
			}
		})
	}
	// GET is not a route.
	if code, _, _ := newRouter(rbac.RoleMaintainer, &fakeRollback{})("GET", path, ""); code != 405 {
		t.Fatalf("GET = %d", code)
	}
}
