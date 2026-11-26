// Package session implements server-side browser sessions: an opaque random id held in an
// HttpOnly cookie, of which only the SHA-256 is stored, with an absolute lifetime and a
// sliding idle window.
//
// The pure parts (id and CSRF derivation, expiry rules, cookie attributes) are free
// functions; Service orchestrates the Store port, the clock and the entropy source.
package session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// CookieName is the session cookie.
const CookieName = "iacp_session"

// CSRFHeader carries the CSRF token on unsafe methods.
const CSRFHeader = "X-CSRF-Token"

// idBytes is the entropy of a session id (256 bits).
const idBytes = 32

// Errors returned by Resolve. Callers map all of them to a plain 401.
var (
	ErrNotFound = errors.New("session not found")
	ErrExpired  = errors.New("session expired")
	ErrIdle     = errors.New("session idle timeout")
	ErrRevoked  = errors.New("session revoked")
	ErrDisabled = errors.New("user disabled")
)

// Record is a stored session joined with its user.
type Record struct {
	IDHash     []byte
	UserID     uuid.UUID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time

	Login         string
	PlatformAdmin bool
	Disabled      bool
}

// Principal is the caller a valid session represents.
func (r Record) Principal() identity.Principal {
	return identity.Principal{UserID: r.UserID, Login: r.Login, Type: identity.ActorUser, PlatformAdmin: r.PlatformAdmin}
}

// Policy bounds a session's life.
type Policy struct {
	// AbsoluteTTL is the maximum age of a session, whatever its activity.
	AbsoluteTTL time.Duration
	// IdleTTL is how long a session survives without a request.
	IdleTTL time.Duration
	// TouchInterval limits how often last_seen_at is written (0 means every request).
	TouchInterval time.Duration
}

// Check reports why r is not valid at now, or nil. Pure.
func (p Policy) Check(r Record, now time.Time) error {
	switch {
	case r.Disabled:
		return ErrDisabled
	case r.RevokedAt != nil:
		return ErrRevoked
	case !now.Before(r.ExpiresAt):
		return ErrExpired
	case !now.Before(r.LastSeenAt.Add(p.IdleTTL)):
		return ErrIdle
	}
	return nil
}

// NeedsTouch reports whether last_seen_at should be advanced at now. Pure.
func (p Policy) NeedsTouch(r Record, now time.Time) bool {
	return now.Sub(r.LastSeenAt) >= p.TouchInterval
}

// Store persists sessions.
type Store interface {
	Insert(ctx context.Context, r Record) error
	// Get returns the session with its user, or ErrNotFound.
	Get(ctx context.Context, idHash []byte) (Record, error)
	Touch(ctx context.Context, idHash []byte, now time.Time) error
	// Revoke marks one session revoked; revoking an unknown or revoked session is not an error.
	Revoke(ctx context.Context, idHash []byte, now time.Time) error
	// RevokeUser revokes every live session of a user.
	RevokeUser(ctx context.Context, userID uuid.UUID, now time.Time) error
	// PurgeExpired deletes sessions that can no longer be valid (past absolute expiry, idle
	// for longer than idleTTL, or revoked) and returns how many.
	PurgeExpired(ctx context.Context, now time.Time, idleTTL time.Duration) (int64, error)
}

// Service issues, resolves and revokes sessions.
type Service struct {
	store  Store
	policy Policy
	clock  platform.Clock
	rand   io.Reader
}

// NewService returns a Service using crypto/rand.
func NewService(store Store, policy Policy, clock platform.Clock) *Service {
	return &Service{store: store, policy: policy, clock: clock, rand: rand.Reader}
}

// WithEntropy replaces the entropy source (tests only).
func (s *Service) WithEntropy(r io.Reader) *Service { s.rand = r; return s }

// Policy returns the lifetime policy (the HTTP layer sizes cookies from it).
func (s *Service) Policy() Policy { return s.policy }

// Issue creates a new session for userID and returns the raw id for the cookie. Every
// login mints a fresh id and never reuses one the client presented, which is what defeats
// session fixation.
func (s *Service) Issue(ctx context.Context, userID uuid.UUID) (string, error) {
	id, err := NewID(s.rand)
	if err != nil {
		return "", err
	}
	now := s.clock.Now()
	rec := Record{IDHash: HashID(id), UserID: userID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.policy.AbsoluteTTL)}
	if err := s.store.Insert(ctx, rec); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return id, nil
}

// Resolve validates the raw session id and slides the idle window. It returns the
// session's record on success.
func (s *Service) Resolve(ctx context.Context, id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, ErrNotFound
	}
	rec, err := s.store.Get(ctx, HashID(id))
	if err != nil {
		return Record{}, err
	}
	now := s.clock.Now()
	if err := s.policy.Check(rec, now); err != nil {
		return Record{}, err
	}
	if s.policy.NeedsTouch(rec, now) {
		if err := s.store.Touch(ctx, rec.IDHash, now); err != nil {
			return Record{}, fmt.Errorf("touch session: %w", err)
		}
		rec.LastSeenAt = now
	}
	return rec, nil
}

// Revoke ends the session with the raw id; an unknown id is not an error.
func (s *Service) Revoke(ctx context.Context, id string) error {
	if !ValidID(id) {
		return nil
	}
	return s.store.Revoke(ctx, HashID(id), s.clock.Now())
}

// RevokeUser ends every session of a user.
func (s *Service) RevokeUser(ctx context.Context, userID uuid.UUID) error {
	return s.store.RevokeUser(ctx, userID, s.clock.Now())
}

// Purge deletes dead sessions.
func (s *Service) Purge(ctx context.Context) (int64, error) {
	return s.store.PurgeExpired(ctx, s.clock.Now(), s.policy.IdleTTL)
}

// NewID returns a fresh session id: 32 random bytes, base64url without padding. Pure
// apart from reading r.
func NewID(r io.Reader) (string, error) {
	b := make([]byte, idBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidID reports whether id has the shape NewID produces. Anything else is rejected
// before it reaches the database. Pure.
func ValidID(id string) bool {
	if len(id) != base64.RawURLEncoding.EncodedLen(idBytes) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(b) == idBytes
}

// HashID is the stored form of a session id. Pure.
func HashID(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:]
}

// CSRFToken derives the per-session CSRF token from the session id: HMAC-SHA256 keyed with
// the id. It is stable for the life of the session, unguessable without the id, and needs
// no extra storage. Pure.
func CSRFToken(id string) string {
	m := hmac.New(sha256.New, []byte(id))
	m.Write([]byte("iacp-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// ValidCSRF compares a presented token with the session's, in constant time. Pure.
func ValidCSRF(id, presented string) bool {
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CSRFToken(id)), []byte(presented)) == 1
}

// SafeMethod reports whether m is a read-only method that needs no CSRF token. Pure.
func SafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// Cookie builds the session cookie: HttpOnly, SameSite=Lax, host-only, and Secure unless
// the deployment is plain-http loopback. Pure.
func Cookie(id string, secure bool, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: id, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge.Seconds())}
}

// ClearCookie expires the session cookie with the same attributes. Pure.
func ClearCookie(secure bool) *http.Cookie {
	c := Cookie("", secure, 0)
	c.MaxAge = -1
	return c
}
