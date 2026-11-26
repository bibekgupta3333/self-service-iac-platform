package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clk struct{ t time.Time }

func (c *clk) Now() time.Time { return c.t }

func TestLimiterWindows(t *testing.T) {
	c := &clk{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	l := New(3, time.Minute, 100, c)
	for i := 1; i <= 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("event %d should pass", i)
		}
	}
	ok, retry := l.AllowN("a")
	if ok || retry != time.Minute {
		t.Fatalf("4th = %v, %v", ok, retry)
	}
	if !l.Allow("b") {
		t.Fatal("keys are independent")
	}
	c.t = c.t.Add(59 * time.Second)
	if l.Allow("a") {
		t.Fatal("still inside the window")
	}
	c.t = c.t.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("a new window starts after expiry")
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	c := &clk{t: time.Now()}
	l := New(1, time.Hour, 50, c)
	for i := range 10_000 {
		l.Allow(fmt.Sprintf("ip-%d", i))
		if l.Len() > 50 {
			t.Fatalf("table grew to %d", l.Len())
		}
	}
	// A flood of other keys never blocks a fresh key.
	if !l.Allow("fresh") {
		t.Fatal("a new key must be admitted when the table is full")
	}
}

func TestLimiterConcurrent(t *testing.T) {
	l := New(100, time.Minute, 10, &clk{t: time.Now()})
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if l.Allow("k") {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if allowed != 100 {
		t.Fatalf("allowed = %d, want exactly 100", allowed)
	}
}
