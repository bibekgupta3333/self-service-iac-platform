package budgets

// Budget administration (WBS 8.5): the admin API behind PUT/GET
// /v1/teams/{slug}/budgets/{month} and the internal actual-spend route. This file holds
// the pure parts (validation, the audit events) and the service that adds the
// team-scoped role check; the SQL lives in admin_postgres.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// Limits on what an admin may set.
const (
	// Currency is the only supported currency: every amount column is *_usd.
	Currency = "USD"
	// MaxLimitUSD bounds a monthly limit (also a CHECK constraint, migration 0014).
	MaxLimitUSD = 1_000_000
	// MinOverrideReason is the shortest reason an override may give.
	MinOverrideReason = 20
	// MaxOverrideReason bounds the reason text.
	MaxOverrideReason = 500
	// MaxOverrideTTL is the longest an override may last.
	MaxOverrideTTL = 31 * 24 * time.Hour
	// MaxThresholds bounds the alert threshold list.
	MaxThresholds = 10
)

// DefaultThresholds are the alert thresholds (percent of the limit) used when none are given.
var DefaultThresholds = []int{50, 80, 100}

// SetInput is the PUT /v1/teams/{slug}/budgets/{month} body.
type SetInput struct {
	// Environment scopes the budget to one environment; empty or null covers all of them.
	Environment *string  `json:"environment"`
	LimitUSD    *float64 `json:"limit_usd"`
	// Currency must be empty or "USD".
	Currency string `json:"currency"`
	// HardBlock defaults to true: exceeding the limit denies the change.
	HardBlock       *bool `json:"hard_block"`
	AlertThresholds []int `json:"alert_thresholds"`
	// SingleChangeCapUSD denies one stack whose monthly cost exceeds it, whatever the headroom
	// (budget.rego); 0 to 1,000,000. Omitted or null means no cap: the policy compares it with the
	// stack's whole estimated cost, not the change, so a default would block updates of big stacks.
	SingleChangeCapUSD *float64 `json:"single_change_cap_usd"`
	// Override grants the admin override (see Override); omitting it clears any granted one.
	// It is refused on a production-scoped budget, where it would never apply.
	Override *OverrideInput `json:"override"`
}

// OverrideInput is an admin override in a SetInput.
type OverrideInput struct {
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
	// MaxDeltaUSD bounds the override: it waives the hard limit only for a stack whose monthly
	// cost is at most this (required, above 0 and at most MaxLimitUSD).
	MaxDeltaUSD *float64 `json:"max_delta_usd"`
}

// Write is a validated SetInput: what the repository stores.
type Write struct {
	Team        string
	Month       time.Time // first of the month, UTC
	Environment string    // "" = every environment of the team
	LimitCents  int64
	HardBlock   bool
	Thresholds  []int
	CapCents    *int64 // single-change cap; nil = none
	Override    *OverrideWrite
}

// OverrideWrite is a validated override.
type OverrideWrite struct {
	Reason        string
	ExpiresAt     time.Time
	MaxDeltaCents int64
}

// Setting is a stored budget as the admin API shows it.
type Setting struct {
	ID              uuid.UUID
	Team            string
	Environment     string // "" = every environment
	Month           time.Time
	LimitUSD        float64
	SpentUSD        float64
	ActualUSD       *float64
	HardBlock       bool
	AlertThresholds []int
	// SingleChangeCapUSD is the per-stack monthly ceiling budget.rego applies; nil = none.
	SingleChangeCapUSD *float64
	Override           *Override
	UpdatedBy          string // login, "" when never set through the API
	UpdatedAt          time.Time
}

// UsedUSD is what counts as already spent: the larger of the committed estimate and the
// recorded actual (the same rule budget.rego applies). Pure.
func (s Setting) UsedUSD() float64 {
	if s.ActualUSD != nil && *s.ActualUSD > s.SpentUSD {
		return *s.ActualUSD
	}
	return s.SpentUSD
}

// Utilisation is UsedUSD as a percent of the limit; 0 for a zero limit. Pure.
func (s Setting) Utilisation() float64 {
	if s.LimitUSD <= 0 {
		return 0
	}
	return math.Round(s.UsedUSD()/s.LimitUSD*10000) / 100
}

// Change is the outcome of Set.
type Change struct {
	Before *Setting // nil when the budget was created
	After  Setting
}

// ParseMonth parses a YYYY-MM path segment into the first instant of that month in UTC. Pure.
func ParseMonth(s string) (time.Time, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil || len(s) != 7 {
		return time.Time{}, apperr.Invalid("invalid_month", fmt.Sprintf("month %q is not YYYY-MM", s))
	}
	return t.UTC(), nil
}

// monthBounds is how far from now a budget month may be: a year back, two years ahead.
func checkMonth(month, now time.Time) error {
	cur := MonthOf(now)
	if month.Before(cur.AddDate(-1, 0, 0)) || month.After(cur.AddDate(2, 0, 0)) {
		return apperr.Invalid("invalid_month", fmt.Sprintf("month %s is outside the range %s to %s",
			month.Format("2006-01"), cur.AddDate(-1, 0, 0).Format("2006-01"), cur.AddDate(2, 0, 0).Format("2006-01")))
	}
	return nil
}

// Cents converts a USD amount to whole cents. It refuses NaN, infinities, negative
// amounts, amounts above max and fractions of a cent. Pure.
func Cents(usd float64, max float64) (int64, bool) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd > max {
		return 0, false
	}
	c := math.Round(usd * 100)
	if math.Abs(c/100-usd) > 1e-9 {
		return 0, false
	}
	return int64(c), true
}

// FormatCents renders cents as a decimal string ("2000.00"), for audit payloads. Pure.
func FormatCents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func fieldErr(pointer, code, msg string) apperr.FieldError {
	return apperr.FieldError{Pointer: pointer, Code: code, Message: msg}
}

// NewWrite validates a SetInput for team and month (YYYY-MM) at time now. Every problem is
// reported together as field errors. Pure.
func NewWrite(team, month string, in SetInput, now time.Time) (Write, error) {
	if !teamRe.MatchString(team) {
		return Write{}, apperr.NotFound("team %q not found", team)
	}
	m, err := ParseMonth(month)
	if err != nil {
		return Write{}, err
	}
	if err := checkMonth(m, now); err != nil {
		return Write{}, err
	}
	w := Write{Team: team, Month: m, HardBlock: true, Thresholds: slices.Clone(DefaultThresholds)}
	var bad []apperr.FieldError

	switch {
	case in.LimitUSD == nil:
		bad = append(bad, fieldErr("/limit_usd", "required", "limit_usd is required"))
	default:
		if c, ok := Cents(*in.LimitUSD, MaxLimitUSD); ok {
			w.LimitCents = c
		} else {
			bad = append(bad, fieldErr("/limit_usd", "out_of_range", fmt.Sprintf(
				"limit_usd must be between 0 and %d with at most two decimals", MaxLimitUSD)))
		}
	}
	if in.Currency != "" && in.Currency != Currency {
		bad = append(bad, fieldErr("/currency", "unsupported_currency", fmt.Sprintf("only %s is supported", Currency)))
	}
	if in.Environment != nil && *in.Environment != "" {
		if !slices.Contains(environments, *in.Environment) {
			bad = append(bad, fieldErr("/environment", "unknown_environment",
				fmt.Sprintf("environment must be one of %s, or omitted for every environment", strings.Join(environments, ", "))))
		} else {
			w.Environment = *in.Environment
		}
	}
	if in.HardBlock != nil {
		w.HardBlock = *in.HardBlock
	}
	if in.AlertThresholds != nil {
		t, err := normaliseThresholds(in.AlertThresholds)
		if err != nil {
			bad = append(bad, fieldErr("/alert_thresholds", "invalid_thresholds", err.Error()))
		} else {
			w.Thresholds = t
		}
	}
	if in.SingleChangeCapUSD != nil {
		if c, ok := Cents(*in.SingleChangeCapUSD, MaxLimitUSD); ok {
			w.CapCents = &c
		} else {
			bad = append(bad, fieldErr("/single_change_cap_usd", "out_of_range", fmt.Sprintf(
				"single_change_cap_usd must be between 0 and %d with at most two decimals", MaxLimitUSD)))
		}
	}
	if in.Override != nil {
		if w.Environment == "production" {
			bad = append(bad, fieldErr("/override", "override_not_allowed",
				"an override never applies in production: raise limit_usd instead (audited), or omit the override"))
		}
		o, fe := newOverride(*in.Override, now)
		bad = append(bad, fe...)
		w.Override = o
	}
	if len(bad) > 0 {
		return Write{}, apperr.Invalid("validation_failed", "the budget is invalid", bad...)
	}
	return w, nil
}

func normaliseThresholds(in []int) ([]int, error) {
	if len(in) == 0 || len(in) > MaxThresholds {
		return nil, fmt.Errorf("give between 1 and %d thresholds", MaxThresholds)
	}
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	if out[0] < 1 || out[len(out)-1] > 200 {
		return nil, fmt.Errorf("each threshold is a percent of the limit between 1 and 200")
	}
	return out, nil
}

func newOverride(in OverrideInput, now time.Time) (*OverrideWrite, []apperr.FieldError) {
	var bad []apperr.FieldError
	reason := strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(reason); n < MinOverrideReason || n > MaxOverrideReason {
		bad = append(bad, fieldErr("/override/reason", "invalid_reason",
			fmt.Sprintf("the reason must be %d to %d characters", MinOverrideReason, MaxOverrideReason)))
	}
	exp := in.ExpiresAt.UTC()
	if in.ExpiresAt.IsZero() || !exp.After(now) || exp.After(now.Add(MaxOverrideTTL)) {
		bad = append(bad, fieldErr("/override/expires_at", "invalid_expiry",
			"expires_at must be in the future and no more than 31 days away"))
	}
	var delta int64
	if in.MaxDeltaUSD == nil {
		bad = append(bad, fieldErr("/override/max_delta_usd", "required", "max_delta_usd bounds the override and is required"))
	} else if c, ok := Cents(*in.MaxDeltaUSD, MaxLimitUSD); !ok || c == 0 {
		bad = append(bad, fieldErr("/override/max_delta_usd", "out_of_range", fmt.Sprintf(
			"max_delta_usd must be above 0 and at most %d with at most two decimals", MaxLimitUSD)))
	} else {
		delta = c
	}
	if len(bad) > 0 {
		return nil, bad
	}
	return &OverrideWrite{Reason: reason, ExpiresAt: exp, MaxDeltaCents: delta}, nil
}

// ActualInput is the POST /v1/internal/budget-actuals body.
type ActualInput struct {
	Team        string   `json:"team"`
	Environment string   `json:"environment"`
	Month       string   `json:"month"` // YYYY-MM
	ActualUSD   *float64 `json:"actual_usd"`
}

// ActualWrite is a validated ActualInput.
type ActualWrite struct {
	Team        string
	Environment string
	Month       time.Time
	ActualCents int64
}

// NewActualWrite validates an ActualInput. Pure.
func NewActualWrite(in ActualInput, now time.Time) (ActualWrite, error) {
	var bad []apperr.FieldError
	if !teamRe.MatchString(in.Team) {
		bad = append(bad, fieldErr("/team", "invalid_team", "team is not a valid slug"))
	}
	if in.Environment != "" && !slices.Contains(environments, in.Environment) {
		bad = append(bad, fieldErr("/environment", "unknown_environment", "unknown environment"))
	}
	m, err := ParseMonth(in.Month)
	if err == nil {
		err = checkMonth(m, now)
	}
	if err != nil {
		bad = append(bad, fieldErr("/month", "invalid_month", "month must be YYYY-MM, within a year back and two years ahead"))
	}
	var cents int64
	switch {
	case in.ActualUSD == nil:
		bad = append(bad, fieldErr("/actual_usd", "required", "actual_usd is required"))
	default:
		c, ok := Cents(*in.ActualUSD, MaxLimitUSD*10)
		if !ok {
			bad = append(bad, fieldErr("/actual_usd", "out_of_range", "actual_usd must be a non-negative amount with at most two decimals"))
		}
		cents = c
	}
	if len(bad) > 0 {
		return ActualWrite{}, apperr.Invalid("validation_failed", "the actual spend is invalid", bad...)
	}
	return ActualWrite{Team: in.Team, Environment: in.Environment, Month: m, ActualCents: cents}, nil
}

// CommittedInput is the POST /v1/internal/budget-committed body: the committed monthly cost of one
// stack, which the team-wide total in the budget context is summed from.
type CommittedInput struct {
	StackPath  string   `json:"stack_path"`
	MonthlyUSD *float64 `json:"monthly_usd"`
}

// CommittedWrite is a validated CommittedInput.
type CommittedWrite struct {
	Stack        StackRef
	MonthlyCents int64
}

// MaxCommittedUSD bounds one stack's committed monthly cost.
const MaxCommittedUSD = 10 * MaxLimitUSD

// NewCommittedWrite validates a CommittedInput. Pure.
func NewCommittedWrite(in CommittedInput) (CommittedWrite, error) {
	var bad []apperr.FieldError
	ref, err := ParseStackPath(in.StackPath)
	if err != nil {
		bad = append(bad, fieldErr("/stack_path", "invalid_stack_path", "stack_path must be environments/<environment>/<team>/<stack>"))
	}
	var cents int64
	if in.MonthlyUSD == nil {
		bad = append(bad, fieldErr("/monthly_usd", "required", "monthly_usd is required"))
	} else if c, ok := Cents(*in.MonthlyUSD, MaxCommittedUSD); !ok {
		bad = append(bad, fieldErr("/monthly_usd", "out_of_range", "monthly_usd must be a non-negative amount with at most two decimals"))
	} else {
		cents = c
	}
	if len(bad) > 0 {
		return CommittedWrite{}, apperr.Invalid("validation_failed", "the committed cost is invalid", bad...)
	}
	return CommittedWrite{Stack: ref, MonthlyCents: cents}, nil
}

// Audit actions.
const (
	ActionSet             = "budget.set"
	ActionOverrideGranted = "budget.override.granted"
	ActionOverrideRevoked = "budget.override.revoked"
	ActionActualRecorded  = "budget.actual.recorded"
	ActionCommittedSet    = "stack.cost.committed"
)

func scopeName(env string) string {
	if env == "" {
		return "all"
	}
	return env
}

// AuditEvents builds the audit events for a Set: always budget.set, plus one for a granted
// or revoked override. Amounts are decimal strings. The repository adds TeamID. Pure.
func AuditEvents(w Write, ch Change, by identity.Principal, correlationID string, at time.Time) []audit.Event {
	base := audit.Event{OccurredAt: at, ActorType: string(by.Type), ActorID: by.AuditActorID(),
		SubjectType: "budget", SubjectID: ch.After.ID.String(), CorrelationID: correlationID}
	mk := func(action string, payload map[string]any) audit.Event {
		e := base
		e.Action = action
		e.Payload, _ = json.Marshal(payload)
		return e
	}
	set := map[string]any{"team": w.Team, "month": w.Month.Format("2006-01"), "environment": scopeName(w.Environment),
		"limit_usd": FormatCents(w.LimitCents), "hard_block": w.HardBlock, "alert_thresholds": w.Thresholds,
		"single_change_cap_usd": nil, "created": ch.Before == nil}
	if w.CapCents != nil {
		set["single_change_cap_usd"] = FormatCents(*w.CapCents)
	}
	if ch.Before != nil {
		set["previous_limit_usd"] = FormatCents(int64(math.Round(ch.Before.LimitUSD * 100)))
		set["previous_hard_block"] = ch.Before.HardBlock
	}
	events := []audit.Event{mk(ActionSet, set)}
	switch {
	case w.Override != nil && !sameOverride(ch.Before, w.Override):
		events = append(events, mk(ActionOverrideGranted, map[string]any{"team": w.Team,
			"month": w.Month.Format("2006-01"), "environment": scopeName(w.Environment),
			"reason": w.Override.Reason, "expires_at": w.Override.ExpiresAt.Format(time.RFC3339),
			"max_delta_usd": FormatCents(w.Override.MaxDeltaCents)}))
	case w.Override != nil:
		// The same override sent again: nothing was granted, so nothing is audited as granted.
	case ch.Before != nil && ch.Before.Override != nil:
		events = append(events, mk(ActionOverrideRevoked, map[string]any{"team": w.Team,
			"month": w.Month.Format("2006-01"), "environment": scopeName(w.Environment)}))
	}
	return events
}

// sameOverride reports whether the stored budget already carries exactly override o (the repository
// then keeps the original grantor). Pure.
func sameOverride(before *Setting, o *OverrideWrite) bool {
	if before == nil || before.Override == nil || o == nil {
		return false
	}
	b := before.Override
	return b.Reason == o.Reason && b.ExpiresAt.Equal(o.ExpiresAt) && int64(math.Round(b.MaxDeltaUSD*100)) == o.MaxDeltaCents
}

// ActualAuditEvent is the audit event for a recorded actual. The repository adds TeamID. Pure.
func ActualAuditEvent(a ActualWrite, s Setting, by identity.Principal, correlationID string, at time.Time) audit.Event {
	payload, _ := json.Marshal(map[string]any{"team": a.Team, "month": a.Month.Format("2006-01"),
		"environment": scopeName(a.Environment), "actual_usd": FormatCents(a.ActualCents)})
	return audit.Event{OccurredAt: at, ActorType: string(by.Type), ActorID: by.AuditActorID(), Action: ActionActualRecorded,
		SubjectType: "budget", SubjectID: s.ID.String(), CorrelationID: correlationID, Payload: payload}
}

// CommittedAuditEvent is the audit event for a recorded committed cost. The repository adds TeamID. Pure.
func CommittedAuditEvent(c CommittedWrite, stackID uuid.UUID, by identity.Principal, correlationID string, at time.Time) audit.Event {
	payload, _ := json.Marshal(map[string]any{"stack_path": c.Stack.Path(), "monthly_usd": FormatCents(c.MonthlyCents)})
	return audit.Event{OccurredAt: at, ActorType: string(by.Type), ActorID: by.AuditActorID(), Action: ActionCommittedSet,
		SubjectType: "stack", SubjectID: stackID.String(), CorrelationID: correlationID, Payload: payload}
}

// AdminRepository is the persistence port of the admin API. Set and RecordActual change the
// budget and append their audit events in one transaction.
type AdminRepository interface {
	// RoleInTeam is the caller's role in team (apperr NotFound for an unknown team).
	RoleInTeam(ctx context.Context, team string, userID uuid.UUID) (rbac.Role, error)
	// Set creates or replaces the budget w describes.
	Set(ctx context.Context, w Write, by identity.Principal, correlationID string) (Change, error)
	// List returns the team's budgets, newest month first; month non-nil limits it to one month.
	List(ctx context.Context, team string, month *time.Time) ([]Setting, error)
	// RecordActual sets actual_usd of an existing budget (apperr NotFound when there is none).
	RecordActual(ctx context.Context, a ActualWrite, by identity.Principal, correlationID string) (Setting, error)
	// RecordCommitted sets the committed monthly cost of an existing, not destroyed stack (apperr
	// NotFound when there is none) and returns its id.
	RecordCommitted(ctx context.Context, c CommittedWrite, by identity.Principal, correlationID string) (uuid.UUID, error)
}

// AdminService is the budget administration use case.
type AdminService struct {
	repo  AdminRepository
	clock platform.Clock
}

// NewAdminService returns an AdminService.
func NewAdminService(repo AdminRepository, clock platform.Clock) *AdminService {
	return &AdminService{repo: repo, clock: clock}
}

// authorize requires the caller to hold a role in team that the RBAC matrix allows to set
// budgets. Budgets are team-wide, so the strictest environment applies.
func (s *AdminService) authorize(ctx context.Context, by identity.Principal, team string) error {
	role, err := s.repo.RoleInTeam(ctx, team, by.UserID)
	if err != nil {
		return err
	}
	if d := rbac.Decide(role, rbac.ActionBudgetSet, rbac.Resource{Environment: rbac.EnvProduction}); !d.Allowed {
		return apperr.Forbidden(fmt.Sprintf("%s requires the %s role in team %s", rbac.ActionBudgetSet, d.Need, team))
	}
	return nil
}

// Set validates and stores a team's budget for month (YYYY-MM).
func (s *AdminService) Set(ctx context.Context, by identity.Principal, team, month string, in SetInput, correlationID string) (Setting, error) {
	if err := s.authorize(ctx, by, team); err != nil {
		return Setting{}, err
	}
	w, err := NewWrite(team, month, in, s.clock.Now())
	if err != nil {
		return Setting{}, err
	}
	ch, err := s.repo.Set(ctx, w, by, correlationID)
	if err != nil {
		return Setting{}, err
	}
	return ch.After, nil
}

// List returns the team's budgets; month "" lists every month.
func (s *AdminService) List(ctx context.Context, by identity.Principal, team, month string) ([]Setting, error) {
	if err := s.authorize(ctx, by, team); err != nil {
		return nil, err
	}
	var m *time.Time
	if month != "" {
		t, err := ParseMonth(month)
		if err != nil {
			return nil, err
		}
		m = &t
	}
	return s.repo.List(ctx, team, m)
}

// RecordCommitted stores one stack's committed monthly cost. Like RecordActual its caller is a machine
// holding the shared token, so no team role applies.
func (s *AdminService) RecordCommitted(ctx context.Context, by identity.Principal, in CommittedInput, correlationID string) (CommittedWrite, error) {
	c, err := NewCommittedWrite(in)
	if err != nil {
		return CommittedWrite{}, err
	}
	if _, err := s.repo.RecordCommitted(ctx, c, by, correlationID); err != nil {
		return CommittedWrite{}, err
	}
	return c, nil
}

// RecordActual stores the actual spend of an existing budget. The caller is a machine
// (the route class admits no OIDC workflow), so no team role applies.
func (s *AdminService) RecordActual(ctx context.Context, by identity.Principal, in ActualInput, correlationID string) (Setting, error) {
	a, err := NewActualWrite(in, s.clock.Now())
	if err != nil {
		return Setting{}, err
	}
	return s.repo.RecordActual(ctx, a, by, correlationID)
}
