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
	"sync"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// ErrUnknownKey means the JWKS has no usable key with the requested kid, even after a refetch.
var ErrUnknownKey = errors.New("unknown signing key")

// maxJWKSBytes bounds the JWKS response.
const maxJWKSBytes = 1 << 20

// minRSABits rejects weak keys published by a misconfigured or hostile JWKS endpoint.
const minRSABits = 2048

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

// JWKSCache serves signing keys from a JWKS URL. Keys are cached for TTL; a token with an
// unknown kid (key rotation) triggers a refetch, at most once per MinRefetch, so forged
// kids cannot make the API hammer the issuer. When a refetch fails, cached keys keep working.
type JWKSCache struct {
	url        string
	client     *http.Client
	clock      platform.Clock
	ttl        time.Duration
	minRefetch time.Duration

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// JWKSOptions tunes a JWKSCache. Zero values take the defaults.
type JWKSOptions struct {
	TTL        time.Duration // default 1h
	MinRefetch time.Duration // default 30s; negative disables the rate limit (tests)
	Client     *http.Client  // default: 10 s timeout
}

// NewJWKSCache returns a cache over url.
func NewJWKSCache(url string, clock platform.Clock, o JWKSOptions) *JWKSCache {
	if o.TTL <= 0 {
		o.TTL = time.Hour
	}
	if o.MinRefetch == 0 {
		o.MinRefetch = 30 * time.Second
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWKSCache{url: url, client: o.Client, clock: clock, ttl: o.TTL, minRefetch: o.MinRefetch}
}

// Key implements KeySource.
func (c *JWKSCache) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	fresh := c.keys != nil && now.Sub(c.fetchedAt) < c.ttl
	if k, ok := c.keys[kid]; ok && fresh {
		return k, nil
	}
	if c.lastAttempt.IsZero() || now.Sub(c.lastAttempt) >= c.minRefetch {
		c.lastAttempt = now
		keys, err := c.fetch(ctx)
		if err == nil {
			c.keys, c.fetchedAt = keys, now
		} else if _, ok := c.keys[kid]; !ok {
			return nil, err
		}
	}
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: kid %q", ErrUnknownKey, kid)
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
