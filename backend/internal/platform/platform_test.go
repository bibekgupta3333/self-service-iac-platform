package platform

import (
	"strings"
	"testing"
	"time"
)

func TestNewShortID(t *testing.T) {
	ids := RandomIDs{}
	seen := map[string]bool{}
	for range 10000 {
		id := ids.NewShortID("req")
		suffix, ok := strings.CutPrefix(id, "req-")
		if !ok || len(suffix) != ShortIDLen || strings.Trim(suffix, shortAlphabet) != "" {
			t.Fatalf("bad short id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate short id %q in 10k draws", id)
		}
		seen[id] = true
	}
	if ShortIDLen < 10 {
		t.Fatal("short ids must stay long enough to make UNIQUE collisions negligible")
	}
}

func TestClocks(t *testing.T) {
	now := SystemClock{}.Now()
	if now.Location() != time.UTC || now.Nanosecond()%1000 != 0 {
		t.Fatalf("SystemClock must return UTC with microsecond precision: %v", now)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !(FixedClock{T: fixed}).Now().Equal(fixed) {
		t.Fatal("FixedClock")
	}
}
