// Package localgit is the local-first adapter of publish.Publisher (ADR-0009).
//
// It commits generated files to a branch of a local git repository (a scratch clone)
// and returns a fake pull request, so the request → branch → PR flow runs end to end
// without a registered GitHub App. git runs through os/exec with argument arrays, never
// a shell. Commits are built with plumbing commands on a temporary index, so the clone's
// working tree and index are never touched, and the branch moves in one update-ref.
// Repository hooks are disabled for every command.
//
// Pull requests, labels and dispatches are recorded in <git-dir>/iacp-local/state.json.
package localgit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
)

// Config configures the adapter.
type Config struct {
	RepoPath   string // path of the local repository (a clone of this repo, or a scratch repo)
	BaseBranch string // branch new branches start from, e.g. main
	GitBinary  string // default "git" from PATH
}

// Identity used for commits made by the platform.
const (
	authorName  = "iacp-bot"
	authorEmail = "iacp-bot@localhost"
)

// State is what the adapter records about fake pull requests.
type State struct {
	NextNumber int                `json:"next_number"`
	Pulls      map[string]*Pull   `json:"pulls"` // head branch → PR
	Dispatches []publish.Dispatch `json:"dispatches"`
}

// Pull is a recorded fake pull request.
type Pull struct {
	Number int      `json:"number"`
	Head   string   `json:"head"`
	Base   string   `json:"base"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}

// Publisher implements publish.Publisher on a local repository.
type Publisher struct {
	cfg    Config
	repo   string // absolute repository path
	gitDir string // absolute git dir (state lives here, outside the working tree)
	mu     sync.Mutex
}

var _ publish.Publisher = (*Publisher)(nil)

// New checks that RepoPath is a git repository with BaseBranch and returns a Publisher.
func New(ctx context.Context, cfg Config) (*Publisher, error) {
	if cfg.RepoPath == "" || cfg.BaseBranch == "" {
		return nil, errors.New("localgit: repository path and base branch are required")
	}
	if err := publish.ValidatePath(cfg.BaseBranch); err != nil {
		return nil, fmt.Errorf("localgit: base branch: %w", err)
	}
	if cfg.GitBinary == "" {
		cfg.GitBinary = "git"
	}
	repo, err := filepath.Abs(cfg.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("localgit: %w", err)
	}
	p := &Publisher{cfg: cfg, repo: repo}
	gitDir, err := p.git(ctx, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("localgit: %s is not a git repository: %w", repo, err)
	}
	p.gitDir = gitDir
	if _, err := p.resolve(ctx, cfg.BaseBranch); err != nil {
		return nil, fmt.Errorf("localgit: base branch %q: %w", cfg.BaseBranch, err)
	}
	return p, nil
}

// Name implements publish.Publisher.
func (p *Publisher) Name() string { return "local" }

// git runs one git command in the repository with hooks disabled and a fixed identity.
// Inherited GIT_* variables are dropped so the caller's environment (for example a hook
// that sets GIT_DIR) cannot redirect the command; extraEnv is added back on top.
func (p *Publisher) git(ctx context.Context, extraEnv []string, stdin []byte, args ...string) (string, error) {
	base := []string{"-C", p.repo, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgSign=false"}
	cmd := exec.CommandContext(ctx, p.cfg.GitBinary, append(base, args...)...)
	env := fp.Filter(os.Environ(), func(kv string) bool { return !strings.HasPrefix(kv, "GIT_") })
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+authorName, "GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName, "GIT_COMMITTER_EMAIL="+authorEmail)
	cmd.Env = append(cmd.Env, extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (p *Publisher) resolve(ctx context.Context, branch string) (string, error) {
	return p.git(ctx, nil, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+branch+"^{commit}")
}

// CommitFiles implements publish.Publisher: blobs and a tree are built on a temporary
// index seeded from the base branch, committed with the base head as parent, and the
// branch is pointed at the commit (created, or force-moved on a retry).
func (p *Publisher) CommitFiles(ctx context.Context, c publish.Commit) (publish.CommitRef, error) {
	if err := publish.ValidatePath(c.Branch); err != nil {
		return publish.CommitRef{}, fmt.Errorf("branch: %w", err)
	}
	if len(c.Files) == 0 {
		return publish.CommitRef{}, fmt.Errorf("commit %s: no files", c.Branch)
	}
	for path := range c.Files {
		if err := publish.ValidatePath(path); err != nil {
			return publish.CommitRef{}, err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	base, err := p.resolve(ctx, p.cfg.BaseBranch)
	if err != nil {
		return publish.CommitRef{}, fmt.Errorf("read base branch: %w", err)
	}
	tmp, err := os.MkdirTemp("", "iacp-localgit-")
	if err != nil {
		return publish.CommitRef{}, fmt.Errorf("temp index: %w", err)
	}
	defer os.RemoveAll(tmp)
	idx := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}

	if _, err := p.git(ctx, idx, nil, "read-tree", base); err != nil {
		return publish.CommitRef{}, err
	}
	for _, path := range fp.SortedKeys(c.Files) {
		blob, err := p.git(ctx, nil, c.Files[path], "hash-object", "-w", "--stdin")
		if err != nil {
			return publish.CommitRef{}, fmt.Errorf("store %s: %w", path, err)
		}
		if _, err := p.git(ctx, idx, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
			return publish.CommitRef{}, fmt.Errorf("stage %s: %w", path, err)
		}
	}
	tree, err := p.git(ctx, idx, nil, "write-tree")
	if err != nil {
		return publish.CommitRef{}, err
	}
	commit, err := p.git(ctx, nil, []byte(c.Message), "commit-tree", tree, "-p", base, "-F", "-")
	if err != nil {
		return publish.CommitRef{}, err
	}
	if _, err := p.git(ctx, nil, nil, "update-ref", "-m", "iacp: publish", "refs/heads/"+c.Branch, commit); err != nil {
		return publish.CommitRef{}, err
	}
	return publish.CommitRef{Branch: c.Branch, SHA: commit, TreeSHA: tree}, nil
}

func (p *Publisher) statePath() string { return filepath.Join(p.gitDir, "iacp-local", "state.json") }

// LoadState reads the recorded pull requests (empty when none were opened yet).
func (p *Publisher) LoadState() (State, error) {
	st := State{NextNumber: 1, Pulls: map[string]*Pull{}}
	raw, err := os.ReadFile(p.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("localgit: read state: %w", err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("localgit: decode state: %w", err)
	}
	if st.Pulls == nil {
		st.Pulls = map[string]*Pull{}
	}
	return st, nil
}

// saveState writes the state atomically (temp file + rename).
func (p *Publisher) saveState(st State) error {
	dir := filepath.Dir(p.statePath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("localgit: state dir: %w", err)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("localgit: encode state: %w", err)
	}
	f, err := os.CreateTemp(dir, "state-*.json")
	if err != nil {
		return fmt.Errorf("localgit: write state: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(f.Name())
		return fmt.Errorf("localgit: write state: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("localgit: write state: %w", err)
	}
	return os.Rename(f.Name(), p.statePath())
}

// OpenPullRequest implements publish.Publisher with a recorded fake PR. The head branch
// must exist; opening the same head again returns the recorded PR.
func (p *Publisher) OpenPullRequest(ctx context.Context, spec publish.PullRequestSpec) (publish.PullRequest, error) {
	if err := publish.ValidatePath(spec.Head); err != nil {
		return publish.PullRequest{}, fmt.Errorf("head: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.resolve(ctx, spec.Head); err != nil {
		return publish.PullRequest{}, fmt.Errorf("head branch %s: %w", spec.Head, err)
	}
	st, err := p.LoadState()
	if err != nil {
		return publish.PullRequest{}, err
	}
	if pr, ok := st.Pulls[spec.Head]; ok {
		return publish.PullRequest{Number: pr.Number, URL: pr.URL}, nil
	}
	n := st.NextNumber
	pr := &Pull{Number: n, Head: spec.Head, Base: p.cfg.BaseBranch, Title: spec.Title, Body: spec.Body,
		Labels: []string{}, URL: fmt.Sprintf("file://%s#pull/%d", filepath.ToSlash(p.repo), n)}
	st.Pulls[spec.Head], st.NextNumber = pr, n+1
	if err := p.saveState(st); err != nil {
		return publish.PullRequest{}, err
	}
	return publish.PullRequest{Number: pr.Number, URL: pr.URL}, nil
}

// AddLabels implements publish.Publisher by recording the labels on the fake PR.
func (p *Publisher) AddLabels(_ context.Context, number int, labels []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, err := p.LoadState()
	if err != nil {
		return err
	}
	for _, pr := range st.Pulls {
		if pr.Number == number {
			pr.Labels = slices.Compact(slices.Sorted(slices.Values(append(pr.Labels, labels...))))
			return p.saveState(st)
		}
	}
	return fmt.Errorf("localgit: pull request #%d not found", number)
}

// DispatchWorkflow implements publish.Publisher by recording the dispatch; no workflow runs
// locally.
func (p *Publisher) DispatchWorkflow(_ context.Context, d publish.Dispatch) error {
	if err := publish.ValidatePath(d.Workflow); err != nil || strings.Contains(d.Workflow, "/") {
		return fmt.Errorf("workflow %q: %w", d.Workflow, publish.ErrInvalidPath)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st, err := p.LoadState()
	if err != nil {
		return err
	}
	st.Dispatches = append(st.Dispatches, d)
	return p.saveState(st)
}
