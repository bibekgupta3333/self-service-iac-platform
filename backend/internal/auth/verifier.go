package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// KeySource resolves a signing key by kid (implemented by JWKSCache).
type KeySource interface {
	Key(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

// MaxLeeway bounds the clock skew tolerated on exp, nbf and iat.
const MaxLeeway = 5 * time.Minute

// Verifier validates GitHub Actions OIDC tokens.
//
// Order (docs/03 §6.9): RS256 signature against the JWKS, iss, aud, exp/nbf/iat with a
// small leeway, then the repository and workflow allow-lists.
type Verifier struct {
	policy Policy
	keys   KeySource
	parser *jwt.Parser
}

// NewVerifier returns a Verifier, or an error if the policy is incomplete.
func NewVerifier(p Policy, keys KeySource, clock platform.Clock, leeway time.Duration) (*Verifier, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("oidc policy: %w", err)
	}
	if leeway < 0 || leeway > MaxLeeway {
		return nil, fmt.Errorf("oidc leeway %s must be between 0 and %s", leeway, MaxLeeway)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), // rejects none, HS256 and friends
		jwt.WithIssuer(p.Issuer),
		jwt.WithAudience(p.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway),
		jwt.WithTimeFunc(clock.Now),
	)
	return &Verifier{policy: p, keys: keys, parser: parser}, nil
}

// Verify returns the claims of a valid token. Failures wrap ErrInvalidToken, except a
// genuine token from a workflow outside the allow-list, which is an apperr Forbidden
// with code workflow_not_allowed.
func (v *Verifier) Verify(ctx context.Context, raw string) (Claims, error) {
	var c Claims
	_, err := v.parser.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token header has no kid")
		}
		return v.keys.Key(ctx, kid)
	})
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if err := CheckClaims(v.policy, c); err != nil {
		return Claims{}, err
	}
	return c, nil
}
