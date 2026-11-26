package prflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/generator"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Requests is the part of requests.Service the flow drives.
type Requests interface {
	Get(ctx context.Context, id uuid.UUID) (*requests.Request, error)
	PublishTarget(ctx context.Context, id uuid.UUID) (*requests.Request, error)
	RecordPublished(ctx context.Context, id uuid.UUID, p requests.PublishedPR, correlationID string) (*requests.Request, error)
	RecordPublishFailure(ctx context.Context, id uuid.UUID, f requests.PublishFailure, correlationID string) error
}

// Versions resolves pinned module versions (catalog.Service).
type Versions interface {
	GetVersion(ctx context.Context, name, version string) (catalog.ModuleVersion, error)
}

// CostCenters resolves the owning team's cost center (requests.PostgresRepository).
type CostCenters interface {
	TeamCostCenter(ctx context.Context, stackID uuid.UUID) (string, error)
}

// Renderer renders a stack root (generator.Generator).
type Renderer interface {
	Render(spec generator.StackSpec) (generator.Files, error)
}

// Options configures rendering defaults.
type Options struct {
	PortalURL          string // request links in PR bodies
	TerraformVersion   string // default DefaultTerraformVersion
	AWSProviderVersion string // default DefaultAWSProviderVersion
}

// Service publishes requests. It is the outbox handler for outbox.TopicRequestPublish.
type Service struct {
	reqs     Requests
	versions Versions
	costs    CostCenters
	render   Renderer
	pub      publish.Publisher
	opts     Options
}

// NewService returns a Service.
func NewService(reqs Requests, versions Versions, costs CostCenters, render Renderer, pub publish.Publisher, opts Options) *Service {
	if opts.TerraformVersion == "" {
		opts.TerraformVersion = DefaultTerraformVersion
	}
	if opts.AWSProviderVersion == "" {
		opts.AWSProviderVersion = DefaultAWSProviderVersion
	}
	return &Service{reqs: reqs, versions: versions, costs: costs, render: render, pub: pub, opts: opts}
}

var _ outbox.Handler = (*Service)(nil)

// errRender marks failures that retrying cannot fix (invalid spec, unresolved versions).
var errRender = errors.New("render failed")

// Handle implements outbox.Handler for request.publish jobs. A request that is no longer
// publishable (left draft, queued, no lock) is skipped. Each failed attempt is recorded;
// the last one (or a permanent failure) fails the request.
func (s *Service) Handle(ctx context.Context, j outbox.Job) error {
	var p requests.PublishPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.RequestID == uuid.Nil {
		return fmt.Errorf("decode publish job %d: %w", j.ID, outbox.ErrPermanent)
	}
	correlationID := fmt.Sprintf("outbox-%d", j.ID)
	r, err := s.reqs.PublishTarget(ctx, p.RequestID)
	switch {
	case errors.Is(err, requests.ErrNotPublishable):
		observability.Logger(ctx).InfoContext(ctx, "publish skipped", "request_id", p.RequestID, "reason", err.Error())
		return nil
	case apperr.IsKind(err, apperr.KindNotFound):
		return fmt.Errorf("publish %s: %w: %w", p.RequestID, err, outbox.ErrPermanent)
	case err != nil:
		return fmt.Errorf("load request %s: %w", p.RequestID, err)
	}

	published, err := s.Publish(ctx, r)
	partial := published
	if err == nil {
		_, err = s.reqs.RecordPublished(ctx, r.ID, published, correlationID)
		if err == nil {
			return nil
		}
		if apperr.IsKind(err, apperr.KindConflict) {
			// The request moved on, lost its lease, or the PR number is taken: no retry helps.
			err = fmt.Errorf("record published PR #%d: %w: %w", published.Number, err, errRender)
		}
	}
	permanent := errors.Is(err, errRender)
	final := permanent || j.Last()
	failure := requests.PublishFailure{Publisher: s.pub.Name(), Attempt: j.Attempts, Error: err.Error(), Final: final}
	if final {
		failure.Cleanup = s.withdrawUnrecorded(ctx, r, partial.Number)
	}
	if rerr := s.reqs.RecordPublishFailure(ctx, r.ID, failure, correlationID); rerr != nil {
		return fmt.Errorf("publish %s: %w (recording the failure: %w)", r.ShortID, err, rerr)
	}
	if permanent {
		return fmt.Errorf("publish %s: %w: %w", r.ShortID, err, outbox.ErrPermanent)
	}
	return fmt.Errorf("publish %s: %w", r.ShortID, err)
}

// withdrawUnrecorded withdraws the branch and PR of a request whose publish failed for
// good, but only after re-reading the request: if a PR is recorded after all (for
// example RecordPublished committed but its reply was lost), nothing is touched.
func (s *Service) withdrawUnrecorded(ctx context.Context, r *requests.Request, number int) string {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	cur, err := s.reqs.Get(rctx, r.ID)
	switch {
	case err != nil:
		return "skipped: could not re-read the request: " + err.Error()
	case cur.PR != nil:
		return fmt.Sprintf("skipped: PR #%d is recorded for the request", cur.PR.Number)
	}
	return s.withdraw(ctx, r.ShortID, number)
}

// withdraw closes the PR (when its number is known) and deletes the request branch,
// which also closes any PR on it. It runs on a fresh 30 s context, since the handler's
// may be what expired, and reports what happened for the audit trail.
func (s *Service) withdraw(ctx context.Context, shortID string, number int) string {
	branch, err := publish.BranchName(shortID)
	if err != nil {
		return "skipped: " + err.Error()
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var problems []string
	if number > 0 {
		if err := s.pub.ClosePullRequest(cctx, number); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if err := s.pub.DeleteBranch(cctx, branch); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		observability.Logger(ctx).WarnContext(ctx, "could not withdraw unrecorded PR", "branch", branch, "pr", number,
			"errors", problems)
		return "failed: " + strings.Join(problems, "; ")
	}
	if number > 0 {
		return fmt.Sprintf("closed PR #%d and deleted %s", number, branch)
	}
	return "deleted " + branch
}

// Publish renders the request's stack and writes it through the publisher: commit on
// iacp/<short id>, open the PR, add labels. It does not touch the database. On error the
// returned value carries what was already created (branch, PR number).
func (s *Service) Publish(ctx context.Context, r *requests.Request) (requests.PublishedPR, error) {
	spec, err := DecodeSpec(r)
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("%w: %w", errRender, err)
	}
	versions := make(map[string]catalog.ModuleVersion, len(spec.Resources))
	for _, in := range spec.Resources {
		v, err := s.versions.GetVersion(ctx, in.Module, in.Version)
		if apperr.IsKind(err, apperr.KindNotFound) {
			return requests.PublishedPR{}, fmt.Errorf("%w: %w", errRender, err)
		}
		if err != nil {
			return requests.PublishedPR{}, fmt.Errorf("resolve %s@%s: %w", in.Module, in.Version, err)
		}
		versions[VersionKey(in.Module, in.Version)] = v
	}
	costCenter, err := s.costs.TeamCostCenter(ctx, r.Stack.ID)
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("cost center: %w", err)
	}
	stackSpec, err := BuildStackSpec(r, spec, versions, costCenter, s.opts.TerraformVersion, s.opts.AWSProviderVersion)
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("%w: %w", errRender, err)
	}
	rendered, err := s.render.Render(stackSpec)
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("%w: %w", errRender, err)
	}
	files, err := publish.StackFiles(r.Stack.Path, rendered)
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("%w: %w", errRender, err)
	}
	branch, err := publish.BranchName(r.ShortID)
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("%w: %w", errRender, err)
	}
	pc := PRContextFor(r, spec, s.opts.PortalURL)

	ref, err := s.pub.CommitFiles(ctx, publish.Commit{Branch: branch, Message: publish.CommitMessage(pc), Files: files,
		Root: r.Stack.Path})
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("commit files: %w", err)
	}
	partial := requests.PublishedPR{Publisher: s.pub.Name(), Branch: branch, CommitSHA: ref.SHA, TreeSHA: ref.TreeSHA}
	pr, err := s.pub.OpenPullRequest(ctx, publish.PullRequestSpec{Head: branch, Title: publish.Title(pc), Body: publish.Body(pc)})
	if err != nil {
		return partial, fmt.Errorf("open pull request: %w", err)
	}
	partial.Number, partial.URL = pr.Number, pr.URL
	if err := s.pub.AddLabels(ctx, pr.Number, publish.Labels(pc)); err != nil {
		return partial, fmt.Errorf("label pull request: %w", err)
	}
	observability.Logger(ctx).InfoContext(ctx, "request published", slog.String("request", r.ShortID),
		slog.String("branch", branch), slog.Int("pr", pr.Number), slog.String("publisher", s.pub.Name()))
	return requests.PublishedPR{Publisher: s.pub.Name(), Branch: branch, CommitSHA: ref.SHA, TreeSHA: ref.TreeSHA,
		Number: pr.Number, URL: pr.URL, FencingToken: *r.FencingToken}, nil
}
