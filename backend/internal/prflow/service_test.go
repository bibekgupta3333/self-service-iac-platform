package prflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/generator"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish/localgit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func token(n int64) *int64 { return &n }

func draftRequest() *requests.Request {
	spec := requests.NormalizedSpec{Team: "payments", Environment: "development", Stack: "orders", Region: "us-east-1",
		Resources: []requests.ResourceInput{
			{Name: "orders_queue", Module: "sqs", Version: "1.3.0", Inputs: json.RawMessage(`{"name":"payments-orders-dev"}`)},
		}, DependsOn: []string{}}
	raw, _ := json.Marshal(spec)
	return &requests.Request{ID: uuid.MustParse("0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90"), ShortID: "req-7k3p9x",
		Stack: requests.StackRef{ID: uuid.New(), Path: "environments/development/payments/orders"}, Kind: requests.KindCreate,
		Status: requests.StatusDraft, Spec: raw, Justification: "orders queue", FencingToken: token(48213)}
}

type fakeRequests struct {
	current   *requests.Request // what Get returns; nil means target
	target    *requests.Request
	targetErr error
	recordErr error
	published []requests.PublishedPR
	failures  []requests.PublishFailure
}

func (f *fakeRequests) Get(context.Context, uuid.UUID) (*requests.Request, error) {
	if f.current != nil {
		return f.current, nil
	}
	return f.target, nil
}

func (f *fakeRequests) PublishTarget(context.Context, uuid.UUID) (*requests.Request, error) {
	return f.target, f.targetErr
}

func (f *fakeRequests) RecordPublished(_ context.Context, _ uuid.UUID, p requests.PublishedPR, _ string) (*requests.Request, error) {
	if f.recordErr != nil {
		return nil, f.recordErr
	}
	f.published = append(f.published, p)
	return f.target, nil
}

func (f *fakeRequests) RecordPublishFailure(_ context.Context, _ uuid.UUID, pf requests.PublishFailure, _ string) error {
	f.failures = append(f.failures, pf)
	return nil
}

type fakeVersions struct{}

func (fakeVersions) GetVersion(_ context.Context, name, version string) (catalog.ModuleVersion, error) {
	if name+"@"+version != "sqs@1.3.0" {
		return catalog.ModuleVersion{}, apperr.NotFound("module %s has no version %s", name, version)
	}
	return catalog.ModuleVersion{ModuleName: "sqs", Version: catalog.SemVer{Major: 1, Minor: 3}, GitSHA: strings.Repeat("9c", 20),
		Outputs: []catalog.Output{{Name: "queue_arn"}}}, nil
}

type fakeCosts struct{}

func (fakeCosts) TeamCostCenter(context.Context, uuid.UUID) (string, error) { return "cc-4102", nil }

// fakePublisher records calls; err fails CommitFiles, labelErr fails AddLabels.
type fakePublisher struct {
	err      error
	labelErr error
	commits  []publish.Commit
	prs      []publish.PullRequestSpec
	labels   []string
	closed   []int
	deleted  []string
}

func (f *fakePublisher) ClosePullRequest(_ context.Context, n int) error {
	f.closed = append(f.closed, n)
	return nil
}

func (f *fakePublisher) PullRequestHead(context.Context, int) (string, error) { return "", nil }

func (f *fakePublisher) DeleteBranch(_ context.Context, b string) error {
	f.deleted = append(f.deleted, b)
	return nil
}

func (f *fakePublisher) Name() string { return "fake" }

func (f *fakePublisher) CommitFiles(_ context.Context, c publish.Commit) (publish.CommitRef, error) {
	if f.err != nil {
		return publish.CommitRef{}, f.err
	}
	f.commits = append(f.commits, c)
	return publish.CommitRef{Branch: c.Branch, SHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("e", 40)}, nil
}

func (f *fakePublisher) OpenPullRequest(_ context.Context, s publish.PullRequestSpec) (publish.PullRequest, error) {
	f.prs = append(f.prs, s)
	return publish.PullRequest{Number: 812, URL: "https://github.com/acme/infra/pull/812"}, nil
}

func (f *fakePublisher) AddLabels(_ context.Context, _ int, labels []string) error {
	if f.labelErr != nil {
		return f.labelErr
	}
	f.labels = labels
	return nil
}

func (f *fakePublisher) DispatchWorkflow(context.Context, publish.Dispatch) error { return nil }

func renderer() *generator.Generator {
	return generator.New("https://example.test/iacp.git", generator.LocalBackend{}, generator.LocalStackProvider{Endpoint: "http://localhost:4566"})
}

func job(attempt int) outbox.Job {
	raw, _ := json.Marshal(requests.PublishPayload{RequestID: uuid.MustParse("0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90")})
	return outbox.Job{ID: 9, Topic: outbox.TopicRequestPublish, Payload: raw, Attempts: attempt, MaxAttempts: 3}
}

func TestHandlePublishesAndRecords(t *testing.T) {
	reqs := &fakeRequests{target: draftRequest()}
	pub := &fakePublisher{}
	svc := NewService(reqs, fakeVersions{}, fakeCosts{}, renderer(), pub, Options{PortalURL: "http://localhost:3000"})
	if err := svc.Handle(context.Background(), job(1)); err != nil {
		t.Fatal(err)
	}
	if len(pub.commits) != 1 || pub.commits[0].Branch != "iacp/req-7k3p9x" {
		t.Fatalf("commits = %+v", pub.commits)
	}
	var paths []string
	for p := range pub.commits[0].Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	want := []string{"backend.tf", "iacp.stack.json", "main.tf", "outputs.tf", "providers.tf", "variables.tf", "versions.tf"}
	for i := range want {
		want[i] = "environments/development/payments/orders/" + want[i]
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %v", paths)
	}
	stackJSON := string(pub.commits[0].Files["environments/development/payments/orders/iacp.stack.json"])
	if !strings.Contains(stackJSON, `"request_id": "req-7k3p9x"`) || !strings.Contains(stackJSON, `"fencing_token": 48213`) {
		t.Fatalf("iacp.stack.json = %s", stackJSON)
	}
	if !strings.Contains(pub.commits[0].Message, "Request: req-7k3p9x") {
		t.Fatalf("message = %q", pub.commits[0].Message)
	}
	if len(pub.prs) != 1 || pub.prs[0].Title != "iacp: create development/payments/orders (req-7k3p9x)" ||
		!strings.Contains(pub.prs[0].Body, "http://localhost:3000/requests/0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90") ||
		!strings.Contains(pub.prs[0].Body, publish.PlanStart) {
		t.Fatalf("pr = %+v", pub.prs)
	}
	if !slices.Contains(pub.labels, "iacp:team:payments") {
		t.Fatalf("labels = %v", pub.labels)
	}
	want1 := requests.PublishedPR{Publisher: "fake", Branch: "iacp/req-7k3p9x", CommitSHA: strings.Repeat("c", 40),
		TreeSHA: strings.Repeat("e", 40), Number: 812, URL: "https://github.com/acme/infra/pull/812", FencingToken: 48213}
	if len(reqs.published) != 1 || reqs.published[0] != want1 || len(reqs.failures) != 0 {
		t.Fatalf("published = %+v, failures = %+v", reqs.published, reqs.failures)
	}
}

func TestHandleFailures(t *testing.T) {
	broken := draftRequest()
	broken.Spec = json.RawMessage(`{"team":"payments","environment":"development","stack":"orders","region":"us-east-1",
		"resources":[{"name":"q","module":"sqs","version":"9.9.9","inputs":{}}]}`)
	tests := []struct {
		name          string
		reqs          *fakeRequests
		pubErr        error
		attempt       int
		wantErr       bool
		wantPermanent bool
		wantFailure   *bool // nil: no failure recorded; else Final
	}{
		{name: "not publishable is skipped", reqs: &fakeRequests{targetErr: fmt.Errorf("%w: queued", requests.ErrNotPublishable)}, attempt: 1},
		{name: "unknown request", reqs: &fakeRequests{targetErr: apperr.NotFound("request x not found")}, attempt: 1,
			wantErr: true, wantPermanent: true},
		{name: "publisher error is retried", reqs: &fakeRequests{target: draftRequest()}, pubErr: errors.New("github 502"), attempt: 1,
			wantErr: true, wantFailure: new(false)},
		{name: "last attempt fails the request", reqs: &fakeRequests{target: draftRequest()}, pubErr: errors.New("github 502"), attempt: 3,
			wantErr: true, wantFailure: new(true)},
		{name: "unresolvable version is permanent", reqs: &fakeRequests{target: broken}, attempt: 1,
			wantErr: true, wantPermanent: true, wantFailure: new(true)},
		{name: "record conflict is permanent", reqs: &fakeRequests{target: draftRequest(), recordErr: apperr.Conflict("stale_fencing_token", "lost")},
			attempt: 1, wantErr: true, wantPermanent: true, wantFailure: new(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &fakePublisher{err: tt.pubErr}
			svc := NewService(tt.reqs, fakeVersions{}, fakeCosts{}, renderer(), pub, Options{})
			err := svc.Handle(context.Background(), job(tt.attempt))
			if (err != nil) != tt.wantErr || errors.Is(err, outbox.ErrPermanent) != tt.wantPermanent {
				t.Fatalf("err = %v", err)
			}
			switch {
			case tt.wantFailure == nil && len(tt.reqs.failures) != 0:
				t.Fatalf("unexpected failures %+v", tt.reqs.failures)
			case tt.wantFailure != nil && (len(tt.reqs.failures) != 1 || tt.reqs.failures[0].Final != *tt.wantFailure ||
				tt.reqs.failures[0].Attempt != tt.attempt || tt.reqs.failures[0].Publisher != "fake"):
				t.Fatalf("failures = %+v", tt.reqs.failures)
			}
		})
	}
}

func TestFinalFailureWithdrawsTheUnrecordedPR(t *testing.T) {
	tests := []struct {
		name        string
		reqs        *fakeRequests
		pub         *fakePublisher
		attempt     int
		wantClosed  []int
		wantDeleted bool
	}{
		{name: "record conflict after the PR opened", reqs: &fakeRequests{target: draftRequest(),
			recordErr: apperr.Conflict("stale_fencing_token", "lost")}, pub: &fakePublisher{}, attempt: 1,
			wantClosed: []int{812}, wantDeleted: true},
		{name: "last attempt fails after the PR opened", reqs: &fakeRequests{target: draftRequest()},
			pub: &fakePublisher{labelErr: errors.New("github 502")}, attempt: 3, wantClosed: []int{812}, wantDeleted: true},
		{name: "last attempt fails before the PR", reqs: &fakeRequests{target: draftRequest()},
			pub: &fakePublisher{err: errors.New("github 502")}, attempt: 3, wantDeleted: true},
		{name: "a retryable failure keeps the PR for the next attempt", reqs: &fakeRequests{target: draftRequest()},
			pub: &fakePublisher{labelErr: errors.New("github 502")}, attempt: 1},
		{name: "a PR recorded despite the error is never withdrawn", reqs: &fakeRequests{target: draftRequest(),
			recordErr: apperr.Conflict("concurrent_update", "lost reply"), current: recordedRequest()},
			pub: &fakePublisher{}, attempt: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(tt.reqs, fakeVersions{}, fakeCosts{}, renderer(), tt.pub, Options{})
			if err := svc.Handle(context.Background(), job(tt.attempt)); err == nil {
				t.Fatal("want an error")
			}
			if !slices.Equal(tt.pub.closed, tt.wantClosed) || (len(tt.pub.deleted) == 1) != tt.wantDeleted {
				t.Fatalf("closed %v deleted %v", tt.pub.closed, tt.pub.deleted)
			}
			if tt.wantDeleted && (tt.pub.deleted[0] != "iacp/req-7k3p9x" || tt.reqs.failures[0].Cleanup == "") {
				t.Fatalf("deleted %v, failure %+v", tt.pub.deleted, tt.reqs.failures)
			}
		})
	}
}

func recordedRequest() *requests.Request {
	r := draftRequest()
	r.Status, r.PR = requests.StatusPROpen, &requests.PRLink{Branch: "iacp/req-7k3p9x", Number: 812}
	return r
}

func TestHandleRejectsBadPayload(t *testing.T) {
	svc := NewService(&fakeRequests{}, fakeVersions{}, fakeCosts{}, renderer(), &fakePublisher{}, Options{})
	err := svc.Handle(context.Background(), outbox.Job{ID: 1, Payload: json.RawMessage(`{"request_id":"nope"}`)})
	if !errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildStackSpec(t *testing.T) {
	r := draftRequest()
	spec, err := DecodeSpec(r)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := fakeVersions{}.GetVersion(context.Background(), "sqs", "1.3.0")
	got, err := BuildStackSpec(r, spec, map[string]catalog.ModuleVersion{VersionKey("sqs", "1.3.0"): v}, "cc-1", "~> 1.13", "~> 5.80")
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "req-7k3p9x" || got.FencingToken != 48213 || got.CostCenter != "cc-1" || len(got.Resources) != 1 ||
		got.Resources[0].GitSHA != v.GitSHA || got.Resources[0].Version != v.Version {
		t.Fatalf("spec = %+v", got)
	}
	if _, err := BuildStackSpec(r, spec, nil, "", "a", "b"); err == nil {
		t.Fatal("unresolved versions must fail")
	}
	r.FencingToken = nil
	if _, err := BuildStackSpec(r, spec, map[string]catalog.ModuleVersion{VersionKey("sqs", "1.3.0"): v}, "", "a", "b"); err == nil {
		t.Fatal("a request without a lock token must fail")
	}
}

func TestPRContextCarriesTheRollbackSource(t *testing.T) {
	r := draftRequest()
	spec, err := DecodeSpec(r)
	if err != nil {
		t.Fatal(err)
	}
	if pc := PRContextFor(r, spec, "http://portal"); pc.RollbackOf != "" {
		t.Fatalf("ordinary request: %+v", pc)
	}
	spec.RollbackOf = "req-4m2n8q"
	if pc := PRContextFor(r, spec, "http://portal"); pc.RollbackOf != "req-4m2n8q" {
		t.Fatalf("rollback: %+v", pc)
	}
}

// TestDestroyRendersAnEmptyMarkedRoot: a destroy request names no modules, resolves none,
// and renders a root whose iacp.stack.json says lifecycle destroy.
func TestDestroyRendersAnEmptyMarkedRoot(t *testing.T) {
	r := draftRequest()
	r.Kind = requests.KindDestroy
	raw, _ := json.Marshal(requests.NormalizedSpec{Team: "payments", Environment: "development", Stack: "orders",
		Region: "us-east-1", Resources: []requests.ResourceInput{}, DependsOn: []string{}})
	r.Spec = raw
	spec, err := DecodeSpec(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := BuildStackSpec(r, spec, nil, "cc-1", "~> 1.13", "~> 5.80")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != generator.LifecycleDestroy || len(got.Resources) != 0 {
		t.Fatalf("spec = %+v", got)
	}
	files, err := generator.New("https://example.test/iacp.git", generator.LocalBackend{},
		generator.LocalStackProvider{Endpoint: "http://localhost:4566"}).Render(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files[generator.FileMain]), "module ") ||
		!strings.Contains(string(files[generator.FileStackJSON]), `"lifecycle": "destroy"`) {
		t.Fatalf("main.tf:\n%s\niacp.stack.json:\n%s", files[generator.FileMain], files[generator.FileStackJSON])
	}
	// An update renders like a create, and an unmarked root stays active.
	u := draftRequest()
	u.Kind = requests.KindUpdate
	us, _ := DecodeSpec(u)
	v, _ := fakeVersions{}.GetVersion(context.Background(), "sqs", "1.3.0")
	if g, err := BuildStackSpec(u, us, map[string]catalog.ModuleVersion{VersionKey("sqs", "1.3.0"): v}, "", "a", "b"); err != nil ||
		g.Lifecycle != generator.LifecycleActive || len(g.Resources) != 1 {
		t.Fatalf("update spec = %+v, %v", g, err)
	}
}

// TestPublishToLocalGit runs the real generator and local git adapter end to end.
func TestPublishToLocalGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	pub, err := localgit.New(context.Background(), localgit.Config{RepoPath: dir, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(&fakeRequests{target: draftRequest()}, fakeVersions{}, fakeCosts{}, renderer(), pub, Options{PortalURL: "http://p"})
	got, err := svc.Publish(context.Background(), draftRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 1 || got.Branch != "iacp/req-7k3p9x" || len(got.TreeSHA) != 40 {
		t.Fatalf("published = %+v", got)
	}
	out, err := exec.Command("git", "-C", dir, "show", "iacp/req-7k3p9x:environments/development/payments/orders/main.tf").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `source = "git::https://example.test/iacp.git//modules/sqs?ref=modules/sqs/v1.3.0"`) {
		t.Fatalf("main.tf = %s (%v)", out, err)
	}
}
