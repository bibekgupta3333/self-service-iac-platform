// Package ratelimit is a small fixed-window limiter with bounded memory. It is per process:
// with several replicas the effective limit is multiplied by the replica count, which is
// acceptable for abuse damping (login starts, denial audit volume) and not for quotas.
package ratelimit

import (
	"sync"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

type window struct {
	start time.Time
	count int
}

// Limiter allows at most Limit events per key in each Window, tracking at most MaxKeys keys.
// When the table is full, expired windows are dropped first, then an arbitrary key is
// evicted, so memory stays bounded and a flood of distinct keys cannot lock anyone out.
type Limiter struct {
	limit   int
	window  time.Duration
	maxKeys int
	clock   platform.Clock

	mu      sync.Mutex
	entries map[string]*window
}

// New returns a Limiter. limit and maxKeys are clamped to at least 1.
func New(limit int, per time.Duration, maxKeys int, clock platform.Clock) *Limiter {
	return &Limiter{limit: max(limit, 1), window: per, maxKeys: max(maxKeys, 1), clock: clock, entries: map[string]*window{}}
}

// Allow records one event for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	ok, _ := l.AllowN(key)
	return ok
}

// AllowN is Allow, also returning the time until the key's window resets.
func (l *Limiter) AllowN(key string) (bool, time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.entries[key]
	if !ok || !now.Before(w.start.Add(l.window)) {
		if !ok && len(l.entries) >= l.maxKeys {
			l.evict(now)
		}
		w = &window{start: now}
		l.entries[key] = w
	}
	w.count++
	return w.count <= l.limit, w.start.Add(l.window).Sub(now)
}

func (l *Limiter) evict(now time.Time) {
	for k, w := range l.entries {
		if !now.Before(w.start.Add(l.window)) {
			delete(l.entries, k)
		}
	}
	if len(l.entries) < l.maxKeys {
		return
	}
	for k := range l.entries {
		delete(l.entries, k)
		return
	}
}

// Len reports how many keys are tracked (tests).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
