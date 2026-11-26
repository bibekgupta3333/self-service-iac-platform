// Package outbox is the Postgres-backed job queue (table outbox) and the in-process
// worker that drains it.
//
// Producers insert messages with Enqueue inside their own transaction, so a job exists
// if and only if the state change that asked for it committed (transactional outbox).
// The worker claims one job at a time with FOR UPDATE SKIP LOCKED and a lease, runs the
// topic's handler outside any transaction, and marks the job processed, schedules a
// retry with exponential backoff, or gives up after MaxAttempts. Several workers (API
// replicas) can drain the same table safely. Publishing to SQS instead is a later adapter
// (docs/03 §8 queue/), the Handler contract stays the same.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// Topics used by the platform.
const (
	TopicRequestPublish = "request.publish" // render and publish a request's PR
	TopicGitHubEvent    = "github.event"    // apply a mapped GitHub webhook command
)

// Message is a job to enqueue.
type Message struct {
	Topic       string
	AggregateID *uuid.UUID // request id when known
	// DedupeKey, when set, keeps at most one pending job with that key: a second Enqueue
	// is a no-op while the first is pending.
	DedupeKey   string
	Payload     json.RawMessage
	AvailableAt time.Time // zero means now
}

// Job is a claimed message.
type Job struct {
	ID          int64
	Topic       string
	AggregateID *uuid.UUID
	Payload     json.RawMessage
	Attempts    int // including the current one
	MaxAttempts int // the worker gives up after this attempt
}

// Last reports whether this is the final attempt.
func (j Job) Last() bool { return j.Attempts >= j.MaxAttempts }

// Handler processes one job. Returning nil marks the job processed; an error schedules a
// retry, unless it wraps ErrPermanent or this was the last attempt.
type Handler interface {
	Handle(ctx context.Context, j Job) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, j Job) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, j Job) error { return f(ctx, j) }

// ErrPermanent marks a failure that retrying cannot fix (wrap it with fmt.Errorf("…: %w")).
var ErrPermanent = errors.New("permanent failure")

// longerRetry is an error that may be retried for more attempts than the worker default.
type longerRetry struct {
	err      error
	attempts int
}

func (e *longerRetry) Error() string { return e.err.Error() }
func (e *longerRetry) Unwrap() error { return e.err }

// RetryLonger marks err as expected to clear up given time (for example a webhook that
// raced the write it refers to): the job is retried with the usual backoff for up to
// attempts tries instead of Options.MaxAttempts.
func RetryLonger(err error, attempts int) error { return &longerRetry{err: err, attempts: attempts} }

// MaxAttemptsFor is the attempt budget for a job that failed with err. Pure.
func MaxAttemptsFor(err error, def int) int {
	var lr *longerRetry
	if errors.As(err, &lr) && lr.attempts > def {
		return lr.attempts
	}
	return def
}

// ErrClaimLost means the job was reclaimed by another worker (this claim's lease ran out),
// so this worker's outcome is discarded.
var ErrClaimLost = errors.New("job claim lost")

// Store is the queue persistence port. Outcome writes are guarded by the attempt number
// returned by Claim (the claim token): they return ErrClaimLost when the job has been
// claimed again since.
type Store interface {
	// Claim leases the oldest available job until now+lease and increments its attempts;
	// nil when none is available.
	Claim(ctx context.Context, now time.Time, lease time.Duration) (*Job, error)
	Complete(ctx context.Context, id int64, attempt int, at time.Time) error
	Retry(ctx context.Context, id int64, attempt int, availableAt time.Time, lastErr string) error
	Dead(ctx context.Context, id int64, attempt int, at time.Time, lastErr string) error
}

// Backoff is the delay before retry number attempt (1-based): 2 s doubling, capped at
// 5 minutes. Pure.
func Backoff(attempt int) time.Duration {
	const base, ceiling = 2 * time.Second, 5 * time.Minute
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		return ceiling
	}
	d := base << (attempt - 1)
	return min(d, ceiling)
}

// truncate bounds error text stored in last_error.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Options tunes a Worker; zero values take the defaults.
type Options struct {
	PollInterval time.Duration // idle wait between polls; default 1 s
	Lease        time.Duration // how long a claim is held; default 2 minutes
	// HandlerTimeout bounds one handler run; it must be shorter than Lease so a job is
	// never handled after its lease could have been reclaimed. Default 3/4 of Lease.
	HandlerTimeout time.Duration
	MaxAttempts    int // default 6
	Logger         *slog.Logger
}

// Worker drains the outbox.
type Worker struct {
	store    Store
	handlers map[string]Handler
	clock    platform.Clock
	opts     Options
	wake     chan struct{}
}

// NewWorker returns a Worker dispatching jobs to handlers by topic.
func NewWorker(store Store, handlers map[string]Handler, clock platform.Clock, opts Options) *Worker {
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.Lease <= 0 {
		opts.Lease = 2 * time.Minute
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 6
	}
	if opts.HandlerTimeout <= 0 || opts.HandlerTimeout >= opts.Lease {
		opts.HandlerTimeout = opts.Lease * 3 / 4
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Worker{store: store, handlers: handlers, clock: clock, opts: opts, wake: make(chan struct{}, 1)}
}

// Wake makes an idle worker poll now (for example right after a request is created).
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes jobs until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.opts.Logger.Info("outbox worker started", "poll_interval", w.opts.PollInterval.String())
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.opts.Logger.Info("outbox worker stopped")
			return
		case <-t.C:
		case <-w.wake:
		}
		for ctx.Err() == nil {
			worked, err := w.RunOnce(ctx)
			if err != nil {
				w.opts.Logger.Error("outbox poll failed", "error", err)
				break
			}
			if !worked {
				break
			}
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(w.opts.PollInterval)
	}
}

// RunOnce claims and processes at most one job. It reports whether a job was found; the
// error is only about the queue itself, never the handler's outcome.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	j, err := w.store.Claim(ctx, w.clock.Now(), w.opts.Lease)
	if err != nil {
		return false, fmt.Errorf("claim job: %w", err)
	}
	if j == nil {
		return false, nil
	}
	j.MaxAttempts = w.opts.MaxAttempts
	log := w.opts.Logger.With("job_id", j.ID, "topic", j.Topic, "attempt", j.Attempts)

	h, ok := w.handlers[j.Topic]
	var herr error
	if !ok {
		herr = fmt.Errorf("no handler for topic %q: %w", j.Topic, ErrPermanent)
	} else {
		hctx, cancel := context.WithTimeout(ctx, w.opts.HandlerTimeout)
		herr = w.safeHandle(hctx, h, *j)
		cancel()
	}
	now := w.clock.Now()
	switch {
	case herr == nil:
		err = w.store.Complete(ctx, j.ID, j.Attempts, now)
	case errors.Is(herr, ErrPermanent) || j.Attempts >= MaxAttemptsFor(herr, j.MaxAttempts):
		log.Error("job failed permanently", "error", herr)
		err = w.store.Dead(ctx, j.ID, j.Attempts, now, truncate(herr.Error(), 2000))
	default:
		delay := Backoff(j.Attempts)
		log.Warn("job failed, will retry", "error", herr, "retry_in", delay.String())
		err = w.store.Retry(ctx, j.ID, j.Attempts, now.Add(delay), truncate(herr.Error(), 2000))
	}
	if errors.Is(err, ErrClaimLost) {
		log.Warn("job was reclaimed by another worker; outcome discarded")
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("record outcome of job %d: %w", j.ID, err)
	}
	return true, nil
}

// safeHandle turns a handler panic into an error so one bad job cannot stop the worker.
func (w *Worker) safeHandle(ctx context.Context, h Handler, j Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	return h.Handle(ctx, j)
}
