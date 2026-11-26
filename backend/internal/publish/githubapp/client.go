// Package githubapp is the GitHub App adapter of publish.Publisher.
//
// It authenticates as the App with a short-lived RS256 JWT, exchanges it for an
// installation token (cached until shortly before it expires), and writes through the
// REST API: the git data API (blobs, tree, commit, ref) commits every generated file
// atomically on the request branch, then the pulls, issues and actions APIs open the
// PR, label it and dispatch workflows.
//
// The HTTP client has a per-request timeout, refuses redirects to another host, caps
// response bodies, and retries 5xx, 429 and secondary rate limits with backoff,
// honouring Retry-After.
package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// DefaultAPIURL is the public GitHub REST API.
const DefaultAPIURL = "https://api.github.com"

// Defaults for Config fields left zero.
const (
	DefaultTimeout     = 15 * time.Second
	DefaultMaxAttempts = 4
	// tokenRefreshMargin renews the installation token this long before it expires.
	tokenRefreshMargin = 5 * time.Minute
	// maxRetryAfter is the longest Retry-After the client waits for; longer means fail now.
	maxRetryAfter = 60 * time.Second
	maxBackoff    = 30 * time.Second
	maxBodyBytes  = 10 << 20
	apiVersion    = "2022-11-28"
)

// Config configures the client. Owner, Repo and BaseBranch name the repository the
// platform writes to.
type Config struct {
	AppID          int64
	InstallationID int64
	PrivateKey     []byte // PEM (PKCS#1 or PKCS#8) RSA key of the App
	Owner          string
	Repo           string
	BaseBranch     string
	APIURL         string        // default DefaultAPIURL; plain http only on loopback (tests)
	Timeout        time.Duration // per HTTP request; default DefaultTimeout
	MaxAttempts    int           // per call, including the first; default DefaultMaxAttempts
}

// APIError is a non-2xx GitHub response that was not retried (or ran out of retries).
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// IsStatus reports whether err wraps an APIError with the given status.
func IsStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

// ErrRedirect is returned when GitHub redirects to another host or too many times.
var ErrRedirect = errors.New("github: redirect refused")

// Client talks to one repository as one App installation.
type Client struct {
	cfg   Config
	key   *rsa.PrivateKey
	base  *url.URL
	http  *http.Client
	clock platform.Clock
	sleep func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex // guards token and tokenExp; held while a token is fetched
	token    string
	tokenExp time.Time
}

// Option customises a Client.
type Option func(*Client)

// WithSleep replaces the backoff sleep (tests record delays instead of waiting).
func WithSleep(f func(ctx context.Context, d time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

// New validates cfg and returns a Client.
func New(cfg Config, clock platform.Clock, opts ...Option) (*Client, error) {
	switch {
	case cfg.AppID <= 0, cfg.InstallationID <= 0:
		return nil, errors.New("githubapp: app id and installation id must be positive")
	case cfg.Owner == "", cfg.Repo == "", cfg.BaseBranch == "":
		return nil, errors.New("githubapp: owner, repo and base branch are required")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	base, err := ParseAPIURL(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	c := &Client{cfg: cfg, key: key, base: base, clock: clock, sleep: sleepCtx}
	c.http = &http.Client{Timeout: cfg.Timeout, CheckRedirect: sameHostRedirects(base)}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// ParseAPIURL accepts an https URL, or plain http on a loopback host. Pure.
func ParseAPIURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("githubapp: invalid API URL %q", raw)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		return nil, fmt.Errorf("githubapp: API URL %q must use https (plain http only on loopback)", raw)
	}
	return u, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameHostRedirects follows at most two redirects, and only to the API host itself.
func sameHostRedirects(base *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > 2 || req.URL.Scheme != base.Scheme || req.URL.Host != base.Host {
			return fmt.Errorf("%w: to %s", ErrRedirect, req.URL.Redacted())
		}
		return nil
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// appJWT signs the App's authentication JWT: iat 60 s in the past for clock skew, and
// exp 9 minutes ahead (GitHub allows at most 10).
func (c *Client) appJWT() (string, error) {
	now := c.clock.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(c.cfg.AppID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
	})
	s, err := tok.SignedString(c.key)
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app jwt: %w", err)
	}
	return s, nil
}

// installationToken returns a cached installation token, fetching a new one when the
// cached one is missing or within tokenRefreshMargin of expiry. The token is scoped to
// the configured repository.
func (c *Client) installationToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.clock.Now().Before(c.tokenExp.Add(-tokenRefreshMargin)) {
		return c.token, nil
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", c.cfg.InstallationID)
	body := map[string]any{"repositories": []string{c.cfg.Repo}}
	if err := c.do(ctx, http.MethodPost, path, body, &out, authApp); err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}
	if out.Token == "" || out.ExpiresAt.IsZero() {
		return "", errors.New("installation token: empty token in response")
	}
	c.token, c.tokenExp = out.Token, out.ExpiresAt
	return c.token, nil
}

func (c *Client) dropToken() {
	c.mu.Lock()
	c.token, c.tokenExp = "", time.Time{}
	c.mu.Unlock()
}

type authKind int

const (
	authInstallation authKind = iota
	authApp
)

// do sends one API call with retries. body (if non-nil) is JSON-encoded once and resent
// on each attempt; a 2xx response is decoded into out when out is non-nil.
func (c *Client) do(ctx context.Context, method, path string, body, out any, auth authKind) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
	}
	refreshed := false
	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		status, header, respBody, err := c.send(ctx, method, path, payload, auth)
		switch {
		case err != nil:
			if ctx.Err() != nil || errors.Is(err, ErrRedirect) {
				return err
			}
			lastErr, header = err, nil // transport error: retry with backoff
		case status >= 200 && status < 300:
			if out != nil && len(respBody) > 0 {
				if err := json.Unmarshal(respBody, out); err != nil {
					return fmt.Errorf("decode %s %s: %w", method, path, err)
				}
			}
			return nil
		case status == http.StatusUnauthorized && auth == authInstallation && !refreshed:
			// The cached token was revoked or expired early: fetch a new one, once.
			c.dropToken()
			refreshed = true
			attempt--
			continue
		default:
			apiErr := &APIError{Method: method, Path: path, Status: status, Message: errorMessage(respBody)}
			if !Retryable(status, header) {
				return apiErr
			}
			lastErr = apiErr
		}
		if attempt == c.cfg.MaxAttempts {
			break
		}
		delay, ok := RetryDelay(attempt, header, c.clock.Now())
		if !ok {
			return fmt.Errorf("%w (Retry-After exceeds %s)", lastErr, maxRetryAfter)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return fmt.Errorf("%w (retry interrupted: %w)", lastErr, err)
		}
	}
	return fmt.Errorf("after %d attempts: %w", c.cfg.MaxAttempts, lastErr)
}

func (c *Client) send(ctx context.Context, method, path string, payload []byte, auth authKind) (int, http.Header, []byte, error) {
	var bearer string
	var err error
	if auth == authApp {
		bearer, err = c.appJWT()
	} else {
		bearer, err = c.installationToken(ctx)
	}
	if err != nil {
		return 0, nil, nil, err
	}
	u := *c.base
	rel, err := url.Parse(path)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build url %s: %w", path, err)
	}
	u.Path = strings.TrimRight(c.base.Path, "/") + rel.Path
	u.RawPath = ""
	if rp := rel.EscapedPath(); rp != rel.Path {
		u.RawPath = strings.TrimRight(c.base.EscapedPath(), "/") + rp
	}
	u.RawQuery = rel.RawQuery
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "iacp-backend")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	if len(body) > maxBodyBytes {
		return 0, nil, nil, fmt.Errorf("%s %s: response exceeds %d bytes", method, path, maxBodyBytes)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// Retryable reports whether a response status is worth retrying: 5xx, 429, and 403
// secondary rate limits (Retry-After set, or no remaining quota). Pure.
func Retryable(status int, h http.Header) bool {
	switch {
	case status >= 500, status == http.StatusTooManyRequests:
		return true
	case status == http.StatusForbidden:
		return h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0"
	}
	return false
}

// RetryDelay is the wait before the next attempt: Retry-After (seconds or HTTP date) when
// present, else exponential backoff from 500 ms capped at 30 s. ok is false when the server
// asks for longer than maxRetryAfter. Pure.
func RetryDelay(attempt int, h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		var d time.Duration
		if secs, err := strconv.Atoi(v); err == nil {
			d = time.Duration(secs) * time.Second
		} else if at, err := http.ParseTime(v); err == nil {
			d = at.Sub(now)
		}
		if d < 0 {
			d = 0
		}
		return d, d <= maxRetryAfter
	}
	d := 500 * time.Millisecond << (attempt - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d, true
}

// errorMessage extracts GitHub's error message (plus field errors) from a response body.
func errorMessage(body []byte) string {
	var e struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || e.Message == "" {
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, raw := range e.Errors {
			parts = append(parts, string(raw))
		}
		return e.Message + ": " + strings.Join(parts, "; ")
	}
	return e.Message
}
