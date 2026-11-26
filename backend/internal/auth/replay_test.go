package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// replayClock is a settable Clock.
type replayClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *replayClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *replayClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestJTIWindowRejectsReplayUntilExpiry(t *testing.T) {
	w := newJTIWindow(10)
	exp := now.Add(5 * time.Minute)
	if w.admit("a", exp, now) {
		t.Fatal("first use must be admitted")
	}
	if !w.admit("a", exp, now.Add(4*time.Minute)) {
		t.Fatal("reuse inside the window must be a replay")
	}
	if w.admit("a", exp.Add(time.Minute), exp) {
		t.Fatal("at exp the id has expired and may be admitted again")
	}
}

func TestJTIWindowDropsExpiredEntries(t *testing.T) {
	w := newJTIWindow(10)
	for i := range 5 {
		w.admit(fmt.Sprint(i), now.Add(time.Duration(i+1)*time.Minute), now)
	}
	w.admit("late", now.Add(time.Hour), now.Add(3*time.Minute))
	if w.len() != 3 { // ids 3 and 4 still live, plus "late"
		t.Fatalf("len = %d, want 3", w.len())
	}
}

func TestJTIWindowIsBounded(t *testing.T) {
	w := newJTIWindow(3)
	for i := range 3 {
		w.admit(fmt.Sprint(i), now.Add(time.Duration(10-i)*time.Minute), now) // "2" expires soonest
	}
	w.admit("new", now.Add(time.Hour), now)
	if w.len() != 3 {
		t.Fatalf("len = %d, want capacity 3", w.len())
	}
	if w.admit("0", now.Add(time.Hour), now) != true || w.admit("1", now.Add(time.Hour), now) != true {
		t.Fatal("entries that expire later must survive eviction")
	}
	if w.admit("2", now.Add(time.Hour), now) {
		t.Fatal("the soonest-expiring entry is the one evicted")
	}
}

func TestMemoryReplayCache(t *testing.T) {
	clock := &replayClock{t: now}
	c := NewMemoryReplayCache(2, clock)
	ctx := context.Background()
	exp := now.Add(5 * time.Minute)
	if err := c.Claim(ctx, "a", exp); err != nil {
		t.Fatal(err)
	}
	if err := c.Claim(ctx, "a", exp); !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("replay: err = %v", err)
	}
	clock.advance(6 * time.Minute)
	if err := c.Claim(ctx, "a", clock.Now().Add(time.Minute)); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	for i := range 50 {
		_ = c.Claim(ctx, fmt.Sprint(i), clock.Now().Add(time.Hour))
	}
	if c.Len() != 2 {
		t.Fatalf("len = %d, want 2", c.Len())
	}
	if NewMemoryReplayCache(0, clock).window.capacity != DefaultReplayCapacity {
		t.Fatal("non-positive capacity must fall back to the default")
	}
}

func TestMemoryReplayCacheConcurrentClaimsAdmitOnce(t *testing.T) {
	c := NewMemoryReplayCache(100, &replayClock{t: now})
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Claim(context.Background(), "same", now.Add(time.Minute)) == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("admitted %d times, want 1", admitted)
	}
}

func TestReplayKey(t *testing.T) {
	exp := jwt.NewNumericDate(now.Add(5 * time.Minute))
	id, until, err := ReplayKey(Claims{RegisteredClaims: jwt.RegisteredClaims{ID: "j1", ExpiresAt: exp}}, 30*time.Second)
	if err != nil || id != "j1" || !until.Equal(now.Add(5*time.Minute+30*time.Second)) {
		t.Fatalf("got %q %v %v", id, until, err)
	}
	for name, c := range map[string]Claims{
		"no jti":   {RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: exp}},
		"long jti": {RegisteredClaims: jwt.RegisteredClaims{ID: string(make([]byte, 129)), ExpiresAt: exp}},
		"no exp":   {RegisteredClaims: jwt.RegisteredClaims{ID: "j1"}},
	} {
		if _, _, err := ReplayKey(c, 0); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

type staticVerifier struct {
	claims Claims
	err    error
}

func (s staticVerifier) Verify(context.Context, string) (Claims, error) { return s.claims, s.err }

type failingStore struct{}

func (failingStore) Claim(context.Context, string, time.Time) error { return errors.New("db down") }

func TestReplayGuard(t *testing.T) {
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{ID: "jti-1", ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}}
	g := NewReplayGuard(staticVerifier{claims: claims}, NewMemoryReplayCache(10, &replayClock{t: now}), 30*time.Second)
	ctx := context.Background()
	if c, err := g.Verify(ctx, "tok"); err != nil || c.ID != "jti-1" {
		t.Fatalf("first use: %v", err)
	}
	_, err := g.Verify(ctx, "tok")
	if !errors.Is(err, ErrInvalidToken) || !errors.Is(err, ErrTokenReplayed) {
		t.Fatalf("replay must be an invalid token: %v", err)
	}

	inner := errors.New("bad signature")
	if _, err := NewReplayGuard(staticVerifier{err: inner}, failingStore{}, 0).Verify(ctx, "x"); !errors.Is(err, inner) {
		t.Fatalf("verification errors pass through before the store is touched: %v", err)
	}
	if _, err := NewReplayGuard(staticVerifier{claims: claims}, failingStore{}, 0).Verify(ctx, "x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("store failure must fail closed: %v", err)
	}
}
