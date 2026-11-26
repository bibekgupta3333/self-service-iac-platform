package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// GitHubIdentity is the port to GitHub for login. The production adapter is GitHubClient;
// tests use an httptest server behind the same adapter, or a fake of this interface.
type GitHubIdentity interface {
	// AuthorizeURL is where the browser is sent to sign in.
	AuthorizeURL(state, codeChallenge string) string
	// Authenticate exchanges the authorization code (with the PKCE verifier) and returns
	// who signed in, whether they belong to the allowed organisation, and their team slugs.
	// The access token never leaves the call.
	Authenticate(ctx context.Context, code, verifier string) (identity.GitHubUser, error)
}

// GitHubConfig is what the adapter needs.
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
	// Org is the organisation whose active members may sign in.
	Org string
}

// Default GitHub endpoints. They are the only hosts the production adapter talks to.
const (
	DefaultWebURL = "https://github.com"
	DefaultAPIURL = "https://api.github.com"
)

const (
	requestTimeout = 10 * time.Second
	maxResponse    = 1 << 20
	maxRedirects   = 3
	teamsPerPage   = 100
	maxTeamPages   = 10
	scopes         = "read:user user:email read:org"
)

// ErrGitHub wraps a failure talking to GitHub; the message never contains the code, the
// token or the client secret.
var ErrGitHub = errors.New("github request failed")

// GitHubClient is the net/http adapter for GitHubIdentity. It only ever builds URLs from its
// two fixed base URLs, refuses any other host, never follows a redirect to another host,
// bounds every call with a timeout and caps response sizes.
type GitHubClient struct {
	cfg      GitHubConfig
	web, api *url.URL
	hc       *http.Client
	loopback bool
}

// GitHubOption customises NewGitHubClient.
type GitHubOption func(*GitHubClient)

// WithEndpoints points the adapter at other base URLs (a test server or GitHub Enterprise).
// Only the hosts of these URLs are then allowed.
func WithEndpoints(web, api string) GitHubOption {
	return func(c *GitHubClient) {
		c.web, _ = url.Parse(web)
		c.api, _ = url.Parse(api)
	}
}

// AllowLoopbackHTTP permits plain http, for loopback hosts only (tests with httptest).
func AllowLoopbackHTTP() GitHubOption { return func(c *GitHubClient) { c.loopback = true } }

// NewGitHubClient returns the adapter. It fails on an incomplete config or unusable endpoints.
func NewGitHubClient(cfg GitHubConfig, opts ...GitHubOption) (*GitHubClient, error) {
	web, _ := url.Parse(DefaultWebURL)
	api, _ := url.Parse(DefaultAPIURL)
	c := &GitHubClient{cfg: cfg, web: web, api: api}
	for _, o := range opts {
		o(c)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.CallbackURL == "" || cfg.Org == "" {
		return nil, errors.New("github client: client id, secret, callback URL and org are required")
	}
	for _, u := range []*url.URL{c.web, c.api} {
		if u == nil || u.Host == "" || !c.schemeOK(u) {
			return nil, errors.New("github client: endpoints must be https URLs")
		}
	}
	c.hc = &http.Client{Timeout: requestTimeout, CheckRedirect: c.checkRedirect}
	return c, nil
}

func (c *GitHubClient) schemeOK(u *url.URL) bool {
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme == "http" && c.loopback {
		ip := net.ParseIP(u.Hostname())
		return u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return false
}

func (c *GitHubClient) hostAllowed(u *url.URL) bool {
	return c.schemeOK(u) && (strings.EqualFold(u.Host, c.web.Host) || strings.EqualFold(u.Host, c.api.Host))
}

func (c *GitHubClient) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: too many redirects", ErrGitHub)
	}
	if !c.hostAllowed(req.URL) {
		return fmt.Errorf("%w: redirect to a host that is not allowed", ErrGitHub)
	}
	// Never forward the bearer token to a different host than the one it was sent to.
	if via[0].URL.Host != req.URL.Host {
		return fmt.Errorf("%w: cross-host redirect refused", ErrGitHub)
	}
	return nil
}

// AuthorizeURL implements GitHubIdentity.
func (c *GitHubClient) AuthorizeURL(state, codeChallenge string) string {
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {c.cfg.CallbackURL},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	u := *c.web
	u.Path = strings.TrimRight(u.Path, "/") + "/login/oauth/authorize"
	u.RawQuery = q.Encode()
	return u.String()
}

// Authenticate implements GitHubIdentity.
func (c *GitHubClient) Authenticate(ctx context.Context, code, verifier string) (identity.GitHubUser, error) {
	token, err := c.exchange(ctx, code, verifier)
	if err != nil {
		return identity.GitHubUser{}, err
	}
	var prof struct {
		ID    int64   `json:"id"`
		Login string  `json:"login"`
		Name  *string `json:"name"`
		Email *string `json:"email"`
	}
	if err := c.apiGet(ctx, token, "/user", nil, &prof); err != nil {
		return identity.GitHubUser{}, err
	}
	if prof.ID <= 0 || prof.Login == "" {
		return identity.GitHubUser{}, fmt.Errorf("%w: malformed profile", ErrGitHub)
	}
	u := identity.GitHubUser{ID: prof.ID, Login: prof.Login, Name: deref(prof.Name), Email: deref(prof.Email)}

	member, err := c.orgMember(ctx, token)
	if err != nil {
		return identity.GitHubUser{}, err
	}
	u.OrgMember = member
	if !member {
		return u, nil // no further calls for someone who may not sign in
	}
	if u.Email == "" {
		u.Email = c.primaryEmail(ctx, token) // best effort: a missing email is not fatal
	}
	if u.TeamSlugs, err = c.teams(ctx, token); err != nil {
		return identity.GitHubUser{}, err
	}
	return u, nil
}

func (c *GitHubClient) exchange(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {c.cfg.CallbackURL},
		"code_verifier": {verifier},
	}
	u := *c.web
	u.Path = strings.TrimRight(u.Path, "/") + "/login/oauth/access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: build token request", ErrGitHub)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if out.Error != "" || out.AccessToken == "" {
		return "", fmt.Errorf("%w: token exchange rejected (%s)", ErrGitHub, sanitize(out.Error))
	}
	return out.AccessToken, nil
}

// orgMember reports whether the signed-in user is an active member of the allowed org.
func (c *GitHubClient) orgMember(ctx context.Context, token string) (bool, error) {
	var m struct {
		State string `json:"state"`
	}
	err := c.apiGet(ctx, token, "/user/memberships/orgs/"+url.PathEscape(c.cfg.Org), nil, &m)
	var se *statusError
	if errors.As(err, &se) && (se.status == http.StatusNotFound || se.status == http.StatusForbidden) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return m.State == "active", nil
}

func (c *GitHubClient) primaryEmail(ctx context.Context, token string) string {
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := c.apiGet(ctx, token, "/user/emails", nil, &emails); err != nil {
		return ""
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	return ""
}

// teams lists the slugs of the user's teams in the allowed org. Pages are requested by
// number from the fixed base URL; Link headers are never followed.
func (c *GitHubClient) teams(ctx context.Context, token string) ([]string, error) {
	var slugs []string
	for page := 1; page <= maxTeamPages; page++ {
		var batch []struct {
			Slug string `json:"slug"`
			Org  struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		q := url.Values{"per_page": {strconv.Itoa(teamsPerPage)}, "page": {strconv.Itoa(page)}}
		if err := c.apiGet(ctx, token, "/user/teams", q, &batch); err != nil {
			return nil, err
		}
		for _, t := range batch {
			if strings.EqualFold(t.Org.Login, c.cfg.Org) && t.Slug != "" {
				slugs = append(slugs, t.Slug)
			}
		}
		if len(batch) < teamsPerPage {
			break
		}
	}
	return slugs, nil
}

func (c *GitHubClient) apiGet(ctx context.Context, token, path string, q url.Values, out any) error {
	u := *c.api
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: build request", ErrGitHub)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return c.do(req, out)
}

type statusError struct{ status int }

func (e *statusError) Error() string { return fmt.Sprintf("%v: status %d", ErrGitHub, e.status) }
func (e *statusError) Unwrap() error { return ErrGitHub }

func (c *GitHubClient) do(req *http.Request, out any) error {
	if !c.hostAllowed(req.URL) {
		return fmt.Errorf("%w: host %q is not allowed", ErrGitHub, req.URL.Host)
	}
	req.Header.Set("User-Agent", "iacp-backend")
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL: it can carry the authorization code
		}
		return fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return fmt.Errorf("%w: unreadable or oversized response", ErrGitHub)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{status: resp.StatusCode}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: malformed response", ErrGitHub)
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sanitize keeps an OAuth error code short and printable for logs.
func sanitize(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, s)
}
