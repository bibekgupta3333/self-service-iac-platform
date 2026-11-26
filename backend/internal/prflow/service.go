package prflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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
	if rerr := s.reqs.RecordPublishFailure(ctx, r.ID, requests.PublishFailure{Publisher: s.pub.Name(), Attempt: j.Attempts,
		Error: err.Error(), Final: final}, correlationID); rerr != nil {
		return fmt.Errorf("publish %s: %w (recording the failure: %w)", r.ShortID, err, rerr)
	}
	if permanent {
		return fmt.Errorf("publish %s: %w: %w", r.ShortID, err, outbox.ErrPermanent)
	}
	return fmt.Errorf("publish %s: %w", r.ShortID, err)
}

// Publish renders the request's stack and writes it through the publisher: commit on
// iacp/<short id>, open the PR, add labels. It does not touch the database.
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
	pr, err := s.pub.OpenPullRequest(ctx, publish.PullRequestSpec{Head: branch, Title: publish.Title(pc), Body: publish.Body(pc)})
	if err != nil {
		return requests.PublishedPR{}, fmt.Errorf("open pull request: %w", err)
	}
	if err := s.pub.AddLabels(ctx, pr.Number, publish.Labels(pc)); err != nil {
		return requests.PublishedPR{}, fmt.Errorf("label pull request: %w", err)
	}
	observability.Logger(ctx).InfoContext(ctx, "request published", slog.String("request", r.ShortID),
		slog.String("branch", branch), slog.Int("pr", pr.Number), slog.String("publisher", s.pub.Name()))
	return requests.PublishedPR{Publisher: s.pub.Name(), Branch: branch, CommitSHA: ref.SHA, TreeSHA: ref.TreeSHA,
		Number: pr.Number, URL: pr.URL, FencingToken: *r.FencingToken}, nil
}
