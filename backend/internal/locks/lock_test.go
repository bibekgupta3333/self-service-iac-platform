package locks

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

var (
	t0      = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	stackA  = uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000001")
	reqA    = uuid.MustParse("11111111-0000-4000-8000-000000000001")
	reqB    = uuid.MustParse("22222222-0000-4000-8000-000000000002")
	reqC    = uuid.MustParse("33333333-0000-4000-8000-000000000003")
	reqD    = uuid.MustParse("44444444-0000-4000-8000-000000000004")
	heldByA = &Lock{StackID: stackA, RequestID: reqA, Holder: "request:req-a", FencingToken: 7, AcquiredAt: t0, ExpiresAt: t0.Add(time.Hour)}
)

func TestLockExpired(t *testing.T) {
	tests := []struct {
		at   time.Time
		want bool
	}{
		{t0, false},
		{t0.Add(time.Hour - time.Nanosecond), false},
		{t0.Add(time.Hour), true},
		{t0.Add(2 * time.Hour), true},
	}
	for _, tt := range tests {
		if got := heldByA.Expired(tt.at); got != tt.want {
			t.Errorf("Expired(%s) = %v, want %v", tt.at, got, tt.want)
		}
	}
}

func TestNextInQueue(t *testing.T) {
	tests := []struct {
		name    string
		waiters []Waiter
		exclude uuid.UUID
		want    uuid.UUID
		ok      bool
	}{
		{name: "empty", ok: false},
		{name: "oldest wins", waiters: []Waiter{{reqC, t0.Add(2 * time.Minute)}, {reqB, t0.Add(time.Minute)}, {reqD, t0.Add(3 * time.Minute)}},
			want: reqB, ok: true},
		{name: "same time breaks ties by id", waiters: []Waiter{{reqC, t0}, {reqB, t0}}, want: reqB, ok: true},
		{name: "excluded is skipped", waiters: []Waiter{{reqB, t0}, {reqC, t0.Add(time.Minute)}}, exclude: reqB, want: reqC, ok: true},
		{name: "only the excluded one", waiters: []Waiter{{reqB, t0}}, exclude: reqB, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := append([]Waiter(nil), tt.waiters...)
			got, ok := NextInQueue(tt.waiters, tt.exclude)
			if ok != tt.ok || got.RequestID != tt.want {
				t.Fatalf("NextInQueue = %v, %v; want %v, %v", got.RequestID, ok, tt.want, tt.ok)
			}
			for i := range in {
				if in[i] != tt.waiters[i] {
					t.Fatal("NextInQueue must not modify its input")
				}
			}
		})
	}
}

func TestDecide(t *testing.T) {
	expiredA := *heldByA
	expiredA.ExpiresAt = t0.Add(-time.Second)
	waitB := []Waiter{{reqB, t0.Add(-time.Minute)}}

	tests := []struct {
		name     string
		current  *Lock
		caller   uuid.UUID
		waiters  []Waiter
		want     Decision
		replaced bool
	}{
		{name: "free lock is granted", caller: reqA,
			want: Decision{Outcome: OutcomeGranted, Grantee: reqA}},
		{name: "holder re-acquires", current: heldByA, caller: reqA,
			want: Decision{Outcome: OutcomeHeld}},
		{name: "holder renews an expired lease nobody took", current: &expiredA, caller: reqA,
			want: Decision{Outcome: OutcomeHeld}},
		{name: "live lease queues the caller", current: heldByA, caller: reqB,
			want: Decision{Outcome: OutcomeQueued, Behind: reqA}},
		{name: "live lease queues even with waiters", current: heldByA, caller: reqC, waiters: waitB,
			want: Decision{Outcome: OutcomeQueued, Behind: reqA}},
		{name: "expired lease is taken over", current: &expiredA, caller: reqB,
			want: Decision{Outcome: OutcomeGranted, Grantee: reqB}, replaced: true},
		{name: "head of queue takes a free lock", caller: reqB, waiters: waitB,
			want: Decision{Outcome: OutcomeGranted, Grantee: reqB}},
		{name: "newcomer cannot jump the queue on a free lock", caller: reqC, waiters: waitB,
			want: Decision{Outcome: OutcomeQueued, Grantee: reqB, Behind: reqB}},
		{name: "newcomer cannot jump the queue on an expired lease", current: &expiredA, caller: reqC, waiters: waitB,
			want: Decision{Outcome: OutcomeQueued, Grantee: reqB, Behind: reqB}, replaced: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(t0, tt.current, tt.caller, tt.waiters)
			if (got.Replaced != nil) != tt.replaced {
				t.Fatalf("Replaced = %+v, want replaced=%v", got.Replaced, tt.replaced)
			}
			if tt.replaced && got.Replaced.FencingToken != expiredA.FencingToken {
				t.Fatalf("Replaced = %+v", got.Replaced)
			}
			got.Replaced = nil
			if got != tt.want {
				t.Fatalf("Decide = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCheckToken(t *testing.T) {
	tests := []struct {
		name    string
		current *Lock
		request uuid.UUID
		token   int64
		ok      bool
	}{
		{name: "current holder and token", current: heldByA, request: reqA, token: 7, ok: true},
		{name: "not locked", request: reqA, token: 7},
		{name: "other holder", current: heldByA, request: reqB, token: 7},
		{name: "superseded token", current: heldByA, request: reqA, token: 6},
		{name: "future token", current: heldByA, request: reqA, token: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckToken(tt.current, tt.request, tt.token)
			if tt.ok != (err == nil) {
				t.Fatalf("CheckToken = %v", err)
			}
			if err != nil {
				if e, ok := apperr.As(err); !ok || e.Code != "stale_fencing_token" || e.Kind != apperr.KindConflict {
					t.Fatalf("want stale_fencing_token conflict, got %v", err)
				}
			}
		})
	}
	if CheckStackToken(heldByA, 7) != nil || CheckStackToken(heldByA, 6) == nil || CheckStackToken(nil, 7) == nil {
		t.Fatal("CheckStackToken must accept only the current token")
	}
}

func TestHolderAndLease(t *testing.T) {
	if HolderFor("req-7k3p9x") != "request:req-7k3p9x" {
		t.Fatal("HolderFor")
	}
	if !LeaseUntil(t0, DefaultLease).Equal(t0.Add(7 * 24 * time.Hour)) {
		t.Fatal("LeaseUntil")
	}
}
