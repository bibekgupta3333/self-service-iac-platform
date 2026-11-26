// Package publish defines the Publisher port: how the platform writes generated stack
// files to the repository and opens the pull request for a change request.
//
// Adapters live in sub-packages: githubapp (a GitHub App over the REST API) and
// localgit (a local clone, for the local-first phase in ADR-0009). This package holds
// the port and the pure helpers shared by every adapter and the orchestrator: branch
// naming, path checks, and PR title, body and label rendering.
package publish

import (
	"context"
	"errors"
)

// Commit is a set of files committed atomically on top of the base branch.
type Commit struct {
	Branch  string            // e.g. iacp/req-7k3p9x; created, or moved to the new commit
	Message string            // commit message
	Files   map[string][]byte // repository-relative path → content
}

// CommitRef is the commit the branch points at after CommitFiles.
type CommitRef struct {
	Branch  string
	SHA     string // commit sha
	TreeSHA string // root tree sha (requests.rendered_tree_sha)
}

// PullRequestSpec describes the PR to open from Head into the base branch.
type PullRequestSpec struct {
	Head  string // branch name
	Title string
	Body  string
}

// PullRequest is an opened (or already open) pull request.
type PullRequest struct {
	Number int
	URL    string
}

// Dispatch triggers a workflow_dispatch run.
type Dispatch struct {
	Workflow string            // workflow file name, e.g. pr-validate.yml
	Ref      string            // branch or tag to run on
	Inputs   map[string]string // workflow inputs
}

// Publisher writes to the repository. Every method is safe to retry: committing the same
// branch again moves it to the new commit, and opening a PR whose head already has an
// open PR returns that PR.
type Publisher interface {
	// Name identifies the adapter in audit events ("github", "local").
	Name() string
	// CommitFiles creates the files as one commit whose parent is the base branch head,
	// then points Branch at it (creating the branch, or force-moving it).
	CommitFiles(ctx context.Context, c Commit) (CommitRef, error)
	// OpenPullRequest opens a PR from spec.Head into the base branch.
	OpenPullRequest(ctx context.Context, spec PullRequestSpec) (PullRequest, error)
	// AddLabels adds labels to a PR. Existing labels are kept.
	AddLabels(ctx context.Context, number int, labels []string) error
	// DispatchWorkflow starts a workflow_dispatch run.
	DispatchWorkflow(ctx context.Context, d Dispatch) error
}

// ErrInvalidPath is returned for a file path that could escape the repository.
var ErrInvalidPath = errors.New("invalid repository path")
