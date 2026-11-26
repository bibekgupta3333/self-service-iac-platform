// Package retention deletes finished queue rows on a schedule: processed outbox jobs and
// webhook delivery ids. It is a simple sweeper started like the OIDC replay sweeper; the
// deletes themselves live in the stores that own the tables.
package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// DefaultRetention is how long finished rows are kept (docs/03: webhook deliveries 7 days).
const DefaultRetention = 7 * 24 * time.Hour

// MinRetention bounds the setting from below: a delivery id must outlive GitHub's redelivery
// window, and a processed job is useful when debugging a publish.
const MinRetention = time.Hour

// DefaultInterval is how often the sweeper runs.
const DefaultInterval = time.Hour

// Batch bounds the rows one purge call may delete, so a large backlog never holds a long
// transaction; the sweeper repeats until a call returns fewer rows.
const Batch = 1000

// Purger deletes the rows of one table that were finished before a cutoff and returns how
// many it removed (at most limit).
type Purger interface {
	Name() string
	Purge(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// PurgerFunc adapts a function to Purger.
type PurgerFunc struct {
	Label string
	Fn    func(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// Name implements Purger.
func (p PurgerFunc) Name() string { return p.Label }

// Purge implements Purger.
func (p PurgerFunc) Purge(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return p.Fn(ctx, cutoff, limit)
}

// Cutoff is the instant before which finished rows are purged at now. Pure.
func Cutoff(now time.Time, retention time.Duration) time.Time { return now.Add(-retention) }

// ValidRetention reports whether d is an acceptable retention. Pure.
func ValidRetention(d time.Duration) error {
	if d < MinRetention {
		return fmt.Errorf("retention must be at least %s, got %s", MinRetention, d)
	}
	return nil
}

// Sweeper purges every Purger once per interval.
type Sweeper struct {
	purgers   []Purger
	retention time.Duration
	interval  time.Duration
	clock     platform.Clock
	logger    *slog.Logger
}

// NewSweeper returns a Sweeper; zero retention or interval take the defaults.
func NewSweeper(purgers []Purger, retention, interval time.Duration, clock platform.Clock, logger *slog.Logger) *Sweeper {
	if retention <= 0 {
		retention = DefaultRetention
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Sweeper{purgers: purgers, retention: retention, interval: interval, clock: clock, logger: logger}
}

// SweepOnce runs every purger to completion (in batches) and returns the rows removed per
// purger. A failing purger is logged and skipped; the others still run.
func (s *Sweeper) SweepOnce(ctx context.Context) map[string]int64 {
	cutoff := Cutoff(s.clock.Now(), s.retention)
	removed := make(map[string]int64, len(s.purgers))
	for _, p := range s.purgers {
		for ctx.Err() == nil {
			n, err := p.Purge(ctx, cutoff, Batch)
			removed[p.Name()] += n
			if err != nil {
				if ctx.Err() == nil {
					s.logger.WarnContext(ctx, "retention purge failed", "table", p.Name(), "error", err)
				}
				break
			}
			if n < Batch {
				break
			}
		}
		if n := removed[p.Name()]; n > 0 {
			s.logger.InfoContext(ctx, "retention purge", "table", p.Name(), "removed", n, "older_than", cutoff)
		}
	}
	return removed
}

// Run sweeps every interval until ctx ends. The first sweep happens after one interval, so
// start-up is not delayed by a backlog.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SweepOnce(ctx)
		}
	}
}
