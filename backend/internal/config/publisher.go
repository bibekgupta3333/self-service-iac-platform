package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// PublisherKind selects the publish.Publisher adapter (IACP_PUBLISHER).
type PublisherKind string

// Publisher kinds.
const (
	// PublisherNone opens no PRs: requests stay in draft (the behaviour before WBS 4.8).
	PublisherNone PublisherKind = "none"
	// PublisherLocal commits to a local clone and records a fake PR (ADR-0009).
	PublisherLocal PublisherKind = "local"
	// PublisherGitHub writes through the GitHub App.
	PublisherGitHub PublisherKind = "github"
)

// DefaultGitHubAPIURL is the public GitHub REST API.
const DefaultGitHubAPIURL = "https://api.github.com"

// MinWebhookSecretLen is the minimum length of IACP_GITHUB_WEBHOOK_SECRET.
const MinWebhookSecretLen = 16

// PublisherConfig configures how generated stacks reach the repository.
type PublisherConfig struct {
	Kind       PublisherKind // IACP_PUBLISHER
	BaseBranch string        // IACP_PUBLISH_BASE_BRANCH
	// LocalRepoPath is the clone the local publisher commits to (IACP_LOCAL_GIT_REPO).
	LocalRepoPath string
	GitHub        GitHubAppConfig
}

// GitHubAppConfig configures the GitHub App publisher.
type GitHubAppConfig struct {
	AppID          int64  // IACP_GITHUB_APP_ID
	InstallationID int64  // IACP_GITHUB_INSTALLATION_ID
	PrivateKeyPath string // IACP_GITHUB_PRIVATE_KEY_PATH: PEM file, never the key itself
	Owner          string // from IACP_GITHUB_REPOSITORY (owner/name)
	Repo           string
	APIURL         string // IACP_GITHUB_API_URL
}

// Repository is owner/name, or "" when unset.
func (g GitHubAppConfig) Repository() string {
	if g.Owner == "" {
		return ""
	}
	return g.Owner + "/" + g.Repo
}

var (
	repositoryRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100})$`)
	branchRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// loadPublisher reads the publisher settings. It fails closed: an unknown kind, a
// half-configured GitHub App, or App settings while another publisher is selected are
// errors rather than silently ignored.
func loadPublisher(get func(name, def string) string) (PublisherConfig, error) {
	p := PublisherConfig{
		Kind:          PublisherKind(get("PUBLISHER", string(PublisherNone))),
		BaseBranch:    get("PUBLISH_BASE_BRANCH", "main"),
		LocalRepoPath: get("LOCAL_GIT_REPO", ""),
		GitHub: GitHubAppConfig{
			PrivateKeyPath: get("GITHUB_PRIVATE_KEY_PATH", ""),
			APIURL:         get("GITHUB_API_URL", DefaultGitHubAPIURL),
		},
	}
	var errs []error
	appID, installation := get("GITHUB_APP_ID", ""), get("GITHUB_INSTALLATION_ID", "")
	var err error
	if p.GitHub.AppID, err = positiveInt(appID); err != nil {
		errs = append(errs, fmt.Errorf("%sGITHUB_APP_ID: %w", Prefix, err))
	}
	if p.GitHub.InstallationID, err = positiveInt(installation); err != nil {
		errs = append(errs, fmt.Errorf("%sGITHUB_INSTALLATION_ID: %w", Prefix, err))
	}
	if repo := get("GITHUB_REPOSITORY", ""); repo != "" {
		m := repositoryRe.FindStringSubmatch(repo)
		if m == nil {
			errs = append(errs, fmt.Errorf("%sGITHUB_REPOSITORY must be owner/name", Prefix))
		} else {
			p.GitHub.Owner, p.GitHub.Repo = m[1], m[2]
		}
	}
	if !branchRe.MatchString(p.BaseBranch) {
		errs = append(errs, fmt.Errorf("%sPUBLISH_BASE_BRANCH %q is not a valid branch name", Prefix, p.BaseBranch))
	}
	appSet := appID != "" || installation != "" || p.GitHub.PrivateKeyPath != ""

	switch p.Kind {
	case PublisherNone, PublisherLocal:
		if appSet {
			errs = append(errs, fmt.Errorf("%sGITHUB_APP_ID, %sGITHUB_INSTALLATION_ID and %sGITHUB_PRIVATE_KEY_PATH are only used with %sPUBLISHER=github",
				Prefix, Prefix, Prefix, Prefix))
		}
		if p.Kind == PublisherLocal && p.LocalRepoPath == "" {
			errs = append(errs, fmt.Errorf("%sPUBLISHER=local needs %sLOCAL_GIT_REPO", Prefix, Prefix))
		}
	case PublisherGitHub:
		var missing []string
		for _, kv := range [][2]string{{"GITHUB_APP_ID", appID}, {"GITHUB_INSTALLATION_ID", installation},
			{"GITHUB_PRIVATE_KEY_PATH", p.GitHub.PrivateKeyPath}, {"GITHUB_REPOSITORY", p.GitHub.Owner}} {
			if kv[1] == "" {
				missing = append(missing, Prefix+kv[0])
			}
		}
		if len(missing) > 0 {
			errs = append(errs, fmt.Errorf("%sPUBLISHER=github needs %v", Prefix, missing))
		}
	default:
		errs = append(errs, fmt.Errorf("%sPUBLISHER: unsupported value %q (supported: none, local, github)", Prefix, p.Kind))
	}
	if p.Kind != PublisherLocal && p.LocalRepoPath != "" {
		errs = append(errs, fmt.Errorf("%sLOCAL_GIT_REPO is only used with %sPUBLISHER=local", Prefix, Prefix))
	}
	return p, errors.Join(errs...)
}

func positiveInt(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return n, nil
}

// loadWorker reads the in-process worker settings.
func loadWorker(get func(name, def string) string) (time.Duration, error) {
	d, err := time.ParseDuration(get("WORKER_POLL_INTERVAL", "1s"))
	if err != nil || d < 50*time.Millisecond || d > time.Minute {
		return 0, fmt.Errorf("%sWORKER_POLL_INTERVAL must be a duration between 50ms and 1m", Prefix)
	}
	return d, nil
}

// loadQueueRetention reads IACP_QUEUE_RETENTION: at least one hour, default 7 days.
func loadQueueRetention(get func(name, def string) string) (time.Duration, error) {
	d, err := time.ParseDuration(get("QUEUE_RETENTION", "168h"))
	if err != nil || d < time.Hour {
		return 0, fmt.Errorf("%sQUEUE_RETENTION must be a duration of at least 1h", Prefix)
	}
	return d, nil
}
