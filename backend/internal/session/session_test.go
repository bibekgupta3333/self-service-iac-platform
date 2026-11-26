package session

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testClock struct{ t time.Time }

func (c *testClock) Now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// memStore is an in-memory Store keyed by the hex of the id hash.
type memStore struct {
	mu       sync.Mutex
	rows     map[string]Record
	user     Record // login / admin / disabled template
	touches  int
	inserted int
}

func newMemStore() *memStore {
	return &memStore{rows: map[string]Record{}, user: Record{Login: "priya"}}
}

func (m *memStore) Insert(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.Login, r.PlatformAdmin, r.Disabled = m.user.Login, m.user.PlatformAdmin, m.user.Disabled
	m.rows[string(r.IDHash)] = r
	m.inserted++
	return nil
}

func (m *memStore) Get(_ context.Context, h []byte) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[string(h)]
	if !ok {
		return Record{}, ErrNotFound
	}
	r.Disabled = m.user.Disabled
	return r, nil
}

func (m *memStore) Touch(_ context.Context, h []byte, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[string(h)]
	r.LastSeenAt = now
	m.rows[string(h)] = r
	m.touches++
	return nil
}

func (m *memStore) Revoke(_ context.Context, h []byte, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[string(h)]; ok && r.RevokedAt == nil {
		r.RevokedAt = &now
		m.rows[string(h)] = r
	}
	return nil
}

func (m *memStore) RevokeUser(_ context.Context, u uuid.UUID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.rows {
		if r.UserID == u && r.RevokedAt == nil {
			r.RevokedAt = &now
			m.rows[k] = r
		}
	}
	return nil
}

func (m *memStore) PurgeExpired(context.Context, time.Time, time.Duration) (int64, error) {
	return 0, nil
}

func newTestService(t *testing.T) (*Service, *memStore, *testClock) {
	t.Helper()
	store, clock := newMemStore(), &testClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	svc := NewService(store, Policy{AbsoluteTTL: 8 * time.Hour, IdleTTL: 30 * time.Minute, TouchInterval: time.Minute}, clock)
	return svc, store, clock
}

func TestIDShapeAndEntropy(t *testing.T) {
	a, err := NewID(strings.NewReader(strings.Repeat("a", 32)))
	if err != nil || !ValidID(a) {
		t.Fatalf("NewID = %q, %v", a, err)
	}
	if _, err := NewID(bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("short entropy must fail")
	}
	for _, bad := range []string{"", "x", a + "x", a[:len(a)-1], strings.Repeat("!", len(a)), strings.Repeat("a", 44)} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
	}
	svc, _, _ := newTestService(t)
	u := uuid.New()
	seen := map[string]bool{}
	for range 50 {
		id, err := svc.Issue(context.Background(), u)
		if err != nil || seen[id] {
			t.Fatalf("Issue = %q, %v (duplicate: %v)", id, err, seen[id])
		}
		seen[id] = true
	}
}

func TestStoresOnlyTheHash(t *testing.T) {
	svc, store, _ := newTestService(t)
	id, err := svc.Issue(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	for k := range store.rows {
		if k == id || strings.Contains(k, id) {
			t.Fatal("the raw session id must never reach the store")
		}
		if k != string(HashID(id)) || len(k) != 32 {
			t.Fatal("stored key must be the SHA-256 of the id")
		}
	}
}

func TestResolveRoundTrip(t *testing.T) {
	svc, store, _ := newTestService(t)
	store.user = Record{Login: "priya", PlatformAdmin: true}
	u := uuid.New()
	id, _ := svc.Issue(context.Background(), u)
	rec, err := svc.Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	p := rec.Principal()
	if p.UserID != u || p.Login != "priya" || !p.PlatformAdmin || p.Type != "user" {
		t.Fatalf("principal = %+v", p)
	}
}

func TestResolveRejectsUnknownAndMalformed(t *testing.T) {
	svc, _, _ := newTestService(t)
	forged, _ := NewID(strings.NewReader(strings.Repeat("z", 32)))
	for _, id := range []string{"", "nope", forged} {
		if _, err := svc.Resolve(context.Background(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestAbsoluteExpiry(t *testing.T) {
	svc, _, clock := newTestService(t)
	id, _ := svc.Issue(context.Background(), uuid.New())
	// Activity keeps the idle window open, but cannot outlive the absolute TTL.
	for range 20 {
		clock.advance(29 * time.Minute)
		if _, err := svc.Resolve(context.Background(), id); err != nil {
			if !errors.Is(err, ErrExpired) {
				t.Fatalf("err = %v", err)
			}
			return
		}
	}
	t.Fatal("session outlived its absolute lifetime")
}

func TestIdleExpiryAndSlidingWindow(t *testing.T) {
	svc, store, clock := newTestService(t)
	id, _ := svc.Issue(context.Background(), uuid.New())

	clock.advance(25 * time.Minute)
	if _, err := svc.Resolve(context.Background(), id); err != nil {
		t.Fatalf("within idle window: %v", err)
	}
	clock.advance(25 * time.Minute) // 50 minutes after login, 25 after the last use
	if _, err := svc.Resolve(context.Background(), id); err != nil {
		t.Fatalf("the idle window must slide: %v", err)
	}
	if store.touches != 2 {
		t.Fatalf("touches = %d, want 2", store.touches)
	}
	clock.advance(31 * time.Minute)
	if _, err := svc.Resolve(context.Background(), id); !errors.Is(err, ErrIdle) {
		t.Fatalf("after the idle timeout: %v", err)
	}
}

func TestTouchIsThrottled(t *testing.T) {
	svc, store, clock := newTestService(t)
	id, _ := svc.Issue(context.Background(), uuid.New())
	for range 5 {
		clock.advance(10 * time.Second)
		if _, err := svc.Resolve(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if store.touches != 0 {
		t.Fatalf("touches = %d, want 0 within the touch interval", store.touches)
	}
}

func TestRevocation(t *testing.T) {
	svc, _, _ := newTestService(t)
	u := uuid.New()
	a, _ := svc.Issue(context.Background(), u)
	b, _ := svc.Issue(context.Background(), u)
	other, _ := svc.Issue(context.Background(), uuid.New())

	if err := svc.Revoke(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(context.Background(), a); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked session: %v", err)
	}
	if err := svc.Revoke(context.Background(), a); err != nil {
		t.Fatalf("revoking twice must be harmless: %v", err)
	}
	if err := svc.Revoke(context.Background(), "garbage"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(context.Background(), b); err != nil {
		t.Fatalf("revoking one session must not touch another: %v", err)
	}
	if err := svc.RevokeUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(context.Background(), b); !errors.Is(err, ErrRevoked) {
		t.Fatalf("RevokeUser: %v", err)
	}
	if _, err := svc.Resolve(context.Background(), other); err != nil {
		t.Fatalf("another user's session: %v", err)
	}
}

func TestDisabledUserLosesSessions(t *testing.T) {
	svc, store, _ := newTestService(t)
	id, _ := svc.Issue(context.Background(), uuid.New())
	store.user.Disabled = true
	if _, err := svc.Resolve(context.Background(), id); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled user: %v", err)
	}
}

// Fixation: a login never adopts an id the client chose; every Issue is a new id.
func TestIssueNeverReusesAClientChosenID(t *testing.T) {
	svc, store, _ := newTestService(t)
	attacker, _ := NewID(strings.NewReader(strings.Repeat("k", 32)))
	id, err := svc.Issue(context.Background(), uuid.New())
	if err != nil || id == attacker {
		t.Fatalf("Issue = %q, %v", id, err)
	}
	if _, ok := store.rows[string(HashID(attacker))]; ok {
		t.Fatal("a pre-chosen id must not become a session")
	}
	if _, err := svc.Resolve(context.Background(), attacker); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pre-chosen id: %v", err)
	}
}

func TestPolicyCheckOrder(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	p := Policy{AbsoluteTTL: time.Hour, IdleTTL: 10 * time.Minute}
	rev := now.Add(-time.Second)
	tests := []struct {
		name string
		rec  Record
		want error
	}{
		{"valid", Record{LastSeenAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}, nil},
		{"expires exactly now", Record{LastSeenAt: now, ExpiresAt: now}, ErrExpired},
		{"idle exactly at the limit", Record{LastSeenAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(time.Hour)}, ErrIdle},
		{"revoked beats expiry", Record{RevokedAt: &rev, LastSeenAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)}, ErrRevoked},
		{"disabled beats everything", Record{Disabled: true, RevokedAt: &rev}, ErrDisabled},
	}
	for _, tt := range tests {
		if got := p.Check(tt.rec, now); !errors.Is(got, tt.want) && got != tt.want {
			t.Errorf("%s: Check = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCSRFToken(t *testing.T) {
	a, _ := NewID(strings.NewReader(strings.Repeat("a", 32)))
	b, _ := NewID(strings.NewReader(strings.Repeat("b", 32)))
	if CSRFToken(a) != CSRFToken(a) {
		t.Fatal("token must be stable for a session")
	}
	if CSRFToken(a) == CSRFToken(b) {
		t.Fatal("tokens must differ across sessions")
	}
	if CSRFToken(a) == a {
		t.Fatal("the token must not reveal the session id")
	}
	if !ValidCSRF(a, CSRFToken(a)) {
		t.Fatal("own token rejected")
	}
	for _, bad := range []string{"", CSRFToken(b), CSRFToken(a) + "x", CSRFToken(a)[1:], a} {
		if ValidCSRF(a, bad) {
			t.Errorf("ValidCSRF accepted %q", bad)
		}
	}
}

func TestSafeMethod(t *testing.T) {
	for m, want := range map[string]bool{"GET": true, "HEAD": true, "OPTIONS": true, "POST": false, "PUT": false,
		"PATCH": false, "DELETE": false, "get": false, "TRACE": false, "": false} {
		if SafeMethod(m) != want {
			t.Errorf("SafeMethod(%q) != %v", m, want)
		}
	}
}

func TestCookieFlags(t *testing.T) {
	c := Cookie("abc", true, 8*time.Hour)
	if c.Name != "iacp_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/" || c.Domain != "" || c.MaxAge != 8*3600 {
		t.Fatalf("cookie = %+v", c)
	}
	s := c.String()
	for _, want := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/", "Max-Age=28800"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q missing %q", s, want)
		}
	}
	if strings.Contains(Cookie("abc", false, time.Hour).String(), "Secure") {
		t.Fatal("loopback cookies must not be Secure")
	}
	clr := ClearCookie(true)
	if clr.MaxAge != -1 || clr.Value != "" || !clr.HttpOnly || !clr.Secure || clr.SameSite != http.SameSiteLaxMode {
		t.Fatalf("clear cookie = %+v", clr)
	}
}
