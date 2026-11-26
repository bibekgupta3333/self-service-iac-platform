package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// headerAuth authenticates by the X-Test-User header (a user id), standing in for sessions.
type headerAuth struct{ users map[string]identity.Principal }

func (h headerAuth) Authenticate(r *http.Request) (identity.Principal, error) {
	p, ok := h.users[r.Header.Get("X-Test-User")]
	if !ok {
		return identity.Principal{}, ErrUnauthenticated
	}
	return p, nil
}

type scopeEnv struct {
	h       http.Handler
	reqA    uuid.UUID
	reqB    uuid.UUID
	teamIDs map[string]uuid.UUID
	userHdr map[string]string
	pool    *pgxpool.Pool
}

func seedScope(t *testing.T) *scopeEnv {
	t.Helper()
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	e := &scopeEnv{teamIDs: map[string]uuid.UUID{}, userHdr: map[string]string{}, pool: pool}

	mkUser := func(login string, gid int64, admin bool) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (github_user_id, github_login, is_platform_admin) VALUES ($1,$2,$3) RETURNING id`,
			gid, login, admin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	users := map[string]identity.Principal{}
	add := func(name string, gid int64, admin bool) uuid.UUID {
		id := mkUser(name, gid, admin)
		users[id.String()] = identity.Principal{UserID: id, Login: name, Type: identity.ActorUser, PlatformAdmin: admin}
		e.userHdr[name] = id.String()
		return id
	}
	viewerA := add("viewer-a", 1, false)
	devB := add("dev-b", 2, false)
	both := add("both", 3, false)
	add("nobody", 4, false)
	add("admin", 5, true)

	for i, slug := range []string{"alpha", "beta"} {
		var tid uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
			VALUES ($1, $1, $2, $1, 'cc') RETURNING id`, slug, 100+i).Scan(&tid); err != nil {
			t.Fatal(err)
		}
		e.teamIDs[slug] = tid
		var sid, rid uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO stacks (team_id, environment_id, name, path, state_key, region, created_by)
			SELECT $1, env.id, 'stk', 'environments/development/'||$2||'/stk', 'k-'||$2, 'us-east-1', $3
			FROM environments env WHERE env.name = 'development' RETURNING id`, tid, slug, viewerA).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO requests (short_id, stack_id, kind, spec, created_by)
			VALUES ('req-'||$2, $1, 'create', '{}'::jsonb, $3) RETURNING id`, sid, slug, viewerA).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		if slug == "alpha" {
			e.reqA = rid
		} else {
			e.reqB = rid
		}
	}
	for _, m := range []struct {
		team string
		user uuid.UUID
		role string
	}{{"alpha", viewerA, "viewer"}, {"beta", devB, "developer"}, {"alpha", both, "viewer"}, {"beta", both, "viewer"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (team_id, user_id, role) VALUES ($1,$2,$3)`, e.teamIDs[m.team], m.user, m.role); err != nil {
			t.Fatal(err)
		}
	}

	clock, ids := platform.SystemClock{}, platform.RandomIDs{}
	writer := audit.NewWriter(clock, ids)
	repo := requests.NewPostgresRepository(pool, writer)
	cat := catalog.NewService(catalog.NewPostgresRepository(pool, writer), clock, "https://example.test/iacp.git")
	svc := requests.NewService(repo, cat, repo, locks.NewService(locks.NewPostgresRepository(pool, writer), clock, 0), clock, ids)
	store := identity.NewUserStore(pool)
	e.h = NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: headerAuth{users: users}, Ready: okPinger{},
		Public:   []RouteRegistrar{NewRequestHandler(svc, requests.NewAccess(pool, store))},
		Policies: DefaultPolicies(), Roles: store,
	})
	return e
}

func (e *scopeEnv) get(user, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("X-Test-User", e.userHdr[user])
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func listedIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []struct {
			ShortID string `json:"short_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range out.Data {
		ids = append(ids, d.ShortID)
	}
	return ids
}

// A viewer in team alpha must never see team beta's requests, by list or by id.
func TestRequestReadsAreScopedToTheCallersTeams(t *testing.T) {
	dbtest.Skip(t)
	e := seedScope(t)

	cases := []struct {
		user string
		path string
		want []string
	}{
		{"viewer-a", "/v1/requests", []string{"req-alpha"}},
		{"viewer-a", "/v1/requests?team=beta", nil},
		{"viewer-a", "/v1/requests?team=alpha", []string{"req-alpha"}},
		{"dev-b", "/v1/requests", []string{"req-beta"}},
		{"both", "/v1/requests", []string{"req-alpha", "req-beta"}},
		{"both", "/v1/requests?team=beta", []string{"req-beta"}},
		{"admin", "/v1/requests", []string{"req-alpha", "req-beta"}},
		{"admin", "/v1/requests?team=beta", []string{"req-beta"}},
	}
	for _, tc := range cases {
		got := listedIDs(t, e.get(tc.user, tc.path))
		if len(got) != len(tc.want) {
			t.Errorf("%s %s = %v, want %v", tc.user, tc.path, got, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(strings.Join(got, ","), w) {
				t.Errorf("%s %s = %v, want %v", tc.user, tc.path, got, tc.want)
			}
		}
	}

	byID := []struct {
		user string
		id   uuid.UUID
		want int
	}{
		{"viewer-a", e.reqA, 200},
		{"viewer-a", e.reqB, 404},
		{"dev-b", e.reqA, 404},
		{"dev-b", e.reqB, 200},
		{"both", e.reqA, 200},
		{"both", e.reqB, 200},
		{"admin", e.reqB, 200},
		{"viewer-a", uuid.New(), 404},
	}
	for _, tc := range byID {
		rec := e.get(tc.user, "/v1/requests/"+tc.id.String())
		if rec.Code != tc.want {
			t.Errorf("%s GET %s = %d, want %d", tc.user, tc.id, rec.Code, tc.want)
		}
	}
	// 404 for a hidden request is byte-identical in shape to 404 for a missing one.
	hidden := e.get("viewer-a", "/v1/requests/"+e.reqB.String())
	missing := e.get("viewer-a", "/v1/requests/"+uuid.NewString())
	if hidden.Code != missing.Code || !strings.Contains(hidden.Body.String(), `"code":"not_found"`) ||
		strings.Contains(hidden.Body.String(), "beta") {
		t.Fatalf("hidden = %d %s; missing = %d", hidden.Code, hidden.Body.String(), missing.Code)
	}
	// A caller with no team role is refused by the route policy, not shown an empty list.
	if rec := e.get("nobody", "/v1/requests"); rec.Code != http.StatusForbidden {
		t.Fatalf("nobody list = %d", rec.Code)
	}
}
