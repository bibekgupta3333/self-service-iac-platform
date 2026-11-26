package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

const (
	testRepo     = "acme/self-service-iac-platform"
	testAudience = "https://api.iacp.acme.internal"
)

var (
	now     = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	keyOnce sync.Once
	key1    *rsa.PrivateKey
	key2    *rsa.PrivateKey
)

func keys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if key1, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if key2, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return key1, key2
}

func jwkOf(kid string, k *rsa.PublicKey) map[string]string {
	return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}
}

// jwksServer serves a JWKS that tests can rotate, and counts fetches.
type jwksServer struct {
	*httptest.Server
	mu      sync.Mutex
	keys    []map[string]string
	fetches atomic.Int32
	fail    atomic.Bool
	gate    chan struct{} // when set, each response waits until it is closed
}

func newJWKSServer(t *testing.T, keys ...map[string]string) *jwksServer {
	return newGatedJWKSServer(t, nil, keys...)
}

func newGatedJWKSServer(t *testing.T, gate chan struct{}, keys ...map[string]string) *jwksServer {
	s := &jwksServer{keys: keys, gate: gate}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		if s.gate != nil {
			<-s.gate
		}
		if s.fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": s.keys})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) set(keys ...map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func testPolicy(issuer string) Policy {
	return Policy{Issuer: issuer, Audience: testAudience, Repository: testRepo, RepositoryID: "87654321",
		Refs: DefaultRefs, WorkflowRefs: DefaultWorkflowRefs(testRepo)}
}

func validClaims() Claims {
	return Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: GitHubIssuer, Subject: "repo:" + testRepo + ":ref:refs/heads/main", Audience: jwt.ClaimStrings{testAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ID: "jti-1",
		},
		Repository: testRepo, RepositoryID: "87654321", RepositoryOwner: "acme", Ref: "refs/heads/main",
		SHA: "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4", JobWorkflowRef: testRepo + "/.github/workflows/apply.yml@refs/heads/main",
		RunID: "11873399021", RunAttempt: "1", EventName: "push",
	}
}

func sign(t *testing.T, k *rsa.PrivateKey, kid string, c Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestVerifier(t *testing.T, srv *jwksServer, minRefetch time.Duration) *Verifier {
	t.Helper()
	clock := platform.FixedClock{T: now}
	v, err := NewVerifier(testPolicy(GitHubIssuer), mustCache(t, srv.URL, clock, JWKSOptions{MinRefetch: minRefetch}), clock, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerify(t *testing.T) {
	k1, k2 := keys(t)
	srv := newJWKSServer(t, jwkOf("k1", &k1.PublicKey))
	v := newTestVerifier(t, srv, -1)

	pubDER, err := x509.MarshalPKIXPublicKey(&k1.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	tests := []struct {
		name      string
		token     func() string
		forbidden bool // workflow_not_allowed instead of an invalid token
		ok        bool
	}{
		{name: "valid", ok: true, token: func() string { return sign(t, k1, "k1", validClaims()) }},
		{name: "valid PR validation run", ok: true, token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) {
				c.Ref, c.JobWorkflowRef = "refs/pull/812/merge", testRepo+"/.github/workflows/pr-validate.yml@refs/pull/812/merge"
			}))
		}},
		{name: "expired within leeway", ok: true, token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-20 * time.Second)) }))
		}},
		{name: "wrong audience", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.Audience = jwt.ClaimStrings{"sts.amazonaws.com"} }))
		}},
		{name: "wrong issuer", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.Issuer = "https://evil.example" }))
		}},
		{name: "expired", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute)) }))
		}},
		{name: "no expiry", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.ExpiresAt = nil }))
		}},
		{name: "not yet valid", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.NotBefore = jwt.NewNumericDate(now.Add(2 * time.Minute)) }))
		}},
		{name: "issued in the future", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.IssuedAt = jwt.NewNumericDate(now.Add(2 * time.Minute)) }))
		}},
		{name: "wrong repository", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.Repository = "acme/other" }))
		}},
		{name: "wrong repository owner", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.RepositoryOwner = "evil" }))
		}},
		{name: "wrong repository id", token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.RepositoryID = "1" }))
		}},
		{name: "ref not allowed", forbidden: true, token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) { c.Ref = "refs/heads/feature" }))
		}},
		{name: "workflow from a PR branch", forbidden: true, token: func() string {
			return sign(t, k1, "k1", mutate(func(c *Claims) {
				c.Ref, c.JobWorkflowRef = "refs/pull/9/merge", testRepo+"/.github/workflows/apply.yml@refs/pull/9/merge"
			}))
		}},
		{name: "signed by another key", token: func() string { return sign(t, k2, "k1", validClaims()) }},
		{name: "no kid", token: func() string { return sign(t, k1, "", validClaims()) }},
		{name: "unknown kid", token: func() string { return sign(t, k2, "k9", validClaims()) }},
		{name: "alg none", token: func() string {
			s, err := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims()).SignedString(jwt.UnsafeAllowNoneSignatureType)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{name: "HS256 with the public key as secret", token: func() string {
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims())
			tok.Header["kid"] = "k1"
			s, err := tok.SignedString(pubPEM)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{name: "garbage", token: func() string { return "not.a.jwt" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := v.Verify(context.Background(), tt.token())
			switch {
			case tt.ok:
				if err != nil || c.Repository != testRepo {
					t.Fatalf("Verify = %+v, %v", c, err)
				}
			case tt.forbidden:
				if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindForbidden || e.Code != "workflow_not_allowed" {
					t.Fatalf("want workflow_not_allowed, got %v", err)
				}
			default:
				if !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("want ErrInvalidToken, got %v", err)
				}
			}
		})
	}
}

func TestUnknownKidTriggersRefetch(t *testing.T) {
	k1, k2 := keys(t)
	srv := newJWKSServer(t, jwkOf("k1", &k1.PublicKey))
	clock := &stepClock{t: now}
	v, err := NewVerifier(testPolicy(GitHubIssuer), mustCache(t, srv.URL, clock, JWKSOptions{MinRefetch: 30 * time.Second}), clock, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, k1, "k1", validClaims())); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, sign(t, k1, "k1", validClaims())); err != nil || srv.fetches.Load() != 1 {
		t.Fatalf("known kid must be served from cache: fetches=%d err=%v", srv.fetches.Load(), err)
	}

	// The issuer rotates in k2. A token with kid k2 makes the cache refetch once.
	srv.set(jwkOf("k1", &k1.PublicKey), jwkOf("k2", &k2.PublicKey))
	clock.t = now.Add(time.Minute) // past the refetch interval
	if _, err := v.Verify(ctx, sign(t, k2, "k2", validClaims())); err != nil || srv.fetches.Load() != 2 {
		t.Fatalf("rotated key: fetches=%d err=%v", srv.fetches.Load(), err)
	}

	// Forged kids inside the refetch interval do not reach the issuer again.
	for range 5 {
		if _, err := v.Verify(ctx, sign(t, k2, "forged", validClaims())); !errors.Is(err, ErrInvalidToken) || !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("forged kid: %v", err)
		}
	}
	if srv.fetches.Load() != 2 {
		t.Fatalf("fetches = %d, want 2 (rate limited)", srv.fetches.Load())
	}
}

func mutate(f func(*Claims)) Claims {
	c := validClaims()
	f(&c)
	return c
}

func TestJWKSCacheTTLAndOutage(t *testing.T) {
	k1, _ := keys(t)
	srv := newJWKSServer(t, jwkOf("k1", &k1.PublicKey))
	clock := &stepClock{t: now}
	c := mustCache(t, srv.URL, clock, JWKSOptions{TTL: time.Hour, MinRefetch: time.Minute})
	ctx := context.Background()
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	// After the TTL the cache refreshes; if the issuer is down, cached keys keep working.
	srv.fail.Store(true)
	clock.t = now.Add(2 * time.Hour)
	if _, err := c.Key(ctx, "k1"); err != nil || srv.fetches.Load() != 2 {
		t.Fatalf("stale key during outage: fetches=%d err=%v", srv.fetches.Load(), err)
	}
	if _, err := c.Key(ctx, "k2"); !errors.Is(err, ErrUnknownKey) || srv.fetches.Load() != 2 {
		t.Fatalf("unknown kid during outage within the interval: fetches=%d err=%v", srv.fetches.Load(), err)
	}
	clock.t = clock.t.Add(2 * time.Minute)
	if _, err := c.Key(ctx, "k2"); err == nil || srv.fetches.Load() != 3 {
		t.Fatalf("unknown kid after the interval must refetch and fail: fetches=%d err=%v", srv.fetches.Load(), err)
	}

	// A cold cache with the issuer down fails closed.
	cold := mustCache(t, srv.URL, clock, JWKSOptions{})
	if _, err := cold.Key(ctx, "k1"); err == nil {
		t.Fatal("cold cache with the issuer down must fail")
	}
}

func mustCache(t *testing.T, url string, clock platform.Clock, o JWKSOptions) *JWKSCache {
	t.Helper()
	c, err := NewJWKSCache(url, clock, o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A caller that disconnects mid-fetch must not fail the fetch for others or start the
// refetch rate limit (the cold-start / key-rotation denial of service).
func TestCancelledCallerDoesNotPoisonRefetch(t *testing.T) {
	k1, _ := keys(t)
	gate := make(chan struct{})
	srv := newGatedJWKSServer(t, gate, jwkOf("k1", &k1.PublicKey))
	c := mustCache(t, srv.URL, platform.FixedClock{T: now}, JWKSOptions{MinRefetch: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Key(ctx, "forged")
		done <- err
	}()
	for srv.fetches.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrUnknownKey) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	close(gate) // the detached fetch completes for everyone else
	got, err := c.Key(context.Background(), "k1")
	if err != nil || got.N.Cmp(k1.N) != 0 {
		t.Fatalf("next caller after a cancelled one: %v", err)
	}
	if srv.fetches.Load() != 1 {
		t.Fatalf("fetches = %d, want 1 (the cancelled caller's fetch was reused)", srv.fetches.Load())
	}
}

func TestConcurrentUnknownKidsShareOneFetch(t *testing.T) {
	k1, _ := keys(t)
	gate := make(chan struct{})
	srv := newGatedJWKSServer(t, gate, jwkOf("k1", &k1.PublicKey))
	// The rate limit is off, so only singleflight keeps this to one fetch.
	c := mustCache(t, srv.URL, platform.FixedClock{T: now}, JWKSOptions{MinRefetch: -1})

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.Key(context.Background(), "k1")
		}()
	}
	for srv.fetches.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let every caller join the in-flight fetch
	close(gate)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if srv.fetches.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", srv.fetches.Load())
	}
}

func TestJWKSEndpointHardening(t *testing.T) {
	for _, u := range []string{"http://issuer.example/jwks", "ftp://x/jwks", "/relative", "https://"} {
		if _, err := NewJWKSCache(u, platform.SystemClock{}, JWKSOptions{}); err == nil {
			t.Errorf("NewJWKSCache(%q) must be rejected", u)
		}
	}
	for _, u := range []string{"https://token.actions.githubusercontent.com/.well-known/jwks", "http://127.0.0.1:8081/jwks", "http://localhost/jwks"} {
		if _, err := NewJWKSCache(u, platform.SystemClock{}, JWKSOptions{}); err != nil {
			t.Errorf("NewJWKSCache(%q) = %v", u, err)
		}
	}

	k1, _ := keys(t)
	good := newJWKSServer(t, jwkOf("k1", &k1.PublicKey))
	redirect := httptest.NewServer(http.RedirectHandler(good.URL, http.StatusFound))
	defer redirect.Close()
	c := mustCache(t, redirect.URL, platform.FixedClock{T: now}, JWKSOptions{Client: &http.Client{}})
	if _, err := c.Key(context.Background(), "k1"); err == nil || good.fetches.Load() != 0 {
		t.Fatalf("redirect must not be followed: err=%v fetches=%d", err, good.fetches.Load())
	}

	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[` + strings.Repeat(" ", maxJWKSBytes) + `]}`))
	}))
	defer huge.Close()
	if _, err := mustCache(t, huge.URL, platform.FixedClock{T: now}, JWKSOptions{}).Key(context.Background(), "k1"); err == nil {
		t.Fatal("an oversized JWKS must be rejected")
	}
}

type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

func TestParseJWKS(t *testing.T) {
	k1, _ := keys(t)
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	enc := func(keys ...map[string]string) []byte {
		b, _ := json.Marshal(map[string]any{"keys": keys})
		return b
	}
	ec := map[string]string{"kty": "EC", "kid": "ec1"}
	enc1 := jwkOf("enc", &k1.PublicKey)
	enc1["use"] = "enc"
	ps := jwkOf("ps", &k1.PublicKey)
	ps["alg"] = "PS256"

	got, err := ParseJWKS(enc(jwkOf("k1", &k1.PublicKey), ec, enc1, ps))
	if err != nil || len(got) != 1 || got["k1"].N.Cmp(k1.N) != 0 || got["k1"].E != k1.E {
		t.Fatalf("ParseJWKS = %v, %v", got, err)
	}
	if _, err := ParseJWKS(enc(jwkOf("weak", &weak.PublicKey))); err == nil {
		t.Fatal("a 1024-bit key must be rejected")
	}
	if _, err := ParseJWKS([]byte(`{`)); err == nil {
		t.Fatal("malformed JWKS must fail")
	}
}

func TestPolicyValidate(t *testing.T) {
	ok := testPolicy(GitHubIssuer)
	tests := []struct {
		name string
		edit func(*Policy)
		ok   bool
	}{
		{name: "valid", edit: func(*Policy) {}, ok: true},
		{name: "loopback http issuer for tests", edit: func(p *Policy) { p.Issuer = "http://127.0.0.1:8081" }, ok: true},
		{name: "plain http issuer", edit: func(p *Policy) { p.Issuer = "http://issuer.example" }},
		{name: "no audience", edit: func(p *Policy) { p.Audience = "" }},
		{name: "bad repository", edit: func(p *Policy) { p.Repository = "acme" }},
		{name: "bad repository id", edit: func(p *Policy) { p.RepositoryID = "abc" }},
		{name: "empty refs", edit: func(p *Policy) { p.Refs = nil }},
		{name: "bad pattern", edit: func(p *Policy) { p.WorkflowRefs = []string{"[unclosed"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ok
			tt.edit(&p)
			if err := p.Validate(); tt.ok != (err == nil) {
				t.Fatalf("Validate = %v", err)
			}
		})
	}
	if _, err := NewVerifier(ok, nil, platform.FixedClock{T: now}, 10*time.Minute); err == nil {
		t.Fatal("leeway above MaxLeeway must be rejected")
	}
}

func TestClaimsIdentity(t *testing.T) {
	wf, err := validClaims().Identity()
	if err != nil || wf.RunID != 11873399021 || wf.RunAttempt != 1 || wf.Repository != testRepo ||
		wf.JobWorkflowRef != testRepo+"/.github/workflows/apply.yml@refs/heads/main" || wf.Ref != "refs/heads/main" {
		t.Fatalf("Identity = %+v, %v", wf, err)
	}
	for _, c := range []Claims{mutate(func(c *Claims) { c.RunID = "" }), mutate(func(c *Claims) { c.RunAttempt = "0" })} {
		if _, err := c.Identity(); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("Identity(%+v) = %v", c, err)
		}
	}
}
