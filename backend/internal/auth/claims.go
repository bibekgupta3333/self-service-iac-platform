// Package auth verifies machine identities. This file holds the GitHub Actions OIDC
// claims and the pure allow-list checks; jwks.go fetches signing keys and verifier.go
// ties signature, registered-claim and allow-list checks together.
package auth

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// GitHubIssuer is the issuer of GitHub Actions OIDC tokens.
const GitHubIssuer = "https://token.actions.githubusercontent.com"

// ErrInvalidToken is wrapped by every verification failure that is not an allow-list
// rejection (bad signature, unknown key, wrong iss/aud, expired, malformed).
var ErrInvalidToken = errors.New("invalid OIDC token")

// Claims are the GitHub Actions OIDC token claims the platform uses.
// https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/about-security-hardening-with-openid-connect
type Claims struct {
	jwt.RegisteredClaims
	Repository      string `json:"repository"`       // "acme/self-service-iac-platform"
	RepositoryID    string `json:"repository_id"`    // numeric, as a string
	RepositoryOwner string `json:"repository_owner"` // "acme"
	Ref             string `json:"ref"`              // "refs/heads/main", "refs/pull/812/merge"
	SHA             string `json:"sha"`
	Workflow        string `json:"workflow"`
	JobWorkflowRef  string `json:"job_workflow_ref"` // "acme/repo/.github/workflows/apply.yml@refs/heads/main"
	RunID           string `json:"run_id"`
	RunAttempt      string `json:"run_attempt"`
	EventName       string `json:"event_name"`
	Environment     string `json:"environment"`
	Actor           string `json:"actor"`
}

// Identity converts the claims into the workflow identity attached to the request. Pure.
func (c Claims) Identity() (identity.Workflow, error) {
	runID, err := strconv.ParseInt(c.RunID, 10, 64)
	if err != nil || runID <= 0 {
		return identity.Workflow{}, fmt.Errorf("%w: run_id %q is not a positive integer", ErrInvalidToken, c.RunID)
	}
	attempt, err := strconv.Atoi(c.RunAttempt)
	if err != nil || attempt <= 0 {
		return identity.Workflow{}, fmt.Errorf("%w: run_attempt %q is not a positive integer", ErrInvalidToken, c.RunAttempt)
	}
	return identity.Workflow{Repository: c.Repository, Ref: c.Ref, JobWorkflowRef: c.JobWorkflowRef, SHA: c.SHA,
		EventName: c.EventName, RunID: runID, RunAttempt: attempt, Actor: c.Actor, Environment: c.Environment}, nil
}

// Policy is what a token must say to be accepted.
type Policy struct {
	Issuer       string
	Audience     string
	Repository   string   // owner/name; repository_owner must be its owner part
	RepositoryID string   // optional numeric id; when set every token must carry exactly this id (survives renames and re-registration)
	Refs         []string // path.Match patterns for the ref claim
	WorkflowRefs []string // path.Match patterns for the job_workflow_ref claim
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// DefaultRefs accepts main and pull-request merge refs (pr-validate runs on the latter).
var DefaultRefs = []string{"refs/heads/main", "refs/pull/*/merge"}

// DefaultWorkflowRefs accepts any workflow defined on main, plus pr-validate.yml as run
// for a pull request. Endpoints narrow this further (runs.CheckProvenance).
func DefaultWorkflowRefs(repository string) []string {
	return []string{
		repository + "/.github/workflows/*@refs/heads/main",
		repository + "/.github/workflows/pr-validate.yml@refs/pull/*/merge",
	}
}

// Owner is the owner part of Repository. Pure.
func (p Policy) Owner() string {
	owner, _, _ := strings.Cut(p.Repository, "/")
	return owner
}

// Validate checks the policy is complete and its patterns compile. Pure.
func (p Policy) Validate() error {
	var errs []error
	if err := CheckEndpointURL(p.Issuer); err != nil {
		errs = append(errs, fmt.Errorf("issuer: %w", err))
	}
	if p.Audience == "" {
		errs = append(errs, errors.New("audience is required"))
	}
	if !repoRe.MatchString(p.Repository) {
		errs = append(errs, fmt.Errorf("repository %q must look like owner/name", p.Repository))
	}
	if p.RepositoryID != "" {
		if _, err := strconv.ParseUint(p.RepositoryID, 10, 64); err != nil {
			errs = append(errs, fmt.Errorf("repository id %q must be numeric", p.RepositoryID))
		}
	}
	if len(p.Refs) == 0 || len(p.WorkflowRefs) == 0 {
		errs = append(errs, errors.New("ref and job_workflow_ref allow-lists must not be empty"))
	}
	for _, pat := range append(append([]string(nil), p.Refs...), p.WorkflowRefs...) {
		if _, err := path.Match(pat, ""); err != nil {
			errs = append(errs, fmt.Errorf("bad pattern %q: %w", pat, err))
		}
	}
	return errors.Join(errs...)
}

// CheckClaims applies the repository and workflow allow-lists to verified claims. Pure.
// iss, aud and the time claims are checked by the JWT parser before this runs.
func CheckClaims(p Policy, c Claims) error {
	switch {
	case c.Repository != p.Repository:
		return fmt.Errorf("%w: repository %q is not allowed", ErrInvalidToken, c.Repository)
	case c.RepositoryOwner != p.Owner():
		return fmt.Errorf("%w: repository_owner %q is not allowed", ErrInvalidToken, c.RepositoryOwner)
	case p.RepositoryID != "" && c.RepositoryID != p.RepositoryID:
		return fmt.Errorf("%w: repository_id %q is not allowed", ErrInvalidToken, c.RepositoryID)
	case !matchAny(p.Refs, c.Ref):
		return workflowNotAllowed(fmt.Sprintf("ref %q is not allowed", c.Ref))
	case !matchAny(p.WorkflowRefs, c.JobWorkflowRef):
		return workflowNotAllowed(fmt.Sprintf("job_workflow_ref %q is not allowed", c.JobWorkflowRef))
	}
	return nil
}

func matchAny(patterns []string, s string) bool {
	if s == "" {
		return false
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

// workflowNotAllowed is the docs/03 §7 403 for a genuine token from a workflow that may
// not call the endpoint.
func workflowNotAllowed(msg string) error {
	return &apperr.Error{Kind: apperr.KindForbidden, Code: "workflow_not_allowed", Message: msg}
}
