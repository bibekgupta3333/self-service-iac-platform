package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Delivery is one verified webhook delivery.
type Delivery struct {
	ID    uuid.UUID // X-GitHub-Delivery
	Event string    // X-GitHub-Event
	Body  []byte
}

// Result reports what Ingest did.
type Result struct {
	Duplicate bool // the delivery id was seen before; nothing was enqueued
	Enqueued  bool // a command was queued for the worker
}

// Store is the ingester's transactional persistence port.
type Store interface {
	// RecordDelivery inserts the delivery id; false when it already exists.
	RecordDelivery(ctx context.Context, id uuid.UUID, event string, at time.Time) (bool, error)
	Enqueue(ctx context.Context, m outbox.Message) (bool, error)
}

// Repository runs a function in one transaction.
type Repository interface {
	InTx(ctx context.Context, fn func(Store) error) error
}

// Ingester dedupes deliveries and enqueues their commands.
type Ingester struct {
	repo       Repository
	clock      platform.Clock
	repository string // owner/name accepted; empty accepts any (signature still required)
}

// NewIngester returns an Ingester. repository, when set, drops events from other repos.
func NewIngester(repo Repository, clock platform.Clock, repository string) *Ingester {
	return &Ingester{repo: repo, clock: clock, repository: repository}
}

// Ingest maps d and, in one transaction, records its id and enqueues the command. A
// malformed payload of a known event returns ErrMalformedPayload (wrapped).
func (in *Ingester) Ingest(ctx context.Context, d Delivery) (Result, error) {
	cmd, err := Map(d.Event, d.ID.String(), d.Body)
	if err != nil {
		return Result{}, err
	}
	if cmd != nil && in.repository != "" && !strings.EqualFold(cmd.Repository, in.repository) {
		cmd = nil // signed by our App, but for a repository the platform does not manage
	}
	var res Result
	err = in.repo.InTx(ctx, func(st Store) error {
		fresh, err := st.RecordDelivery(ctx, d.ID, d.Event, in.clock.Now())
		if err != nil {
			return fmt.Errorf("record delivery: %w", err)
		}
		if !fresh {
			res.Duplicate = true
			return nil
		}
		if cmd == nil {
			return nil
		}
		payload, err := json.Marshal(cmd)
		if err != nil {
			return fmt.Errorf("encode command: %w", err)
		}
		if _, err := st.Enqueue(ctx, outbox.Message{Topic: outbox.TopicGitHubEvent, Payload: payload, AvailableAt: in.clock.Now()}); err != nil {
			return err
		}
		res.Enqueued = true
		return nil
	})
	return res, err
}

// PostgresRepository implements Repository.
type PostgresRepository struct {
	pool *pgxpool.Pool
	tx   *db.TxRunner
}

// NewPostgresRepository returns a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool, tx: db.NewTxRunner(pool)}
}

// InTx implements Repository.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(Store) error) error {
	return r.tx.InTx(ctx, func(tx pgx.Tx) error { return fn(pgStore{tx: tx}) })
}

// PurgeDeliveries deletes delivery ids received before cutoff (docs/03: keep 7 days).
func (r *PostgresRepository) PurgeDeliveries(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM webhook_deliveries WHERE received_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge webhook deliveries: %w", err)
	}
	return tag.RowsAffected(), nil
}

type pgStore struct{ tx pgx.Tx }

func (s pgStore) RecordDelivery(ctx context.Context, id uuid.UUID, event string, at time.Time) (bool, error) {
	tag, err := s.tx.Exec(ctx, `INSERT INTO webhook_deliveries (delivery_id, event, received_at) VALUES ($1, $2, $3)
		ON CONFLICT (delivery_id) DO NOTHING`, id, event, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s pgStore) Enqueue(ctx context.Context, m outbox.Message) (bool, error) {
	return outbox.Enqueue(ctx, s.tx, m)
}

// RequestEvents is the part of requests.Service the Applier drives.
type RequestEvents interface {
	ApplyGitHubEvent(ctx context.Context, e requests.GitHubEvent) (requests.EventOutcome, error)
	RecordWorkflowHint(ctx context.Context, h requests.WorkflowHint) (bool, error)
}

// PRHeads reads a PR's current head (publish.Publisher implements it).
type PRHeads interface {
	PullRequestHead(ctx context.Context, number int) (string, error)
}

// Applier is the outbox handler for github.event commands.
type Applier struct {
	reqs  RequestEvents
	heads PRHeads // nil without a publisher: stale pushes are only audited
}

// NewApplier returns an Applier. heads may be nil.
func NewApplier(reqs RequestEvents, heads PRHeads) *Applier {
	return &Applier{reqs: reqs, heads: heads}
}

var _ outbox.Handler = (*Applier)(nil)

// Handle implements outbox.Handler. Transitions that are not allowed are recorded by the
// request service and not retried; database errors are.
func (a *Applier) Handle(ctx context.Context, j outbox.Job) error {
	var c Command
	if err := json.Unmarshal(j.Payload, &c); err != nil {
		return fmt.Errorf("decode command %d: %w", j.ID, outbox.ErrPermanent)
	}
	switch c.Kind {
	case KindTransition:
		e := requests.GitHubEvent{RequestShortID: c.RequestShortID, PRNumber: c.PRNumber,
			Event: c.RequestEvent, MergeCommitSHA: c.MergeCommitSHA, DeliveryID: c.DeliveryID, Action: c.Action,
			HeadSHA: c.HeadSHA, BeforeSHA: c.BeforeSHA}
		out, err := a.reqs.ApplyGitHubEvent(ctx, e)
		if err != nil {
			return err
		}
		return a.resync(ctx, e, out)
	case KindHint:
		_, err := a.reqs.RecordWorkflowHint(ctx, requests.WorkflowHint{RequestShortID: c.RequestShortID, Workflow: c.Workflow,
			RunID: c.RunID, Conclusion: c.Conclusion, HeadSHA: c.HeadSHA, URL: c.URL, DeliveryID: c.DeliveryID})
		return err
	}
	return errors.Join(fmt.Errorf("unknown command kind %q", c.Kind), outbox.ErrPermanent)
}

// resync handles a push whose before SHA did not match the known head (a lost or
// reordered synchronize). Instead of dropping it, it reads the PR's real head and, when
// that differs from the known head, applies one push from the known head to the real
// one. A replayed old push finds the head unchanged and does nothing.
func (a *Applier) resync(ctx context.Context, e requests.GitHubEvent, out requests.EventOutcome) error {
	if a.heads == nil || out.Reason != "stale_head" || e.Event != requests.EventNewCommit || out.KnownHead == "" {
		return nil
	}
	head, err := a.heads.PullRequestHead(ctx, e.PRNumber)
	if err != nil {
		return fmt.Errorf("resync head of PR #%d: %w", e.PRNumber, err) // retried by the outbox
	}
	if head == "" || head == out.KnownHead {
		return nil
	}
	e.BeforeSHA, e.HeadSHA, e.Action = out.KnownHead, head, "synchronize (resynced from GitHub)"
	_, err = a.reqs.ApplyGitHubEvent(ctx, e)
	return err
}
