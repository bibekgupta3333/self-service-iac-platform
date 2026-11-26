package auth

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// ErrTokenReplayed means a token's jti was already used within its validity window.
var ErrTokenReplayed = errors.New("token replayed")

// ReplayStore remembers token ids (jti) until they expire.
//
// Implementations: MemoryReplayCache (per process) and PostgresReplayStore (shared by
// every API replica, the production default).
type ReplayStore interface {
	// Claim records jti until expiresAt. It returns ErrTokenReplayed when jti is already
	// recorded and has not expired.
	Claim(ctx context.Context, jti string, expiresAt time.Time) error
}

// ClaimsVerifier is the verification step a ReplayGuard decorates (Verifier.Verify).
type ClaimsVerifier interface {
	Verify(ctx context.Context, raw string) (Claims, error)
}

// ReplayGuard rejects a verified token whose jti was already used. It runs after the
// full signature and allow-list checks, so junk tokens never consume store entries.
// A token stays claimed until exp plus the verifier leeway, the last moment the
// verifier would still accept it. Clients must request a fresh OIDC token per call.
type ReplayGuard struct {
	inner  ClaimsVerifier
	store  ReplayStore
	leeway time.Duration
}

// NewReplayGuard decorates inner with store.
func NewReplayGuard(inner ClaimsVerifier, store ReplayStore, leeway time.Duration) *ReplayGuard {
	return &ReplayGuard{inner: inner, store: store, leeway: leeway}
}

// ErrReplayStoreUnavailable is returned when the replay store cannot be reached. The
// token is refused (fail closed) with a 503, so callers retry and operators can tell an
// outage from a replay.
var ErrReplayStoreUnavailable = &apperr.Error{Kind: apperr.KindUnavailable, Code: "replay_store_unavailable",
	Message: "the token replay check is unavailable; retry with a fresh token"}

// Verify implements the TokenVerifier port. A replay wraps ErrInvalidToken (generic
// 401); a store failure wraps ErrReplayStoreUnavailable (503). Both refuse the token.
func (g *ReplayGuard) Verify(ctx context.Context, raw string) (Claims, error) {
	c, err := g.inner.Verify(ctx, raw)
	if err != nil {
		return Claims{}, err
	}
	jti, exp, err := ReplayKey(c, g.leeway)
	if err != nil {
		return Claims{}, err
	}
	switch err := g.store.Claim(ctx, jti, exp); {
	case errors.Is(err, ErrTokenReplayed):
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	case err != nil:
		return Claims{}, fmt.Errorf("%w: %w", ErrReplayStoreUnavailable, err)
	}
	return c, nil
}

// maxJTILen bounds the stored id; GitHub uses a UUID.
const maxJTILen = 128

// ReplayKey returns the jti and the instant until which it must be remembered. Pure.
func ReplayKey(c Claims, leeway time.Duration) (string, time.Time, error) {
	switch {
	case c.ID == "":
		return "", time.Time{}, fmt.Errorf("%w: token has no jti", ErrInvalidToken)
	case len(c.ID) > maxJTILen:
		return "", time.Time{}, fmt.Errorf("%w: jti longer than %d bytes", ErrInvalidToken, maxJTILen)
	case c.ExpiresAt == nil:
		return "", time.Time{}, fmt.Errorf("%w: token has no exp", ErrInvalidToken)
	}
	return c.ID, c.ExpiresAt.Add(leeway), nil
}

// DefaultReplayCapacity bounds MemoryReplayCache. GitHub tokens live minutes, so this
// is far above the number of machine calls one replica sees in a token lifetime.
const DefaultReplayCapacity = 10_000

// jtiWindow is the replay cache's bookkeeping: a map for lookups and a min-heap on
// expiry for eviction. It holds no clock, lock or I/O: callers pass now, so its
// behaviour is fully determined by its inputs (tests need no fakes).
type jtiWindow struct {
	capacity int
	expiry   map[string]time.Time
	order    expiryHeap
}

func newJTIWindow(capacity int) *jtiWindow {
	return &jtiWindow{capacity: capacity, expiry: make(map[string]time.Time)}
}

// admit records jti until exp and reports whether it was already live at now.
// Expired entries are dropped first. When the window is still full, the entry that
// expires soonest is evicted: the window stays bounded, at the cost of forgetting the
// token closest to its own expiry.
func (w *jtiWindow) admit(jti string, exp, now time.Time) (replayed bool) {
	w.expire(now)
	if _, live := w.expiry[jti]; live {
		return true
	}
	for len(w.expiry) >= w.capacity {
		w.evictOldest()
	}
	w.expiry[jti] = exp
	heap.Push(&w.order, heapEntry{jti: jti, exp: exp})
	return false
}

// expire drops every entry whose expiry is at or before now.
func (w *jtiWindow) expire(now time.Time) {
	for w.order.Len() > 0 && !w.order[0].exp.After(now) {
		w.evictOldest()
	}
}

func (w *jtiWindow) evictOldest() {
	e := heap.Pop(&w.order).(heapEntry)
	delete(w.expiry, e.jti)
}

func (w *jtiWindow) len() int { return len(w.expiry) }

type heapEntry struct {
	jti string
	exp time.Time
}

type expiryHeap []heapEntry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].exp.Before(h[j].exp) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(heapEntry)) }
func (h *expiryHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// MemoryReplayCache is a bounded, per-process ReplayStore. It only protects one API
// instance: with several replicas a token replayed against another replica is not
// caught, so production uses PostgresReplayStore (IACP_OIDC_REPLAY_STORE=postgres).
type MemoryReplayCache struct {
	mu     sync.Mutex
	window *jtiWindow
	clock  platform.Clock
}

// NewMemoryReplayCache returns a cache holding at most capacity live ids.
func NewMemoryReplayCache(capacity int, clock platform.Clock) *MemoryReplayCache {
	if capacity <= 0 {
		capacity = DefaultReplayCapacity
	}
	return &MemoryReplayCache{window: newJTIWindow(capacity), clock: clock}
}

// Claim implements ReplayStore.
func (c *MemoryReplayCache) Claim(_ context.Context, jti string, expiresAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window.admit(jti, expiresAt, c.clock.Now()) {
		return ErrTokenReplayed
	}
	return nil
}

// Len returns the number of live ids (for tests and metrics).
func (c *MemoryReplayCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.window.len()
}
