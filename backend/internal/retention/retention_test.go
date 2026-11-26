package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCutoffAndValidation(t *testing.T) {
	if got := Cutoff(now, 48*time.Hour); !got.Equal(now.Add(-48 * time.Hour)) {
		t.Fatalf("cutoff = %s", got)
	}
	if ValidRetention(time.Minute) == nil || ValidRetention(0) == nil {
		t.Fatal("a retention under the minimum must be refused")
	}
	if err := ValidRetention(MinRetention); err != nil {
		t.Fatal(err)
	}
}

// backlog is a Purger holding n finished rows.
type backlog struct {
	mu      sync.Mutex
	name    string
	rows    int64
	cutoffs []time.Time
	err     error
}

func (b *backlog) Name() string { return b.name }
func (b *backlog) Purge(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cutoffs = append(b.cutoffs, cutoff)
	if b.err != nil {
		return 0, b.err
	}
	n := min(b.rows, int64(limit))
	b.rows -= n
	return n, nil
}

func TestSweepOnceDrainsInBatchesWithTheRetentionCutoff(t *testing.T) {
	a := &backlog{name: "outbox", rows: 2*Batch + 5}
	s := NewSweeper([]Purger{a}, 24*time.Hour, 0, platform.FixedClock{T: now}, quiet())
	removed := s.SweepOnce(context.Background())
	if removed["outbox"] != 2*Batch+5 || a.rows != 0 {
		t.Fatalf("removed %v, %d rows left", removed, a.rows)
	}
	if len(a.cutoffs) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(a.cutoffs))
	}
	for _, c := range a.cutoffs {
		if !c.Equal(now.Add(-24 * time.Hour)) {
			t.Fatalf("cutoff = %s", c)
		}
	}
}

func TestSweepOnceKeepsGoingAfterAFailingPurger(t *testing.T) {
	bad := &backlog{name: "outbox", err: errors.New("boom")}
	good := &backlog{name: "webhook_deliveries", rows: 3}
	removed := NewSweeper([]Purger{bad, good}, 0, 0, platform.FixedClock{T: now}, quiet()).SweepOnce(context.Background())
	if removed["webhook_deliveries"] != 3 || removed["outbox"] != 0 || len(bad.cutoffs) != 1 {
		t.Fatalf("removed %v, bad purger called %d times", removed, len(bad.cutoffs))
	}
}

func TestDefaultsAndRunStopsWithItsContext(t *testing.T) {
	s := NewSweeper(nil, 0, 0, platform.FixedClock{T: now}, quiet())
	if s.retention != DefaultRetention || s.interval != DefaultInterval {
		t.Fatalf("defaults not applied: %+v", s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored its context")
	}
}

func TestRunSweepsEveryInterval(t *testing.T) {
	b := &backlog{name: "outbox", rows: 1}
	s := NewSweeper([]Purger{b}, time.Hour, 5*time.Millisecond, platform.FixedClock{T: now}, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("the sweeper never ran")
		case <-time.After(10 * time.Millisecond):
		}
		b.mu.Lock()
		ran := len(b.cutoffs) > 0
		b.mu.Unlock()
		if ran {
			return
		}
	}
}
