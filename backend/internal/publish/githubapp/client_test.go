package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
)

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

// appKey generates the App key once per test binary; nothing is read from disk.
func appKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testKey)})
}

// fakeGitHub is an in-memory GitHub REST API for one repository.
type fakeGitHub struct {
	t   *testing.T
	key *rsa.PublicKey
	now time.Time

	mu         sync.Mutex
	calls      []string // "METHOD path"
	tokens     int
	refs       map[string]string // branch → sha
	blobs      map[string][]byte
	trees      map[string][]TreeEntry
	pulls      map[string]int // head → number
	labels     map[int][]string
	dispatches []map[string]any
	failNext   map[string][]int // "METHOD path" → statuses to answer first
	retryAfter string
}

func newFake(t *testing.T) (*fakeGitHub, *httptest.Server) {
	key, _ := appKey(t)
	f := &fakeGitHub{t: t, key: &key.PublicKey, now: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
		refs: map[string]string{"main": "base0000000000000000000000000000000000000"}, blobs: map[string][]byte{},
		trees: map[string][]TreeEntry{}, pulls: map[string]int{}, labels: map[int][]string{}, failNext: map[string][]int{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	if st := f.failNext[call]; len(st) > 0 {
		f.failNext[call] = st[1:]
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		w.WriteHeader(st[0])
		_, _ = io.WriteString(w, `{"message":"injected failure"}`)
		return
	}
	if r.Header.Get("X-GitHub-Api-Version") != apiVersion || r.Header.Get("Accept") != "application/vnd.github+json" {
		http.Error(w, `{"message":"bad headers"}`, http.StatusBadRequest)
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	reply := func(status int, v any) {
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}

	if r.URL.Path == "/app/installations/99/access_tokens" {
		claims := jwt.RegisteredClaims{}
		_, err := jwt.ParseWithClaims(bearer, &claims, func(*jwt.Token) (any, error) { return f.key, nil },
			jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(func() time.Time { return f.now }))
		if err != nil || claims.Issuer != "12345" || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 10*time.Minute {
			reply(http.StatusUnauthorized, map[string]string{"message": "bad jwt"})
			return
		}
		perms, _ := body["permissions"].(map[string]any)
		if len(perms) != 3 || perms["contents"] != "write" || perms["pull_requests"] != "write" || perms["actions"] != "write" {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "token must be narrowed to the publisher's permissions"})
			return
		}
		if repos, _ := body["repositories"].([]any); len(repos) != 1 || repos[0] != "infra" {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "token must be scoped to the repo"})
			return
		}
		f.tokens++
		reply(http.StatusCreated, map[string]any{"token": fmt.Sprintf("ghs_test%d", f.tokens), "expires_at": f.now.Add(time.Hour)})
		return
	}
	if !strings.HasPrefix(bearer, "ghs_test") {
		reply(http.StatusUnauthorized, map[string]string{"message": "bad token"})
		return
	}

	p := strings.TrimPrefix(r.URL.Path, "/repos/acme/infra")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/ref/heads/"):
		sha, ok := f.refs[strings.TrimPrefix(p, "/git/ref/heads/")]
		if !ok {
			reply(http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		reply(http.StatusOK, map[string]any{"object": map[string]string{"sha": sha}})
	case r.Method == http.MethodGet && p == "/git/trees/basetree":
		if r.URL.Query().Get("recursive") != "1" {
			reply(http.StatusBadRequest, map[string]string{"message": "recursive listing expected"})
			return
		}
		reply(http.StatusOK, map[string]any{"truncated": false, "tree": []map[string]string{
			{"path": "README.md", "type": "blob"},
			{"path": "environments/development/payments/orders", "type": "tree"},
			{"path": "environments/development/payments/orders/main.tf", "type": "blob"},
			{"path": "environments/development/payments/orders/old.tf", "type": "blob"},
			{"path": "environments/development/payments/orders-v2/main.tf", "type": "blob"},
		}})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/commits/"):
		reply(http.StatusOK, map[string]any{"tree": map[string]string{"sha": "basetree"}})
	case r.Method == http.MethodPost && p == "/git/blobs":
		content, _ := base64.StdEncoding.DecodeString(body["content"].(string))
		sha := fmt.Sprintf("blob%02d", len(f.blobs))
		f.blobs[sha] = content
		reply(http.StatusCreated, map[string]string{"sha": sha})
	case r.Method == http.MethodPost && p == "/git/trees":
		raw, _ := json.Marshal(body["tree"])
		var entries []TreeEntry
		_ = json.Unmarshal(raw, &entries)
		if body["base_tree"] != "basetree" {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "base_tree missing"})
			return
		}
		sha := fmt.Sprintf("tree%02d", len(f.trees))
		f.trees[sha] = entries
		reply(http.StatusCreated, map[string]string{"sha": sha})
	case r.Method == http.MethodPost && p == "/git/commits":
		parents, _ := body["parents"].([]any)
		if len(parents) != 1 || parents[0] != f.refs["main"] {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "bad parent"})
			return
		}
		reply(http.StatusCreated, map[string]string{"sha": "commit-" + body["tree"].(string)})
	case r.Method == http.MethodPost && p == "/git/refs":
		branch := strings.TrimPrefix(body["ref"].(string), "refs/heads/")
		if _, exists := f.refs[branch]; exists {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "Reference already exists"})
			return
		}
		f.refs[branch] = body["sha"].(string)
		reply(http.StatusCreated, nil)
	case r.Method == http.MethodPatch && strings.HasPrefix(p, "/git/refs/heads/"):
		if body["force"] != true {
			reply(http.StatusUnprocessableEntity, map[string]string{"message": "not a fast forward"})
			return
		}
		f.refs[strings.TrimPrefix(p, "/git/refs/heads/")] = body["sha"].(string)
		reply(http.StatusOK, nil)
	case r.Method == http.MethodPost && p == "/pulls":
		head := body["head"].(string)
		if _, exists := f.pulls[head]; exists {
			reply(http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed",
				"errors": []map[string]string{{"message": "A pull request already exists for acme:" + head}}})
			return
		}
		n := 800 + len(f.pulls)
		f.pulls[head] = n
		reply(http.StatusCreated, map[string]any{"number": n, "html_url": fmt.Sprintf("https://github.com/acme/infra/pull/%d", n)})
	case r.Method == http.MethodGet && p == "/pulls":
		head := strings.TrimPrefix(r.URL.Query().Get("head"), "acme:")
		n, ok := f.pulls[head]
		if !ok || r.URL.Query().Get("state") != "open" {
			reply(http.StatusOK, []any{})
			return
		}
		reply(http.StatusOK, []map[string]any{{"number": n, "html_url": fmt.Sprintf("https://github.com/acme/infra/pull/%d", n)}})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/issues/") && strings.HasSuffix(p, "/labels"):
		var n int
		_, _ = fmt.Sscanf(p, "/issues/%d/labels", &n)
		for _, l := range body["labels"].([]any) {
			f.labels[n] = append(f.labels[n], l.(string))
		}
		reply(http.StatusOK, nil)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/actions/workflows/"):
		f.dispatches = append(f.dispatches, body)
		reply(http.StatusNoContent, nil)
	default:
		reply(http.StatusNotFound, map[string]string{"message": "Not Found: " + call})
	}
}

func (f *fakeGitHub) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

type recordedSleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *recordedSleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return nil
}

func newClient(t *testing.T, srv *httptest.Server, f *fakeGitHub, opts ...Option) (*Client, *recordedSleeps) {
	t.Helper()
	_, pemKey := appKey(t)
	sl := &recordedSleeps{}
	c, err := New(Config{AppID: 12345, InstallationID: 99, PrivateKey: pemKey, Owner: "acme", Repo: "infra",
		BaseBranch: "main", APIURL: srv.URL, Timeout: 2 * time.Second},
		platform.FixedClock{T: f.now}, append([]Option{WithSleep(sl.sleep)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c, sl
}

var files = map[string][]byte{
	"environments/development/payments/orders/main.tf":         []byte("module \"q\" {}\n"),
	"environments/development/payments/orders/iacp.stack.json": []byte("{}\n"),
}

func TestCommitFilesOpensPRAndLabels(t *testing.T) {
	f, srv := newFake(t)
	c, _ := newClient(t, srv, f)
	ctx := context.Background()

	ref, err := c.CommitFiles(ctx, publish.Commit{Branch: "iacp/req-7k3p9x", Message: "feat: stack", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Branch != "iacp/req-7k3p9x" || ref.SHA != "commit-tree00" || ref.TreeSHA != "tree00" || f.refs["iacp/req-7k3p9x"] != ref.SHA {
		t.Fatalf("ref = %+v, refs = %v", ref, f.refs)
	}
	entries := f.trees["tree00"]
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
		if e.Mode != "100644" || e.Type != "blob" || e.SHA == nil || string(f.blobs[*e.SHA]) != string(files[e.Path]) {
			t.Fatalf("entry %+v does not match its file", e)
		}
	}
	if !slices.IsSorted(paths) || len(paths) != 2 {
		t.Fatalf("tree paths = %v", paths)
	}

	pr, err := c.OpenPullRequest(ctx, publish.PullRequestSpec{Head: ref.Branch, Title: "t", Body: "b"})
	if err != nil || pr.Number != 800 || pr.URL != "https://github.com/acme/infra/pull/800" {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
	if err := c.AddLabels(ctx, pr.Number, []string{"iacp:team:payments", "iacp"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.labels[800], []string{"iacp", "iacp:team:payments"}) {
		t.Fatalf("labels = %v", f.labels[800])
	}
	if err := c.DispatchWorkflow(ctx, publish.Dispatch{Workflow: "pr-validate.yml", Ref: ref.Branch, Inputs: map[string]string{"request": "req-7k3p9x"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.dispatches) != 1 || f.dispatches[0]["ref"] != ref.Branch {
		t.Fatalf("dispatches = %v", f.dispatches)
	}
	// One installation token served every call.
	if f.tokens != 1 {
		t.Fatalf("installation tokens fetched = %d, want 1 (cached)", f.tokens)
	}
}

func TestCommitReplacesTheStackRoot(t *testing.T) {
	f, srv := newFake(t)
	c, _ := newClient(t, srv, f)
	ref, err := c.CommitFiles(context.Background(), publish.Commit{Branch: "iacp/req-7k3p9x", Message: "m", Files: files,
		Root: "environments/development/payments/orders"})
	if err != nil {
		t.Fatal(err)
	}
	var deleted []string
	for _, e := range f.trees[ref.TreeSHA] {
		if e.SHA == nil {
			deleted = append(deleted, e.Path)
		}
	}
	// Only the stale file under the root goes; README and the sibling orders-v2 stay.
	if !slices.Equal(deleted, []string{"environments/development/payments/orders/old.tf"}) {
		t.Fatalf("deleted = %v", deleted)
	}
	if _, err := c.CommitFiles(context.Background(), publish.Commit{Branch: "iacp/req-7k3p9x", Files: files, Root: "environments/x"}); !errors.Is(err, publish.ErrInvalidPath) {
		t.Fatalf("files outside the root: %v", err)
	}
}

func TestPublishIsRetrySafe(t *testing.T) {
	f, srv := newFake(t)
	c, _ := newClient(t, srv, f)
	ctx := context.Background()
	cm := publish.Commit{Branch: "iacp/req-7k3p9x", Message: "m", Files: files}
	if _, err := c.CommitFiles(ctx, cm); err != nil {
		t.Fatal(err)
	}
	// Second run: the ref exists, so it is force-moved instead of failing.
	ref, err := c.CommitFiles(ctx, cm)
	if err != nil || f.refs["iacp/req-7k3p9x"] != ref.SHA || f.count("PATCH /repos/acme/infra/git/refs/heads/iacp/req-7k3p9x") != 1 {
		t.Fatalf("re-commit: %+v, %v, refs %v", ref, err, f.refs)
	}
	first, err := c.OpenPullRequest(ctx, publish.PullRequestSpec{Head: cm.Branch, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.OpenPullRequest(ctx, publish.PullRequestSpec{Head: cm.Branch, Title: "t"})
	if err != nil || again != first {
		t.Fatalf("existing PR not returned: %+v, %v", again, err)
	}
}

func TestDispatchIsNotRetried(t *testing.T) {
	f, srv := newFake(t)
	c, sl := newClient(t, srv, f)
	f.failNext["POST /repos/acme/infra/actions/workflows/pr-validate.yml/dispatches"] = []int{502}
	err := c.DispatchWorkflow(context.Background(), publish.Dispatch{Workflow: "pr-validate.yml", Ref: "main"})
	if !IsStatus(err, 502) || len(sl.d) != 0 || f.count("POST /repos/acme/infra/actions/workflows/pr-validate.yml/dispatches") != 1 {
		t.Fatalf("err = %v, sleeps = %v", err, sl.d)
	}
}

func TestOnlyExistingRefIsForceMoved(t *testing.T) {
	f, srv := newFake(t)
	c, _ := newClient(t, srv, f)
	f.failNext["POST /repos/acme/infra/git/refs"] = []int{422} // "injected failure", not "Reference already exists"
	_, err := c.CommitFiles(context.Background(), publish.Commit{Branch: "iacp/req-abcdef", Message: "m", Files: files})
	if !IsStatus(err, 422) || f.count("PATCH /repos/acme/infra/git/refs/heads/iacp/req-abcdef") != 0 {
		t.Fatalf("err = %v; a different 422 must not force-move the branch", err)
	}
}

func TestRetriesServerErrorsHonouringRetryAfter(t *testing.T) {
	f, srv := newFake(t)
	c, sl := newClient(t, srv, f)
	f.failNext["POST /repos/acme/infra/pulls"] = []int{502, 429}
	f.retryAfter = "3"
	pr, err := c.OpenPullRequest(context.Background(), publish.PullRequestSpec{Head: "iacp/req-abcdef", Title: "t"})
	if err != nil || pr.Number != 800 {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
	if !slices.Equal(sl.d, []time.Duration{3 * time.Second, 3 * time.Second}) {
		t.Fatalf("sleeps = %v", sl.d)
	}
}

func TestBackoffWithoutRetryAfterAndGivesUp(t *testing.T) {
	f, srv := newFake(t)
	c, sl := newClient(t, srv, f)
	f.failNext["POST /repos/acme/infra/issues/5/labels"] = []int{500, 503, 500, 500, 500}
	err := c.AddLabels(context.Background(), 5, []string{"iacp"})
	if !IsStatus(err, 500) || !strings.Contains(err.Error(), "after 4 attempts") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(sl.d, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}) {
		t.Fatalf("sleeps = %v", sl.d)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	f, srv := newFake(t)
	c, sl := newClient(t, srv, f)
	f.failNext["POST /repos/acme/infra/issues/5/labels"] = []int{404}
	err := c.AddLabels(context.Background(), 5, []string{"iacp"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Message != "injected failure" || len(sl.d) != 0 {
		t.Fatalf("err = %v, sleeps = %v", err, sl.d)
	}
}

func TestRetryAfterTooLongFailsFast(t *testing.T) {
	f, srv := newFake(t)
	c, sl := newClient(t, srv, f)
	f.failNext["POST /repos/acme/infra/issues/5/labels"] = []int{429}
	f.retryAfter = "3600"
	if err := c.AddLabels(context.Background(), 5, []string{"iacp"}); !IsStatus(err, 429) || len(sl.d) != 0 {
		t.Fatalf("err = %v, sleeps = %v", err, sl.d)
	}
}

func TestRevokedTokenIsRefreshedOnce(t *testing.T) {
	f, srv := newFake(t)
	c, _ := newClient(t, srv, f)
	ctx := context.Background()
	if err := c.AddLabels(ctx, 1, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	f.failNext["POST /repos/acme/infra/issues/1/labels"] = []int{401}
	if err := c.AddLabels(ctx, 1, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 {
		t.Fatalf("tokens = %d, want a refresh after 401", f.tokens)
	}
}

func TestTokenRenewedBeforeExpiry(t *testing.T) {
	f, srv := newFake(t)
	clock := &platform.FixedClock{T: f.now}
	_, pemKey := appKey(t)
	c, err := New(Config{AppID: 12345, InstallationID: 99, PrivateKey: pemKey, Owner: "acme", Repo: "infra",
		BaseBranch: "main", APIURL: srv.URL}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, at := range []time.Duration{0, 50 * time.Minute, 56 * time.Minute} {
		clock.T = f.now.Add(at)
		if err := c.AddLabels(ctx, 1, []string{"a"}); err != nil {
			t.Fatal(err)
		}
	}
	// The token expires at +60m: reused at +50m, renewed inside the 5-minute margin at +56m.
	if f.tokens != 2 {
		t.Fatalf("tokens = %d, want 2", f.tokens)
	}
}

func TestRedirectToOtherHostRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the client followed a redirect to another host")
	}))
	defer other.Close()
	f, srv := newFake(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/app/") {
			srv.Config.Handler.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	c, sl := newClient(t, redirector, f)
	if err := c.AddLabels(context.Background(), 1, []string{"a"}); !errors.Is(err, ErrRedirect) || len(sl.d) != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeoutIsEnforced(t *testing.T) {
	f, srv := newFake(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/app/") {
			srv.Config.Handler.ServeHTTP(w, r)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	_, pemKey := appKey(t)
	c, err := New(Config{AppID: 12345, InstallationID: 99, PrivateKey: pemKey, Owner: "acme", Repo: "infra",
		BaseBranch: "main", APIURL: slow.URL, Timeout: 50 * time.Millisecond, MaxAttempts: 2},
		platform.FixedClock{T: f.now}, WithSleep(func(context.Context, time.Duration) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = c.AddLabels(context.Background(), 1, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	_, pemKey := appKey(t)
	good := Config{AppID: 1, InstallationID: 2, PrivateKey: pemKey, Owner: "o", Repo: "r", BaseBranch: "main"}
	tests := []struct {
		name string
		mut  func(*Config)
	}{
		{"no app id", func(c *Config) { c.AppID = 0 }},
		{"no installation", func(c *Config) { c.InstallationID = 0 }},
		{"no repo", func(c *Config) { c.Repo = "" }},
		{"bad key", func(c *Config) { c.PrivateKey = []byte("not a key") }},
		{"plain http", func(c *Config) { c.APIURL = "http://api.github.com" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good
			tt.mut(&c)
			if _, err := New(c, platform.SystemClock{}); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := New(good, platform.SystemClock{}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsUnsafeInput(t *testing.T) {
	f, srv := newFake(t)
	c, _ := newClient(t, srv, f)
	ctx := context.Background()
	if _, err := c.CommitFiles(ctx, publish.Commit{Branch: "iacp/req-abcdef", Files: map[string][]byte{"../x": nil}}); !errors.Is(err, publish.ErrInvalidPath) {
		t.Fatalf("err = %v", err)
	}
	if err := c.DispatchWorkflow(ctx, publish.Dispatch{Workflow: "../../x.yml", Ref: "main"}); !errors.Is(err, publish.ErrInvalidPath) {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("unsafe input reached GitHub: %v", f.calls)
	}
}

func TestRetryDelay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		attempt int
		header  string
		want    time.Duration
		ok      bool
	}{
		{1, "", 500 * time.Millisecond, true},
		{3, "", 2 * time.Second, true},
		{20, "", maxBackoff, true},
		{1, "7", 7 * time.Second, true},
		{1, "61", 61 * time.Second, false},
		{1, now.Add(10 * time.Second).Format(http.TimeFormat), 10 * time.Second, true},
		{1, now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
	}
	for _, tt := range tests {
		h := http.Header{}
		if tt.header != "" {
			h.Set("Retry-After", tt.header)
		}
		got, ok := RetryDelay(tt.attempt, h, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("RetryDelay(%d, %q) = %s, %v", tt.attempt, tt.header, got, ok)
		}
	}
	if !Retryable(403, http.Header{"X-Ratelimit-Remaining": {"0"}}) || Retryable(403, http.Header{}) || Retryable(422, nil) {
		t.Error("Retryable classification is wrong")
	}
}
