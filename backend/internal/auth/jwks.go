package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// ErrUnknownKey means the JWKS has no usable key with the requested kid, even after a refetch.
var ErrUnknownKey = errors.New("unknown signing key")

// maxJWKSBytes bounds the JWKS response.
const maxJWKSBytes = 1 << 20

// minRSABits rejects weak keys published by a misconfigured or hostile JWKS endpoint.
const minRSABits = 2048

// DefaultFetchTimeout bounds one JWKS fetch.
const DefaultFetchTimeout = 10 * time.Second

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// ParseJWKS extracts the RS256 signing keys of a JWKS document, keyed by kid. Keys of
// other types or uses are skipped. Pure.
func ParseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		pub, err := rsaKey(k)
		if err != nil {
			return nil, fmt.Errorf("JWKS key %q: %w", k.Kid, err)
		}
		keys[k.Kid] = pub
	}
	return keys, nil
}

func rsaKey(k jwk) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("bad exponent")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	if pub.N.BitLen() < minRSABits {
		return nil, fmt.Errorf("modulus is %d bits, want at least %d", pub.N.BitLen(), minRSABits)
	}
	if pub.E < 3 || pub.E%2 == 0 {
		return nil, errors.New("bad exponent")
	}
	return pub, nil
}

// CheckEndpointURL accepts https URLs, and plain http only on the loopback interface
// (tests, local development). Pure.
func CheckEndpointURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not an absolute URL", raw)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%q must use https", raw)
}

func isLoopbackHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// noRedirect stops the JWKS client from following redirects, so neither an https→http
// downgrade nor an off-host redirect can substitute the key set. A 3xx is a fetch error.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// JWKSCache serves signing keys from a JWKS URL.
//
//   - Keys are cached for TTL.
//   - A token with an unknown kid (key rotation) triggers a refetch, at most once per
//     MinRefetch after the previous completed attempt, so forged kids cannot make the API
//     hammer the issuer.
//   - Concurrent misses share one fetch (singleflight). The mutex is never held during I/O.
//   - The fetch runs on a context detached from the caller (with its own timeout), so a
//     caller that disconnects cannot fail the fetch or start the rate limit.
//   - When a refetch fails, cached keys keep working.
type JWKSCache struct {
	url          string
	client       *http.Client
	clock        platform.Clock
	ttl          time.Duration
	minRefetch   time.Duration
	fetchTimeout time.Duration
	flight       singleflight.Group

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time // end of the last completed fetch attempt
}

// JWKSOptions tunes a JWKSCache. Zero values take the defaults.
type JWKSOptions struct {
	TTL          time.Duration // default 1h
	MinRefetch   time.Duration // default 30s; negative disables the rate limit (tests)
	FetchTimeout time.Duration // default DefaultFetchTimeout
	Client       *http.Client  // default: a client with no redirects; redirects are always disabled
}

// NewJWKSCache returns a cache over rawURL, which must pass CheckEndpointURL.
func NewJWKSCache(rawURL string, clock platform.Clock, o JWKSOptions) (*JWKSCache, error) {
	if err := CheckEndpointURL(rawURL); err != nil {
		return nil, fmt.Errorf("jwks url: %w", err)
	}
	if o.TTL <= 0 {
		o.TTL = time.Hour
	}
	if o.MinRefetch == 0 {
		o.MinRefetch = 30 * time.Second
	}
	if o.FetchTimeout <= 0 {
		o.FetchTimeout = DefaultFetchTimeout
	}
	client := &http.Client{}
	if o.Client != nil {
		copied := *o.Client
		client = &copied
	}
	client.CheckRedirect = noRedirect
	return &JWKSCache{url: rawURL, client: client, clock: clock, ttl: o.TTL, minRefetch: o.MinRefetch,
		fetchTimeout: o.FetchTimeout}, nil
}

// Key implements KeySource.
func (c *JWKSCache) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	key, fresh, mayFetch := c.lookup(kid)
	if key != nil && fresh {
		return key, nil
	}
	var fetchErr error
	if mayFetch {
		detached := context.WithoutCancel(ctx)
		ch := c.flight.DoChan("jwks", func() (any, error) { return nil, c.refresh(detached) })
		select {
		case res := <-ch:
			fetchErr = res.Err
		case <-ctx.Done():
			// The caller left; the shared fetch carries on for everyone else.
			if key != nil {
				return key, nil
			}
			return nil, fmt.Errorf("%w: kid %q: %w", ErrUnknownKey, kid, ctx.Err())
		}
	}
	if key, _, _ = c.lookup(kid); key != nil {
		return key, nil
	}
	if fetchErr != nil {
		return nil, fmt.Errorf("%w: kid %q: %w", ErrUnknownKey, kid, fetchErr)
	}
	return nil, fmt.Errorf("%w: kid %q", ErrUnknownKey, kid)
}

// lookup reads the cache: the key (nil if absent), whether the cache is within its TTL,
// and whether the rate limit allows a fetch now.
func (c *JWKSCache) lookup(kid string) (*rsa.PublicKey, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	fresh := c.keys != nil && now.Sub(c.fetchedAt) < c.ttl
	mayFetch := c.lastAttempt.IsZero() || c.minRefetch < 0 || now.Sub(c.lastAttempt) >= c.minRefetch
	return c.keys[kid], fresh, mayFetch
}

// refresh fetches the key set and records the attempt. Only an attempt that ran to an
// outcome (success, error or our own timeout) starts the rate limit; a cancelled one does not.
func (c *JWKSCache) refresh(ctx context.Context) error {
	fctx, cancel := context.WithTimeout(ctx, c.fetchTimeout)
	defer cancel()
	keys, err := c.fetch(fctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if !errors.Is(err, context.Canceled) {
		c.lastAttempt = now
	}
	if err == nil {
		c.keys, c.fetchedAt = keys, now
	}
	return err
}

func (c *JWKSCache) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("build JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch JWKS: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read JWKS: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return nil, errors.New("JWKS response too large")
	}
	return ParseJWKS(body)
}
