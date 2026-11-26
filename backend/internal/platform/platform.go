// Package platform defines small ports for non-deterministic inputs (time, identifiers)
// so domain services stay testable with fixed values.
package platform

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock. Times are UTC and truncated to microseconds
// so they survive a round trip through PostgreSQL timestamptz unchanged.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// FixedClock always returns T. Useful in tests.
type FixedClock struct{ T time.Time }

// Now implements Clock.
func (c FixedClock) Now() time.Time { return c.T }

// IDGenerator creates identifiers.
type IDGenerator interface {
	// NewID returns a random UUID.
	NewID() uuid.UUID
	// NewShortID returns a short, human-friendly id with the given prefix, e.g. "req-7k3p9x2m4q".
	NewShortID(prefix string) string
}

// RandomIDs is the production IDGenerator backed by crypto/rand.
type RandomIDs struct{}

const shortAlphabet = "0123456789abcdefghjkmnpqrstvwxyz" // Crockford base32, lower case

// ShortIDLen is the number of random characters in a short id: 32^10 ≈ 1.1e15 values,
// so a collision on the UNIQUE short_id is about a 50% chance only after ~39 million ids.
const ShortIDLen = 10

// NewID implements IDGenerator.
func (RandomIDs) NewID() uuid.UUID { return uuid.New() }

// NewShortID implements IDGenerator. The alphabet has 32 symbols, so byte%32 is unbiased.
func (RandomIDs) NewShortID(prefix string) string {
	b := make([]byte, ShortIDLen)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err)) // never happens on supported platforms
	}
	for i := range b {
		b[i] = shortAlphabet[int(b[i])%len(shortAlphabet)]
	}
	return prefix + "-" + string(b)
}
