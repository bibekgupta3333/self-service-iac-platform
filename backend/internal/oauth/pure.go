// Package oauth implements the GitHub OAuth authorization-code login with state and PKCE.
//
// Pure helpers (PKCE, redirect policy, error mapping) live in this file; GitHub access sits
// behind the GitHubIdentity port with an HTTP adapter (github.go); Service orchestrates a
// login (service.go).
package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const entropyBytes = 32

// NewToken returns 32 random bytes as base64url, used for both the state and the PKCE
// verifier (43 characters, within RFC 7636's 43..128). Pure apart from reading r.
func NewToken(r io.Reader) (string, error) {
	b := make([]byte, entropyBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidToken reports whether s has the shape NewToken produces. Pure.
func ValidToken(s string) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(entropyBytes) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == entropyBytes
}

// Challenge is the S256 PKCE code challenge for verifier (RFC 7636 section 4.2). Pure.
func Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// HashState is the stored form of a state value. Pure.
func HashState(state string) []byte {
	h := sha256.Sum256([]byte(state))
	return h[:]
}

// StatesMatch compares the state from the query with the one bound to the browser by
// cookie, in constant time. Both must be well-formed. Pure.
func StatesMatch(query, cookie string) bool {
	return ValidToken(query) && ValidToken(cookie) && subtle.ConstantTimeCompare([]byte(query), []byte(cookie)) == 1
}

// RedirectPolicy decides where a finished login may send the browser, so the login cannot
// be used as an open redirect.
type RedirectPolicy struct {
	// Origins are the absolute origins ("https://portal.example.test") a login may return to.
	Origins []string
	// Default is used when the requested target is empty or not allowed; it must be a path.
	Default string
}

// Resolve returns target when it is a safe same-site path or an absolute URL on an allowed
// origin, and the default otherwise. Pure.
func (p RedirectPolicy) Resolve(target string) string {
	if target == "" || len(target) > 2048 || strings.ContainsAny(target, "\\\r\n\t") || hasControl(target) {
		return p.Default
	}
	if target[0] == '/' {
		// "//host" and "/\host" are scheme-relative to browsers.
		if strings.HasPrefix(target, "//") {
			return p.Default
		}
		return target
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return p.Default
	}
	origin := u.Scheme + "://" + u.Host
	for _, o := range p.Origins {
		if strings.EqualFold(o, origin) {
			return u.String()
		}
	}
	return p.Default
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// LoginErrorCode maps a login failure to the fixed code shown to the browser as
// ?login_error=<code>. Pure.
func LoginErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalidState):
		return "invalid_state"
	case errors.Is(err, ErrNotOrgMember):
		return "not_org_member"
	case errors.Is(err, ErrUserDisabled):
		return "user_disabled"
	case errors.Is(err, ErrGitHubUnavailable):
		return "github_unavailable"
	}
	return "login_failed"
}
