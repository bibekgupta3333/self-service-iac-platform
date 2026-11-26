package githubapp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
)

var _ publish.Publisher = (*Client)(nil)

// Name implements publish.Publisher.
func (c *Client) Name() string { return "github" }

func (c *Client) repoPath(suffix string) string {
	return "/repos/" + url.PathEscape(c.cfg.Owner) + "/" + url.PathEscape(c.cfg.Repo) + suffix
}

// refPath escapes each segment of a branch name for the git/refs endpoints
// (iacp/req-1 → heads/iacp/req-1).
func refPath(branch string) string {
	segs := strings.Split(branch, "/")
	return "heads/" + strings.Join(fp.Map(segs, url.PathEscape), "/")
}

type shaObject struct {
	SHA string `json:"sha"`
}

// TreeEntry is one blob in a git data API tree.
type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// TreeEntries turns path → blob sha into regular-file tree entries sorted by path. Pure.
func TreeEntries(blobs map[string]string) []TreeEntry {
	out := make([]TreeEntry, 0, len(blobs))
	for _, p := range fp.SortedKeys(blobs) {
		out = append(out, TreeEntry{Path: p, Mode: "100644", Type: "blob", SHA: blobs[p]})
	}
	return out
}

// CommitFiles implements publish.Publisher with the git data API: read the base branch
// head, create one blob per file, a tree on top of the base tree, a commit whose parent
// is the base head, then create the branch ref (or force-move it if it already exists,
// so a retried publish lands the same files). Nothing is visible until the ref moves.
func (c *Client) CommitFiles(ctx context.Context, cm publish.Commit) (publish.CommitRef, error) {
	if err := publish.ValidatePath(cm.Branch); err != nil {
		return publish.CommitRef{}, fmt.Errorf("branch: %w", err)
	}
	if len(cm.Files) == 0 {
		return publish.CommitRef{}, fmt.Errorf("commit %s: no files", cm.Branch)
	}
	for p := range cm.Files {
		if err := publish.ValidatePath(p); err != nil {
			return publish.CommitRef{}, err
		}
	}

	var head struct {
		Object shaObject `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/git/ref/"+refPath(c.cfg.BaseBranch)), nil, &head, authInstallation); err != nil {
		return publish.CommitRef{}, fmt.Errorf("read base branch %s: %w", c.cfg.BaseBranch, err)
	}
	var base struct {
		Tree shaObject `json:"tree"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath("/git/commits/"+url.PathEscape(head.Object.SHA)), nil, &base, authInstallation); err != nil {
		return publish.CommitRef{}, fmt.Errorf("read base commit: %w", err)
	}

	blobs := make(map[string]string, len(cm.Files))
	for _, p := range fp.SortedKeys(cm.Files) {
		var blob shaObject
		body := map[string]string{"content": base64.StdEncoding.EncodeToString(cm.Files[p]), "encoding": "base64"}
		if err := c.do(ctx, http.MethodPost, c.repoPath("/git/blobs"), body, &blob, authInstallation); err != nil {
			return publish.CommitRef{}, fmt.Errorf("create blob %s: %w", p, err)
		}
		blobs[p] = blob.SHA
	}

	var tree shaObject
	if err := c.do(ctx, http.MethodPost, c.repoPath("/git/trees"),
		map[string]any{"base_tree": base.Tree.SHA, "tree": TreeEntries(blobs)}, &tree, authInstallation); err != nil {
		return publish.CommitRef{}, fmt.Errorf("create tree: %w", err)
	}
	var commit shaObject
	if err := c.do(ctx, http.MethodPost, c.repoPath("/git/commits"),
		map[string]any{"message": cm.Message, "tree": tree.SHA, "parents": []string{head.Object.SHA}}, &commit, authInstallation); err != nil {
		return publish.CommitRef{}, fmt.Errorf("create commit: %w", err)
	}

	err := c.do(ctx, http.MethodPost, c.repoPath("/git/refs"),
		map[string]string{"ref": "refs/heads/" + cm.Branch, "sha": commit.SHA}, nil, authInstallation)
	if refExists(err) { // the branch exists: a retried publish
		err = c.do(ctx, http.MethodPatch, c.repoPath("/git/refs/"+refPath(cm.Branch)),
			map[string]any{"sha": commit.SHA, "force": true}, nil, authInstallation)
	}
	if err != nil {
		return publish.CommitRef{}, fmt.Errorf("point %s at %s: %w", cm.Branch, commit.SHA, err)
	}
	return publish.CommitRef{Branch: cm.Branch, SHA: commit.SHA, TreeSHA: tree.SHA}, nil
}

// refExists reports whether err is GitHub's 422 for creating a ref that already exists;
// any other 422 (bad sha, invalid name) must not turn into a forced update. Pure.
func refExists(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusUnprocessableEntity &&
		strings.Contains(strings.ToLower(e.Message), "reference already exists")
}

type pullResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// OpenPullRequest implements publish.Publisher. When GitHub answers 422 because a PR for
// the head already exists, the open PR is looked up and returned.
func (c *Client) OpenPullRequest(ctx context.Context, spec publish.PullRequestSpec) (publish.PullRequest, error) {
	var pr pullResponse
	err := c.do(ctx, http.MethodPost, c.repoPath("/pulls"), map[string]any{
		"title": spec.Title, "head": spec.Head, "base": c.cfg.BaseBranch, "body": spec.Body, "maintainer_can_modify": false,
	}, &pr, authInstallation)
	if IsStatus(err, http.StatusUnprocessableEntity) {
		existing, found, lerr := c.findOpenPull(ctx, spec.Head)
		if lerr != nil {
			return publish.PullRequest{}, fmt.Errorf("open pull request: %w (lookup: %w)", err, lerr)
		}
		if found {
			return existing, nil
		}
	}
	if err != nil {
		return publish.PullRequest{}, fmt.Errorf("open pull request: %w", err)
	}
	return publish.PullRequest{Number: pr.Number, URL: pr.HTMLURL}, nil
}

func (c *Client) findOpenPull(ctx context.Context, head string) (publish.PullRequest, bool, error) {
	q := url.Values{"head": {c.cfg.Owner + ":" + head}, "base": {c.cfg.BaseBranch}, "state": {"open"}}
	var pulls []pullResponse
	if err := c.do(ctx, http.MethodGet, c.repoPath("/pulls?"+q.Encode()), nil, &pulls, authInstallation); err != nil {
		return publish.PullRequest{}, false, err
	}
	if len(pulls) == 0 {
		return publish.PullRequest{}, false, nil
	}
	return publish.PullRequest{Number: pulls[0].Number, URL: pulls[0].HTMLURL}, true, nil
}

// AddLabels implements publish.Publisher (issues API; labels are created if missing).
func (c *Client) AddLabels(ctx context.Context, number int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	sorted := slices.Sorted(slices.Values(labels))
	if err := c.do(ctx, http.MethodPost, c.repoPath(fmt.Sprintf("/issues/%d/labels", number)),
		map[string][]string{"labels": sorted}, nil, authInstallation); err != nil {
		return fmt.Errorf("label pull request #%d: %w", number, err)
	}
	return nil
}

// DispatchWorkflow implements publish.Publisher.
func (c *Client) DispatchWorkflow(ctx context.Context, d publish.Dispatch) error {
	if err := publish.ValidatePath(d.Workflow); err != nil || strings.Contains(d.Workflow, "/") {
		return fmt.Errorf("workflow %q: %w", d.Workflow, publish.ErrInvalidPath)
	}
	inputs := d.Inputs
	if inputs == nil {
		inputs = map[string]string{}
	}
	// A dispatch is not idempotent: a retried POST after a lost response could start two runs.
	if err := c.doOnce(ctx, http.MethodPost, c.repoPath("/actions/workflows/"+url.PathEscape(d.Workflow)+"/dispatches"),
		map[string]any{"ref": d.Ref, "inputs": inputs}, nil); err != nil {
		return fmt.Errorf("dispatch %s on %s: %w", d.Workflow, d.Ref, err)
	}
	return nil
}
