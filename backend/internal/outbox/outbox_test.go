package outbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

type lostStore struct{ memStore }

func (l *lostStore) Complete(context.Context, int64, int, time.Time) error { return ErrClaimLost }

func TestHandlerDeadlineAndLostClaim(t *testing.T) {
	store := &lostStore{}
	store.add("t")
	var deadline time.Time
	w := NewWorker(store, map[string]Handler{"t": HandlerFunc(func(ctx context.Context, _ Job) error {
		deadline, _ = ctx.Deadline()
		return nil
	})}, platform.SystemClock{}, Options{Lease: time.Minute, Logger: quiet()})
	start := time.Now()
	worked, err := w.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("a lost claim is not a queue error: worked=%v err=%v", worked, err)
	}
	if d := deadline.Sub(start); deadline.IsZero() || d > 46*time.Second || d < 44*time.Second {
		t.Fatalf("handler deadline %v after start, want 3/4 of the lease", d)
	}
}

type memJob struct {
	Job
	availableAt time.Time
	done, dead  bool
	lastErr     string
}

type memStore struct {
	mu   sync.Mutex
	jobs []*memJob
}

func (m *memStore) add(topic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, &memJob{Job: Job{ID: int64(len(m.jobs) + 1), Topic: topic}})
}

func (m *memStore) Claim(_ context.Context, now time.Time, _ time.Duration) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if !j.done && !j.dead && !j.availableAt.After(now) {
			j.Attempts++
			c := j.Job
			return &c, nil
		}
	}
	return nil, nil
}

func (m *memStore) find(id int64) *memJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id-1]
}

func (m *memStore) Complete(_ context.Context, id int64, _ int, _ time.Time) error {
	m.find(id).done = true
	return nil
}

func (m *memStore) Retry(_ context.Context, id int64, _ int, at time.Time, e string) error {
	j := m.find(id)
	j.availableAt, j.lastErr = at, e
	return nil
}

func (m *memStore) Dead(_ context.Context, id int64, _ int, _ time.Time, e string) error {
	j := m.find(id)
	j.dead, j.lastErr = true, e
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWorkerOutcomes(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		topic    string
		handler  Handler
		runs     int
		wantDone bool
		wantDead bool
		wantErr  string
		attempts int
	}{
		{name: "success", topic: "ok", handler: HandlerFunc(func(context.Context, Job) error { return nil }),
			runs: 1, wantDone: true, attempts: 1},
		{name: "transient then success", topic: "flaky", handler: flaky(2), runs: 3, wantDone: true, attempts: 3},
		{name: "gives up after max attempts", topic: "broken", handler: HandlerFunc(func(context.Context, Job) error {
			return errors.New("boom")
		}), runs: 3, wantDead: true, wantErr: "boom", attempts: 3},
		{name: "permanent error is not retried", topic: "perm", handler: HandlerFunc(func(context.Context, Job) error {
			return fmt.Errorf("bad payload: %w", ErrPermanent)
		}), runs: 1, wantDead: true, wantErr: "bad payload", attempts: 1},
		{name: "panic is contained", topic: "panic", handler: HandlerFunc(func(context.Context, Job) error {
			panic("kaboom")
		}), runs: 3, wantDead: true, wantErr: "handler panic: kaboom", attempts: 3},
		{name: "unknown topic", topic: "nobody", runs: 1, wantDead: true, wantErr: `no handler for topic "nobody"`, attempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			store.add(tt.topic)
			clock := &platform.FixedClock{T: t0}
			handlers := map[string]Handler{}
			if tt.handler != nil {
				handlers[tt.topic] = tt.handler
			}
			w := NewWorker(store, handlers, clock, Options{MaxAttempts: 3, Logger: quiet()})
			for i := 0; i < tt.runs; i++ {
				worked, err := w.RunOnce(context.Background())
				if err != nil || !worked {
					t.Fatalf("run %d: worked=%v err=%v", i, worked, err)
				}
				clock.T = clock.T.Add(10 * time.Minute) // past any backoff
			}
			if worked, _ := w.RunOnce(context.Background()); worked {
				t.Fatal("job should be finished")
			}
			j := store.find(1)
			if j.done != tt.wantDone || j.dead != tt.wantDead || j.Attempts != tt.attempts {
				t.Fatalf("job = %+v", j)
			}
			if tt.wantErr != "" && !strings.Contains(j.lastErr, tt.wantErr) {
				t.Fatalf("last error %q, want %q", j.lastErr, tt.wantErr)
			}
		})
	}
}

func flaky(failures int) Handler {
	n := 0
	return HandlerFunc(func(_ context.Context, _ Job) error {
		n++
		if n <= failures {
			return errors.New("transient")
		}
		return nil
	})
}

func TestRetryWaitsForBackoff(t *testing.T) {
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	store := &memStore{}
	store.add("flaky")
	clock := &platform.FixedClock{T: t0}
	w := NewWorker(store, map[string]Handler{"flaky": flaky(1)}, clock, Options{Logger: quiet()})
	if worked, _ := w.RunOnce(context.Background()); !worked {
		t.Fatal("first run must claim the job")
	}
	if got := store.find(1).availableAt; !got.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("retry at %s", got)
	}
	if worked, _ := w.RunOnce(context.Background()); worked {
		t.Fatal("job must not be claimed before its backoff")
	}
	clock.T = t0.Add(2 * time.Second)
	if worked, _ := w.RunOnce(context.Background()); !worked || !store.find(1).done {
		t.Fatal("job must succeed after the backoff")
	}
}

func TestBackoff(t *testing.T) {
	tests := map[int]time.Duration{0: 2 * time.Second, 1: 2 * time.Second, 2: 4 * time.Second, 5: 32 * time.Second,
		8: 256 * time.Second, 9: 5 * time.Minute, 50: 5 * time.Minute}
	for attempt, want := range tests {
		if got := Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %s, want %s", attempt, got, want)
		}
	}
}

func TestRunStopsOnCancelAndWakes(t *testing.T) {
	store := &memStore{}
	done := make(chan struct{}, 4)
	w := NewWorker(store, map[string]Handler{"t": HandlerFunc(func(context.Context, Job) error {
		done <- struct{}{}
		return nil
	})}, platform.SystemClock{}, Options{PollInterval: time.Hour, Logger: quiet()})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { w.Run(ctx); close(stopped) }()

	store.add("t")
	w.Wake()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wake did not trigger a poll")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
