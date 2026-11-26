package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testClientID = "test-client-id"
	testCode     = "test-code-123"
	testToken    = "gho_test_access_token"
)

// testSecret is generated per run; nothing here is a real credential.
var testSecret = "secret-" + strings.Repeat("s", 20)

type fakeGitHub struct {
	*httptest.Server
	member      string // "active", "pending", "none", "forbidden"
	teamPages   [][]map[string]any
	profileJSON string
	tokenReply  string
	hits        map[string]int
	lastForm    url.Values
	lastAuth    atomic.Value
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{member: "active", hits: map[string]int{},
		profileJSON: `{"id": 4242, "login": "priya", "name": "Priya S.", "email": null}`,
		teamPages: [][]map[string]any{{
			{"slug": "payments", "organization": map[string]any{"login": "acme"}},
			{"slug": "other-team", "organization": map[string]any{"login": "elsewhere"}},
			{"slug": "payments-maintainers", "organization": map[string]any{"login": "ACME"}},
		}}}
	f.tokenReply = `{"access_token": "` + testToken + `", "token_type": "bearer"}`
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		f.hits["token"]++
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.tokenReply))
	})
	api := func(name string, h http.HandlerFunc) {
		mux.HandleFunc("GET "+name, func(w http.ResponseWriter, r *http.Request) {
			f.hits[name]++
			f.lastAuth.Store(r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			h(w, r)
		})
	}
	api("/user", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(f.profileJSON)) })
	api("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"x@example.test","primary":false,"verified":true},{"email":"priya@example.test","primary":true,"verified":true}]`))
	})
	api("/user/memberships/orgs/acme", func(w http.ResponseWriter, _ *http.Request) {
		switch f.member {
		case "none":
			http.NotFound(w, nil)
		case "forbidden":
			http.Error(w, "restricted", http.StatusForbidden)
		default:
			_, _ = fmt.Fprintf(w, `{"state": %q, "role": "member"}`, f.member)
		}
	})
	api("/user/teams", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		_, _ = fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		if page > len(f.teamPages) {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(w).Encode(f.teamPages[page-1])
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) client(t *testing.T) *GitHubClient {
	t.Helper()
	c, err := NewGitHubClient(GitHubConfig{ClientID: testClientID, ClientSecret: testSecret,
		CallbackURL: "https://portal.example.test/api/iacp/v1/auth/callback", Org: "acme"},
		WithEndpoints(f.URL, f.URL), AllowLoopbackHTTP())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthenticateHappyPath(t *testing.T) {
	f := newFakeGitHub(t)
	u, err := f.client(t).Authenticate(context.Background(), testCode, "verifier-value")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 4242 || u.Login != "priya" || u.Name != "Priya S." || !u.OrgMember {
		t.Fatalf("user = %+v", u)
	}
	if u.Email != "priya@example.test" {
		t.Fatalf("email should fall back to the primary verified address, got %q", u.Email)
	}
	if got := strings.Join(u.TeamSlugs, ","); got != "payments,payments-maintainers" {
		t.Fatalf("teams = %q (only the allowed org's teams, case-insensitively)", got)
	}
	form := f.lastForm
	if form.Get("code") != testCode || form.Get("code_verifier") != "verifier-value" || form.Get("client_id") != testClientID ||
		form.Get("client_secret") != testSecret || form.Get("redirect_uri") != "https://portal.example.test/api/iacp/v1/auth/callback" {
		t.Fatalf("token exchange form = %v", form)
	}
}

func TestAuthenticatePaginatesTeams(t *testing.T) {
	f := newFakeGitHub(t)
	full := make([]map[string]any, teamsPerPage)
	for i := range full {
		full[i] = map[string]any{"slug": fmt.Sprintf("t%d", i), "organization": map[string]any{"login": "acme"}}
	}
	f.teamPages = [][]map[string]any{full, {{"slug": "last", "organization": map[string]any{"login": "acme"}}}}
	u, err := f.client(t).Authenticate(context.Background(), testCode, "v")
	if err != nil || len(u.TeamSlugs) != teamsPerPage+1 || u.TeamSlugs[teamsPerPage] != "last" {
		t.Fatalf("teams = %d, %v", len(u.TeamSlugs), err)
	}
	if f.hits["/user/teams"] != 2 {
		t.Fatalf("team page requests = %d", f.hits["/user/teams"])
	}
}

func TestAuthenticateNonMembersGetNoTeams(t *testing.T) {
	for _, state := range []string{"none", "pending", "forbidden"} {
		t.Run(state, func(t *testing.T) {
			f := newFakeGitHub(t)
			f.member = state
			u, err := f.client(t).Authenticate(context.Background(), testCode, "v")
			if err != nil {
				t.Fatal(err)
			}
			if u.OrgMember {
				t.Fatal("must not be treated as a member")
			}
			if f.hits["/user/teams"] != 0 || f.hits["/user/emails"] != 0 {
				t.Fatal("no further calls for a non-member")
			}
		})
	}
}

func TestAuthenticateFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fakeGitHub)
	}{
		{"token error", func(f *fakeGitHub) { f.tokenReply = `{"error": "bad_verification_code"}` }},
		{"empty token", func(f *fakeGitHub) { f.tokenReply = `{"access_token": ""}` }},
		{"token reply not JSON", func(f *fakeGitHub) { f.tokenReply = `<html>` }},
		{"malformed profile", func(f *fakeGitHub) { f.profileJSON = `{"id": 0, "login": ""}` }},
		{"profile not JSON", func(f *fakeGitHub) { f.profileJSON = `nope` }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeGitHub(t)
			tt.setup(f)
			_, err := f.client(t).Authenticate(context.Background(), testCode, "v")
			if !errors.Is(err, ErrGitHub) {
				t.Fatalf("err = %v, want ErrGitHub", err)
			}
			for _, secret := range []string{testSecret, testCode, testToken} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks a secret: %v", err)
				}
			}
		})
	}
}

func TestAuthenticateServerErrorIsAnError(t *testing.T) {
	f := newFakeGitHub(t)
	c := f.client(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", 502) }))
	defer srv.Close()
	c2, _ := NewGitHubClient(GitHubConfig{ClientID: "a", ClientSecret: "b", CallbackURL: "https://x.example.test/cb", Org: "acme"},
		WithEndpoints(srv.URL, srv.URL), AllowLoopbackHTTP())
	if _, err := c2.Authenticate(context.Background(), testCode, "v"); !errors.Is(err, ErrGitHub) {
		t.Fatalf("err = %v", err)
	}
	_ = c
}

func TestRedirectToAnotherHostIsNotFollowed(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHits.Add(1)
		_, _ = w.Write([]byte(`{"state":"active"}`))
	}))
	defer evil.Close()

	for _, path := range []string{"/login/oauth/access_token", "/user"} {
		t.Run(path, func(t *testing.T) {
			evilHits.Store(0)
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, evil.URL+r.URL.Path, http.StatusTemporaryRedirect)
			}))
			defer gh.Close()
			c, _ := NewGitHubClient(GitHubConfig{ClientID: "a", ClientSecret: testSecret, CallbackURL: "https://x.example.test/cb", Org: "acme"},
				WithEndpoints(gh.URL, gh.URL), AllowLoopbackHTTP())
			var err error
			if path == "/user" {
				err = c.apiGet(context.Background(), testToken, "/user", nil, &struct{}{})
			} else {
				_, err = c.exchange(context.Background(), testCode, "v")
			}
			if !errors.Is(err, ErrGitHub) || !strings.Contains(err.Error(), "host") && !strings.Contains(err.Error(), "redirect") {
				t.Fatalf("err = %v", err)
			}
			if evilHits.Load() != 0 {
				t.Fatal("the redirect was followed to another host")
			}
			if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testCode) {
				t.Fatalf("error leaks a secret: %v", err)
			}
		})
	}
}

func TestRedirectLoopIsBounded(t *testing.T) {
	var n atomic.Int32
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Redirect(w, r, gh.URL+"/user", http.StatusFound)
	}))
	defer gh.Close()
	c, _ := NewGitHubClient(GitHubConfig{ClientID: "a", ClientSecret: "b", CallbackURL: "https://x.example.test/cb", Org: "acme"},
		WithEndpoints(gh.URL, gh.URL), AllowLoopbackHTTP())
	if err := c.apiGet(context.Background(), testToken, "/user", nil, &struct{}{}); !errors.Is(err, ErrGitHub) {
		t.Fatalf("err = %v", err)
	}
	if n.Load() > maxRedirects+1 {
		t.Fatalf("followed %d requests", n.Load())
	}
}

func TestTimeoutBoundsASlowServer(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer gh.Close()
	c, _ := NewGitHubClient(GitHubConfig{ClientID: "a", ClientSecret: "b", CallbackURL: "https://x.example.test/cb", Org: "acme"},
		WithEndpoints(gh.URL, gh.URL), AllowLoopbackHTTP())
	if c.hc.Timeout != requestTimeout || requestTimeout > 30*time.Second {
		t.Fatalf("client timeout = %v", c.hc.Timeout)
	}
	c.hc.Timeout = 100 * time.Millisecond
	start := time.Now()
	if err := c.apiGet(context.Background(), testToken, "/user", nil, &struct{}{}); !errors.Is(err, ErrGitHub) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the timeout did not bound the call")
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFakeGitHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client(t).Authenticate(ctx, testCode, "v"); !errors.Is(err, ErrGitHub) {
		t.Fatalf("err = %v", err)
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"x":"` + strings.Repeat("a", maxResponse+10) + `"}`))
	}))
	defer gh.Close()
	c, _ := NewGitHubClient(GitHubConfig{ClientID: "a", ClientSecret: "b", CallbackURL: "https://x.example.test/cb", Org: "acme"},
		WithEndpoints(gh.URL, gh.URL), AllowLoopbackHTTP())
	if err := c.apiGet(context.Background(), testToken, "/user", nil, &struct{}{}); !errors.Is(err, ErrGitHub) {
		t.Fatalf("err = %v", err)
	}
}

func TestHostRestriction(t *testing.T) {
	cfg := GitHubConfig{ClientID: "a", ClientSecret: "b", CallbackURL: "https://x.example.test/cb", Org: "acme"}

	def, err := NewGitHubClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for u, want := range map[string]bool{
		"https://github.com/login/oauth/access_token": true,
		"https://api.github.com/user":                 true,
		"https://evil.example.test/user":              false,
		"https://github.com.evil.example.test/":       false,
		"http://github.com/login":                     false, // downgrade
		"https://gist.github.com/":                    false,
	} {
		pu, _ := url.Parse(u)
		if got := def.hostAllowed(pu); got != want {
			t.Errorf("hostAllowed(%s) = %v, want %v", u, got, want)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://evil.example.test/user", nil)
	if err := def.do(req, &struct{}{}); !errors.Is(err, ErrGitHub) {
		t.Fatalf("do() to a foreign host: %v", err)
	}

	if _, err := NewGitHubClient(cfg, WithEndpoints("http://127.0.0.1:1", "http://127.0.0.1:1")); err == nil {
		t.Fatal("plain http must be refused without the loopback option")
	}
	if _, err := NewGitHubClient(cfg, WithEndpoints("http://github.example.test", "http://github.example.test"), AllowLoopbackHTTP()); err == nil {
		t.Fatal("plain http must be refused for non-loopback hosts even with the option")
	}
	if _, err := NewGitHubClient(cfg, WithEndpoints("", "")); err == nil {
		t.Fatal("empty endpoints must be refused")
	}
	for _, bad := range []GitHubConfig{{}, {ClientID: "a"}, {ClientID: "a", ClientSecret: "b", CallbackURL: "x"}} {
		if _, err := NewGitHubClient(bad); err == nil {
			t.Errorf("config %+v must be refused", bad)
		}
	}
}

func TestAuthorizeURL(t *testing.T) {
	c, _ := NewGitHubClient(GitHubConfig{ClientID: testClientID, ClientSecret: testSecret,
		CallbackURL: "https://portal.example.test/api/iacp/v1/auth/callback", Org: "acme"})
	raw := c.AuthorizeURL("state-1", "challenge-1")
	if strings.Contains(raw, testSecret) {
		t.Fatal("the client secret must never appear in the authorize URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.Path != "/login/oauth/authorize" {
		t.Fatalf("url = %s, %v", raw, err)
	}
	q := u.Query()
	want := map[string]string{"client_id": testClientID, "state": "state-1", "code_challenge": "challenge-1",
		"code_challenge_method": "S256", "redirect_uri": "https://portal.example.test/api/iacp/v1/auth/callback"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !strings.Contains(q.Get("scope"), "read:org") {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}
