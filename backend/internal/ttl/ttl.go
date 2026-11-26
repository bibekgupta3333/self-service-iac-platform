// Package ttl implements the lifetime of ephemeral stacks (docs/06 §6): the bounds a
// requested TTL must respect, expiry and warning arithmetic, extension, and the services
// that warn about, extend and tear down stacks whose time is up.
//
// This file is the pure core: every function takes its inputs and a time as arguments and
// performs no I/O, so the rules are tested without mocks. The services (service.go) and the
// PostgreSQL adapter (postgres.go) wrap it.
package ttl

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
)

// WarnBefore is how long before expiry the owners are warned (docs/06 §6).
const WarnBefore = 24 * time.Hour

// ProductionEnvironment is the one environment where a stack can never be ephemeral.
const ProductionEnvironment = "production"

// Stack statuses the TTL rules need to know; they mirror requests.StackStatus.
const (
	statusDestroying = "destroying"
	statusDestroyed  = "destroyed"
)

// MaxTTLCeilingHours (one year) bounds IACP_TTL_MAX_HOURS, far below where hour arithmetic
// overflows time.Duration or the int4 ttl_hours column.
const MaxTTLCeilingHours = 8760

// Policy bounds the TTL of an ephemeral stack. All values are hours.
type Policy struct {
	MinHours     int
	MaxHours     int
	DefaultHours int
	// MaxExtensionHours caps the total time extensions may add to one stack.
	MaxExtensionHours int
	// BackoffHours is how long after a failed, rejected or cancelled TTL destroy no new one is
	// opened for the stack; 0 disables the backoff.
	BackoffHours int
	// MaxAttempts is how many unsuccessful TTL destroys a stack may have in its current
	// lifetime cycle before the reaper stops and reports it as blocked; 0 means no limit.
	MaxAttempts int
}

// DefaultPolicy is used when nothing is configured: 1 h to 30 days, 72 h by default, and
// at most 7 days of extensions per stack.
func DefaultPolicy() Policy {
	return Policy{MinHours: 1, MaxHours: 720, DefaultHours: 72, MaxExtensionHours: 168, BackoffHours: 24, MaxAttempts: 3}
}

// Validate reports an inconsistent policy (a configuration error). Pure.
func (p Policy) Validate() error {
	switch {
	case p.MinHours < 1:
		return fmt.Errorf("minimum TTL must be at least 1 hour, got %d", p.MinHours)
	case p.MaxHours < p.MinHours:
		return fmt.Errorf("maximum TTL %d is below the minimum %d", p.MaxHours, p.MinHours)
	case p.DefaultHours < p.MinHours || p.DefaultHours > p.MaxHours:
		return fmt.Errorf("default TTL %d must be between %d and %d", p.DefaultHours, p.MinHours, p.MaxHours)
	case p.MaxHours > MaxTTLCeilingHours:
		return fmt.Errorf("maximum TTL %d exceeds the ceiling of %d hours", p.MaxHours, MaxTTLCeilingHours)
	case p.MaxExtensionHours < 0:
		return fmt.Errorf("maximum total extension must not be negative, got %d", p.MaxExtensionHours)
	case p.BackoffHours < 0 || p.MaxAttempts < 0:
		return fmt.Errorf("retry backoff (%d h) and maximum attempts (%d) must not be negative", p.BackoffHours, p.MaxAttempts)
	}
	return nil
}

// EnvRule is what an environment says about ephemeral stacks.
type EnvRule struct {
	Name           string
	AllowEphemeral bool
	MaxTTLHours    *int // nil: no environment ceiling beyond the policy
}

// AllowsEphemeral reports whether stacks in the environment may be ephemeral. Production
// never may, whatever the row says. Pure.
func (e EnvRule) AllowsEphemeral() bool {
	return e.AllowEphemeral && e.Name != ProductionEnvironment
}

// ceiling is the longest lifetime the policy and the environment allow together.
func (p Policy) ceiling(e EnvRule) int {
	if e.MaxTTLHours != nil && *e.MaxTTLHours < p.MaxHours {
		return *e.MaxTTLHours
	}
	return p.MaxHours
}

// Resolve checks a requested TTL against the policy and the environment and returns the
// hours to use: the default when none was asked for (0). Violations come back as field
// errors on /ephemeral. Pure.
func (p Policy) Resolve(requested int, env EnvRule) (int, []apperr.FieldError) {
	if !env.AllowsEphemeral() {
		return 0, []apperr.FieldError{{Pointer: "/ephemeral", Code: "ephemeral_not_allowed",
			Message: fmt.Sprintf("ephemeral stacks are not allowed in %s", env.Name)}}
	}
	hours := requested
	if hours == 0 {
		hours = p.DefaultHours
	}
	ceiling := p.ceiling(env)
	switch {
	case hours < p.MinHours:
		return 0, []apperr.FieldError{{Pointer: "/ephemeral/ttl_hours", Code: "minimum",
			Message: fmt.Sprintf("must be >= %d", p.MinHours)}}
	case hours > ceiling:
		return 0, []apperr.FieldError{{Pointer: "/ephemeral/ttl_hours", Code: "maximum",
			Message: fmt.Sprintf("must be <= %d in %s", ceiling, env.Name)}}
	}
	return hours, nil
}

// ExpiresAt is when a TTL of hours starting at from runs out. Pure.
func ExpiresAt(from time.Time, hours int) time.Time {
	return from.Add(time.Duration(hours) * time.Hour)
}

// Phase says what a stack is due for.
type Phase string

// Phases.
const (
	// PhaseNone: nothing to do yet (or ever: not ephemeral, already gone).
	PhaseNone Phase = "none"
	// PhaseWarn: inside the warning window and not warned yet.
	PhaseWarn Phase = "warn"
	// PhaseTeardown: expired; a destroy request is due.
	PhaseTeardown Phase = "teardown"
)

// Stack is a stack as the TTL rules see it.
type Stack struct {
	ID               uuid.UUID
	TeamID           uuid.UUID
	Team             string
	Environment      string
	Name             string
	Path             string
	Status           string
	IsEphemeral      bool
	ExpiresAt        *time.Time
	WarnedAt         *time.Time
	CreatedAt        time.Time
	TTLExtendedHours int
	// MaxTTLHours is the environment's lifetime ceiling, nil when it has none.
	MaxTTLHours *int
	// Attempts are the platform's earlier TTL destroys of this stack that ended unsuccessfully
	// (failed, rejected or cancelled), oldest first. Only the teardown backoff reads them.
	Attempts []Attempt
}

// Attempt is one TTL destroy request that ended without destroying the stack.
type Attempt struct {
	CreatedAt time.Time
	EndedAt   time.Time
	Outcome   string // failed, rejected or cancelled
}

// BackoffState is the verdict of Policy.Backoff.
type BackoffState string

// Backoff states.
const (
	// BackoffClear: a destroy may be opened now.
	BackoffClear BackoffState = "clear"
	// BackoffWait: the last attempt ended too recently.
	BackoffWait BackoffState = "wait"
	// BackoffBlocked: too many attempts; a human has to look at the stack.
	BackoffBlocked BackoffState = "blocked"
)

// Backoff decides whether another TTL destroy may be opened for s at now. Only attempts
// created at or after the current expiry count: extending the stack starts a new cycle. A
// stack with MaxAttempts unsuccessful attempts is blocked; otherwise it waits BackoffHours
// after the newest attempt ended. Pure.
func (p Policy) Backoff(s Stack, now time.Time) (BackoffState, time.Time) {
	if s.ExpiresAt == nil {
		return BackoffClear, time.Time{}
	}
	var cycle []Attempt
	for _, a := range s.Attempts {
		if !a.CreatedAt.Before(*s.ExpiresAt) {
			cycle = append(cycle, a)
		}
	}
	if len(cycle) == 0 {
		return BackoffClear, time.Time{}
	}
	if p.MaxAttempts > 0 && len(cycle) >= p.MaxAttempts {
		return BackoffBlocked, time.Time{}
	}
	last := cycle[0].EndedAt
	for _, a := range cycle {
		if a.EndedAt.After(last) {
			last = a.EndedAt
		}
	}
	until := last.Add(time.Duration(p.BackoffHours) * time.Hour)
	if p.BackoffHours > 0 && now.Before(until) {
		return BackoffWait, until
	}
	return BackoffClear, time.Time{}
}

// Eligible reports whether the stack may be handled by the TTL machinery at all: it must be
// an ephemeral stack outside production that still exists. It is the guard in front of
// every automatic action, warning and teardown alike. Pure.
func Eligible(s Stack) error {
	switch {
	case !s.IsEphemeral || s.ExpiresAt == nil:
		return apperr.Conflict("not_ephemeral", "the stack is not ephemeral")
	case s.Environment == ProductionEnvironment:
		return apperr.Conflict("production_not_ephemeral", "production stacks are never ephemeral")
	case s.Status == statusDestroying || s.Status == statusDestroyed:
		return apperr.Conflict("stack_destroyed", fmt.Sprintf("the stack is %s", s.Status))
	}
	return nil
}

// PhaseAt classifies the stack at now: teardown once expired, warn from WarnBefore ahead of
// expiry until a warning was recorded, else none. An ineligible stack is always none. Pure.
func PhaseAt(s Stack, now time.Time) Phase {
	if Eligible(s) != nil {
		return PhaseNone
	}
	exp := *s.ExpiresAt
	switch {
	case !now.Before(exp):
		return PhaseTeardown
	case !now.Before(exp.Add(-WarnBefore)) && s.WarnedAt == nil:
		return PhaseWarn
	}
	return PhaseNone
}

// WarnHorizon is the latest expiry a stack may have to be due for a warning or teardown at
// now: the coarse filter repositories apply before PhaseAt decides. Pure.
func WarnHorizon(now time.Time) time.Time { return now.Add(WarnBefore) }

// HoursLeft is the whole hours (rounded up) until the stack expires, 0 once expired. Pure.
func HoursLeft(expiresAt, now time.Time) int {
	d := expiresAt.Sub(now)
	if d <= 0 {
		return 0
	}
	return int((d + time.Hour - 1) / time.Hour)
}

// Extension is the result of extending a stack's TTL.
type Extension struct {
	ExpiresAt     time.Time
	ExtendedHours int // total extension after this one
	// ClearWarning is true when the new expiry lies beyond the warning window, so the stack
	// is warned again before it. An extension that stays inside the window keeps the
	// recorded warning: the owners were already told within the last 24 hours.
	ClearWarning bool
}

// Extend computes the new expiry when hours are added at now. The time is added to the
// current expiry, or to now when the stack already expired but was not torn down yet. The
// request is refused when hours are outside the policy bounds, when the stack's total
// extension would pass the cap, or when its lifetime would pass the environment ceiling.
// Pure.
func (p Policy) Extend(s Stack, hours int, now time.Time) (Extension, error) {
	if err := Eligible(s); err != nil {
		return Extension{}, err
	}
	if hours < p.MinHours || hours > p.MaxHours {
		return Extension{}, apperr.Invalid("validation_failed", "extension is outside the allowed range",
			apperr.FieldError{Pointer: "/hours", Code: "range",
				Message: fmt.Sprintf("must be between %d and %d", p.MinHours, p.MaxHours)})
	}
	if total := s.TTLExtendedHours + hours; total > p.MaxExtensionHours {
		return Extension{}, apperr.Conflict("ttl_extension_cap",
			fmt.Sprintf("the stack was already extended by %d h; at most %d h in total may be added (%d h left)",
				s.TTLExtendedHours, p.MaxExtensionHours, max(0, p.MaxExtensionHours-s.TTLExtendedHours)))
	}
	base := *s.ExpiresAt
	if base.Before(now) {
		base = now
	}
	next := base.Add(time.Duration(hours) * time.Hour)
	if s.MaxTTLHours != nil && next.Sub(s.CreatedAt) > time.Duration(*s.MaxTTLHours)*time.Hour {
		return Extension{}, apperr.Conflict("ttl_environment_max",
			fmt.Sprintf("the stack's lifetime would pass the %d h maximum of %s", *s.MaxTTLHours, s.Environment))
	}
	return Extension{ExpiresAt: next, ExtendedHours: s.TTLExtendedHours + hours,
		ClearWarning: next.Sub(now) > WarnBefore}, nil
}

// Due is a stack and what it is due for.
type Due struct {
	Stack Stack
	Phase Phase
}

// Classify keeps the stacks that are due for a warning or a teardown at now (teardowns
// of stacks in backoff left out), teardowns first and each group by expiry then id, so the order is deterministic. Pure.
func (p Policy) Classify(stacks []Stack, now time.Time) []Due {
	var warn, down []Due
	for _, s := range stacks {
		switch PhaseAt(s, now) {
		case PhaseWarn:
			warn = append(warn, Due{Stack: s, Phase: PhaseWarn})
		case PhaseTeardown:
			// A stack in backoff is left alone; a blocked one stays listed so that the
			// teardown call records it (ttl.blocked) and the reaper reports it.
			if state, _ := p.Backoff(s, now); state != BackoffWait {
				down = append(down, Due{Stack: s, Phase: PhaseTeardown})
			}
		}
	}
	sortDue(warn)
	sortDue(down)
	return append(down, warn...)
}

func sortDue(d []Due) {
	slices.SortFunc(d, func(a, b Due) int {
		if c := a.Stack.ExpiresAt.Compare(*b.Stack.ExpiresAt); c != 0 {
			return c
		}
		return bytes.Compare(a.Stack.ID[:], b.Stack.ID[:])
	})
}

// WarningComment is the text posted on the stack's pull request when it nears expiry. Pure.
func WarningComment(s Stack, now time.Time, stackURL string) string {
	exp := s.ExpiresAt.UTC().Format(time.RFC3339)
	var b strings.Builder
	fmt.Fprintf(&b, "**TTL warning:** the ephemeral stack `%s` expires at %s (in about %d h) and will then be destroyed.\n\n",
		s.Path, exp, HoursLeft(*s.ExpiresAt, now))
	if stackURL != "" {
		fmt.Fprintf(&b, "To keep it longer, extend its TTL: %s\n", stackURL)
	} else {
		b.WriteString("To keep it longer, extend its TTL from the portal or `POST /v1/stacks/{id}/ttl/extend`.\n")
	}
	return b.String()
}
