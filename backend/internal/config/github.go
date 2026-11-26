package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// GitHubAuthConfig configures GitHub OAuth login, sessions and team sync. The four identity
// fields are required together: a half configuration fails loudly instead of leaving login
// in an unknown state.
type GitHubAuthConfig struct {
	ClientID     string // IACP_GITHUB_CLIENT_ID
	ClientSecret string // IACP_GITHUB_CLIENT_SECRET
	CallbackURL  string // IACP_GITHUB_CALLBACK_URL: the OAuth app's registered callback
	Org          string // IACP_GITHUB_ORG: only active members of this organisation may sign in
	// AdminTeam is the GitHub team slug whose members become platform admins.
	AdminTeam string // IACP_AUTH_ADMIN_TEAM, default iacp-admins
	// RedirectAllowlist lists absolute origins a login may return to (IACP_AUTH_REDIRECT_ALLOWLIST).
	// Relative paths are always allowed.
	RedirectAllowlist []string
	// DefaultRedirect is where a login lands when no valid return_to is given.
	DefaultRedirect string // IACP_AUTH_DEFAULT_REDIRECT, default "/"
	SessionTTL      time.Duration
	SessionIdleTTL  time.Duration
}

// SecureCookies reports whether session cookies carry the Secure attribute: always, except
// when the callback is plain http on a loopback host (local development). Pure.
func (g GitHubAuthConfig) SecureCookies() bool {
	u, err := url.Parse(g.CallbackURL)
	return err != nil || u.Scheme != "http" || !isLoopbackHost(u.Hostname())
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func loadGitHubAuth(get func(name, def string) string, mode AuthMode) (GitHubAuthConfig, error) {
	g := GitHubAuthConfig{
		ClientID:        get("GITHUB_CLIENT_ID", ""),
		ClientSecret:    get("GITHUB_CLIENT_SECRET", ""),
		CallbackURL:     get("GITHUB_CALLBACK_URL", ""),
		Org:             get("GITHUB_ORG", ""),
		AdminTeam:       get("AUTH_ADMIN_TEAM", "iacp-admins"),
		DefaultRedirect: get("AUTH_DEFAULT_REDIRECT", "/"),
	}
	var errs []error
	for _, o := range splitList(get("AUTH_REDIRECT_ALLOWLIST", "")) {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) ||
			u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%sAUTH_REDIRECT_ALLOWLIST: %q must be an https origin (http only for loopback)", Prefix, o))
			continue
		}
		g.RedirectAllowlist = append(g.RedirectAllowlist, u.Scheme+"://"+u.Host)
	}
	var err error
	if g.SessionTTL, err = time.ParseDuration(get("AUTH_SESSION_TTL", "8h")); err != nil || g.SessionTTL < time.Minute || g.SessionTTL > 720*time.Hour {
		errs = append(errs, fmt.Errorf("%sAUTH_SESSION_TTL must be a duration between 1m and 720h", Prefix))
	}
	if g.SessionIdleTTL, err = time.ParseDuration(get("AUTH_SESSION_IDLE_TTL", "1h")); err != nil || g.SessionIdleTTL < time.Minute {
		errs = append(errs, fmt.Errorf("%sAUTH_SESSION_IDLE_TTL must be a duration of at least 1m", Prefix))
	} else if g.SessionIdleTTL > g.SessionTTL {
		g.SessionIdleTTL = g.SessionTTL
	}
	if !strings.HasPrefix(g.DefaultRedirect, "/") || strings.HasPrefix(g.DefaultRedirect, "//") || strings.Contains(g.DefaultRedirect, `\`) {
		errs = append(errs, fmt.Errorf("%sAUTH_DEFAULT_REDIRECT must be a path starting with a single /", Prefix))
	}

	set := 0
	for _, v := range []string{g.ClientID, g.ClientSecret, g.CallbackURL, g.Org} {
		if v != "" {
			set++
		}
	}
	switch {
	case mode == AuthModeGitHub && set < 4, mode != AuthModeGitHub && set > 0 && set < 4:
		errs = append(errs, fmt.Errorf("%[1]sGITHUB_CLIENT_ID, %[1]sGITHUB_CLIENT_SECRET, %[1]sGITHUB_CALLBACK_URL and %[1]sGITHUB_ORG must all be set together (required for %[1]sAUTH_MODE=github)", Prefix))
	case set == 4:
		u, err := url.Parse(g.CallbackURL)
		switch {
		case err != nil || u.Host == "" || u.User != nil || u.Fragment != "":
			errs = append(errs, fmt.Errorf("%sGITHUB_CALLBACK_URL is not a valid absolute URL", Prefix))
		case u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())):
			errs = append(errs, fmt.Errorf("%sGITHUB_CALLBACK_URL must be https (http is allowed for loopback hosts only)", Prefix))
		}
		if strings.ContainsAny(g.Org, "/ ") {
			errs = append(errs, fmt.Errorf("%sGITHUB_ORG must be an organisation login", Prefix))
		}
	}
	return g, errors.Join(errs...)
}
