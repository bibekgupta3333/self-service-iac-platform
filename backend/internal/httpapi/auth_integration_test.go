package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/teamsync"
)

// TestGitHubLoginEndToEnd drives login, callback, /me, CSRF-protected logout and the
// denial audit through the real router, Postgres stores and the real GitHub adapter
// pointed at a fake GitHub.
func TestGitHubLoginEndToEnd(t *testing.T) {
	dbtest.Skip(t)
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"tok"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":555,"login":"priya","name":"Priya","email":"p@example.test"}`))
		case "/user/memberships/orgs/acme":
			_, _ = w.Write([]byte(`{"state":"active"}`))
		case "/user/teams":
			_, _ = w.Write([]byte(`[{"slug":"payments","organization":{"login":"acme"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	if _, err := pool.Exec(ctx, `INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
		VALUES ('payments', 'Payments', 1, 'payments', 'cc-1')`); err != nil {
		t.Fatal(err)
	}

	clock, ids := platform.SystemClock{}, platform.RandomIDs{}
	writer := audit.NewWriter(clock, ids)
	recorder := audit.NewRecorder(db.NewTxRunner(pool), writer)
	sessions := session.NewService(session.NewPostgresStore(pool),
		session.Policy{AbsoluteTTL: time.Hour, IdleTTL: 10 * time.Minute}, clock)
	client, err := oauth.NewGitHubClient(oauth.GitHubConfig{ClientID: "id", ClientSecret: "secret-" + uuid.NewString(),
		CallbackURL: "http://localhost:3000/api/iacp/v1/auth/callback", Org: "acme"},
		oauth.WithEndpoints(gh.URL, gh.URL), oauth.AllowLoopbackHTTP())
	if err != nil {
		t.Fatal(err)
	}
	syncer := teamsync.NewSyncer(db.NewTxRunner(pool), writer, clock, teamsync.Mapping{AdminTeam: "iacp-admins"})
	login := oauth.NewService(client, oauth.NewPostgresStateStore(pool), syncer, sessions, recorder,
		oauth.RedirectPolicy{Default: "/"}, clock)
	users := identity.NewUserStore(pool)
	ah := &AuthHandler{Login: login, Sessions: sessions, Profiles: users, Audit: recorder, Mode: "github"}
	h := NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: NewSessionAuthenticator(sessions), Ready: okPinger{},
		Public: []RouteRegistrar{ah, NewRequestHandler(&fakeRequests{byHash: nil}), NewAuditHandler(audit.NewVerifier(pool))}, PublicHandlers: []RouteRegistrar{ah.Public()},
		Policies: DefaultPolicies(), Roles: users, DenialAudit: recorder,
	})
	call := func(method, path string, cookies []*http.Cookie, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	lr := call("GET", "/v1/auth/login?return_to=/requests", nil)
	state := cookieOf(lr, StateCookieName)
	if lr.Code != http.StatusFound || state == nil {
		t.Fatalf("login = %d", lr.Code)
	}
	if c := cookieOf(lr, StateCookieName); c.Secure {
		t.Fatal("plain-http loopback must not set Secure")
	}
	cb := call("GET", "/v1/auth/callback?code=abc&state="+state.Value, []*http.Cookie{state})
	sess := cookieOf(cb, session.CookieName)
	if cb.Code != http.StatusFound || cb.Header().Get("Location") != "/requests" || sess == nil {
		t.Fatalf("callback = %d %s", cb.Code, cb.Header().Get("Location"))
	}
	// The state is single use.
	if again := call("GET", "/v1/auth/callback?code=abc&state="+state.Value, []*http.Cookie{state}); !strings.Contains(again.Header().Get("Location"), "login_error=invalid_state") {
		t.Fatalf("replayed callback = %d %s", again.Code, again.Header().Get("Location"))
	}

	me := call("GET", "/v1/me", []*http.Cookie{sess})
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"slug":"payments","role":"developer"`) ||
		!strings.Contains(me.Body.String(), session.CSRFToken(sess.Value)) {
		t.Fatalf("me = %d %s", me.Code, me.Body.String())
	}
	// Developer: may read requests, may not verify the audit chain; the denial is audited.
	if rec := call("GET", "/v1/requests", []*http.Cookie{sess}); rec.Code != 200 {
		t.Fatalf("requests = %d", rec.Code)
	}
	if rec := call("GET", "/v1/audit/verify", []*http.Cookie{sess}); rec.Code != http.StatusForbidden {
		t.Fatalf("audit verify = %d", rec.Code)
	}
	var denied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'rbac.denied' AND subject_id = 'GET /v1/audit/verify'`).Scan(&denied); err != nil || denied != 1 {
		t.Fatalf("denial audit rows = %d, %v", denied, err)
	}

	if rec := call("POST", "/v1/auth/logout", []*http.Cookie{sess}); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf = %d", rec.Code)
	}
	if rec := call("POST", "/v1/auth/logout", []*http.Cookie{sess}, "X-CSRF-Token", session.CSRFToken(sess.Value)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec := call("GET", "/v1/me", []*http.Cookie{sess}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout = %d", rec.Code)
	}
	for _, action := range []string{"auth.login", "auth.logout", "rbac.membership_changed"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = $1`, action).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s events = %d, %v", action, n, err)
		}
	}
}
