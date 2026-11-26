package localgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
)

// scratchRepo creates a repository with one commit on main and returns its path.
func scratchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitFilesOnBranchWithoutTouchingWorktree(t *testing.T) {
	dir := scratchRepo(t)
	ctx := context.Background()
	p, err := New(ctx, Config{RepoPath: dir, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"environments/development/payments/orders/main.tf":         []byte("module \"q\" {}\n"),
		"environments/development/payments/orders/iacp.stack.json": []byte("{}\n"),
	}
	ref, err := p.CommitFiles(ctx, publish.Commit{Branch: "iacp/req-7k3p9x", Message: "feat: stack\n\nRequest: req-7k3p9x\n", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", "iacp/req-7k3p9x"); got != ref.SHA {
		t.Fatalf("branch at %s, want %s", got, ref.SHA)
	}
	if got := gitOut(t, dir, "rev-parse", ref.SHA+"^{tree}"); got != ref.TreeSHA {
		t.Fatalf("tree %s, want %s", got, ref.TreeSHA)
	}
	if got := gitOut(t, dir, "show", "iacp/req-7k3p9x:environments/development/payments/orders/main.tf"); got != "module \"q\" {}" {
		t.Fatalf("file content %q", got)
	}
	// The base branch's files are kept, the parent is main, and the author is the bot.
	if got := gitOut(t, dir, "show", "iacp/req-7k3p9x:README.md"); got != "scratch" {
		t.Fatalf("base file lost: %q", got)
	}
	if gitOut(t, dir, "rev-parse", ref.SHA+"^") != gitOut(t, dir, "rev-parse", "main") {
		t.Fatal("parent must be the base branch head")
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%an <%ae>|%s", ref.SHA); got != "iacp-bot <iacp-bot@localhost>|feat: stack" {
		t.Fatalf("commit = %q", got)
	}
	// Checkout, index and main are untouched.
	if got := gitOut(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree changed: %q", got)
	}
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("HEAD moved to %s", got)
	}

	// Retry: the branch is force-moved to an equivalent commit.
	files["environments/development/payments/orders/main.tf"] = []byte("changed\n")
	again, err := p.CommitFiles(ctx, publish.Commit{Branch: "iacp/req-7k3p9x", Message: "retry", Files: files})
	if err != nil || gitOut(t, dir, "rev-parse", "iacp/req-7k3p9x") != again.SHA {
		t.Fatalf("retry: %+v, %v", again, err)
	}
}

func TestFakePullRequests(t *testing.T) {
	dir := scratchRepo(t)
	ctx := context.Background()
	p, err := New(ctx, Config{RepoPath: dir, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenPullRequest(ctx, publish.PullRequestSpec{Head: "iacp/req-missing"}); err == nil {
		t.Fatal("a PR needs an existing head branch")
	}
	for _, b := range []string{"iacp/req-aaaaaa", "iacp/req-bbbbbb"} {
		if _, err := p.CommitFiles(ctx, publish.Commit{Branch: b, Message: "m", Files: map[string][]byte{"x/main.tf": []byte("x")}}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := p.OpenPullRequest(ctx, publish.PullRequestSpec{Head: "iacp/req-aaaaaa", Title: "A", Body: "body"})
	if err != nil || a.Number != 1 || !strings.HasPrefix(a.URL, "file://") {
		t.Fatalf("a = %+v, %v", a, err)
	}
	b, err := p.OpenPullRequest(ctx, publish.PullRequestSpec{Head: "iacp/req-bbbbbb", Title: "B"})
	if err != nil || b.Number != 2 {
		t.Fatalf("b = %+v, %v", b, err)
	}
	again, err := p.OpenPullRequest(ctx, publish.PullRequestSpec{Head: "iacp/req-aaaaaa", Title: "A"})
	if err != nil || again != a {
		t.Fatalf("reopen = %+v, %v", again, err)
	}
	if err := p.AddLabels(ctx, 1, []string{"iacp:team:x", "iacp"}); err != nil {
		t.Fatal(err)
	}
	if err := p.AddLabels(ctx, 1, []string{"iacp"}); err != nil {
		t.Fatal(err)
	}
	if err := p.AddLabels(ctx, 9, []string{"iacp"}); err == nil {
		t.Fatal("labels on an unknown PR must fail")
	}
	if err := p.DispatchWorkflow(ctx, publish.Dispatch{Workflow: "pr-validate.yml", Ref: "iacp/req-aaaaaa"}); err != nil {
		t.Fatal(err)
	}
	// A new adapter on the same repository sees the recorded state.
	p2, err := New(ctx, Config{RepoPath: dir, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := p2.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if pr := st.Pulls["iacp/req-aaaaaa"]; pr == nil || pr.Body != "body" || !slices.Equal(pr.Labels, []string{"iacp", "iacp:team:x"}) {
		t.Fatalf("state = %+v", st.Pulls["iacp/req-aaaaaa"])
	}
	if st.NextNumber != 3 || len(st.Dispatches) != 1 {
		t.Fatalf("state = %+v", st)
	}
	if got := gitOut(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("state must live in the git dir, not the worktree: %q", got)
	}
}

func TestRejectsUnsafeInput(t *testing.T) {
	dir := scratchRepo(t)
	ctx := context.Background()
	p, err := New(ctx, Config{RepoPath: dir, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []publish.Commit{
		{Branch: "iacp/req-aaaaaa", Files: map[string][]byte{"../escape": nil}},
		{Branch: "iacp/req-aaaaaa", Files: map[string][]byte{".git/hooks/post-commit": nil}},
		{Branch: "--upload-pack=x", Files: map[string][]byte{"a": nil}},
		{Branch: "iacp/req-aaaaaa"},
	} {
		if _, err := p.CommitFiles(ctx, c); err == nil {
			t.Errorf("commit %+v must be rejected", c)
		}
	}
	if err := p.DispatchWorkflow(ctx, publish.Dispatch{Workflow: "../x.yml"}); !errors.Is(err, publish.ErrInvalidPath) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewValidatesRepository(t *testing.T) {
	ctx := context.Background()
	if _, err := New(ctx, Config{RepoPath: t.TempDir(), BaseBranch: "main"}); err == nil {
		t.Fatal("a directory that is not a repository must be rejected")
	}
	dir := scratchRepo(t)
	if _, err := New(ctx, Config{RepoPath: dir, BaseBranch: "trunk"}); err == nil {
		t.Fatal("a missing base branch must be rejected")
	}
	if _, err := New(ctx, Config{RepoPath: dir}); err == nil {
		t.Fatal("base branch is required")
	}
	// A plain directory inside a repository must not resolve to the parent repository.
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctx, Config{RepoPath: sub, BaseBranch: "main"}); err == nil || !strings.Contains(err.Error(), "not the root") {
		t.Fatalf("nested directory: %v", err)
	}
}
