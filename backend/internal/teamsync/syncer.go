package teamsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// adminLockKey serialises platform-admin changes so two concurrent syncs cannot each see
// "the other admin" and remove both.
const adminLockKey = 727275

const syncActor = "github-team-sync"

// Syncer is the PostgreSQL adapter that upserts a user and syncs their memberships on login.
// Everything for one login, including its audit events, commits or rolls back together.
type Syncer struct {
	txr     *db.TxRunner
	audit   *audit.Writer
	clock   platform.Clock
	mapping Mapping
}

// NewSyncer returns a Syncer.
func NewSyncer(txr *db.TxRunner, w *audit.Writer, clock platform.Clock, m Mapping) *Syncer {
	return &Syncer{txr: txr, audit: w, clock: clock, mapping: m}
}

var _ oauth.UserSyncer = (*Syncer)(nil)

// SyncLogin implements oauth.UserSyncer.
func (s *Syncer) SyncLogin(ctx context.Context, gu identity.GitHubUser) (oauth.SyncResult, error) {
	if gu.ID <= 0 || gu.Login == "" {
		return oauth.SyncResult{}, errors.New("sync login: malformed GitHub user")
	}
	var res oauth.SyncResult
	err := s.txr.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = s.syncTx(ctx, tx, gu)
		return err
	})
	if err != nil {
		return oauth.SyncResult{}, err
	}
	return res, nil
}

func (s *Syncer) syncTx(ctx context.Context, tx pgx.Tx, gu identity.GitHubUser) (oauth.SyncResult, error) {
	now := s.clock.Now()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", adminLockKey); err != nil {
		return oauth.SyncResult{}, fmt.Errorf("admin lock: %w", err)
	}

	var userID uuid.UUID
	var hasAdmin, disabled bool
	err := tx.QueryRow(ctx, `
		INSERT INTO users (github_user_id, github_login, email, display_name, last_login_at)
		VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5)
		ON CONFLICT (github_user_id) DO UPDATE
		   SET github_login = EXCLUDED.github_login,
		       email = coalesce(EXCLUDED.email, users.email),
		       display_name = coalesce(EXCLUDED.display_name, users.display_name),
		       last_login_at = EXCLUDED.last_login_at
		RETURNING id, is_platform_admin, disabled_at IS NOT NULL`,
		gu.ID, gu.Login, gu.Email, gu.Name, now).Scan(&userID, &hasAdmin, &disabled)
	if err != nil {
		return oauth.SyncResult{}, fmt.Errorf("upsert user %q: %w", gu.Login, err)
	}
	if disabled {
		return oauth.SyncResult{}, oauth.ErrUserDisabled
	}

	teams, err := s.teams(ctx, tx)
	if err != nil {
		return oauth.SyncResult{}, err
	}
	current, err := s.memberships(ctx, tx, userID)
	if err != nil {
		return oauth.SyncResult{}, err
	}
	changes := Plan(current, DesiredRoles(teams, gu.TeamSlugs))
	bySlug := map[uuid.UUID]string{}
	for _, t := range teams {
		bySlug[t.ID] = t.Slug
	}
	for _, c := range changes {
		if err := s.apply(ctx, tx, userID, c, now); err != nil {
			return oauth.SyncResult{}, err
		}
		teamID := c.TeamID
		if err := s.event(ctx, tx, "rbac.membership_changed", userID, &teamID, map[string]any{
			"login": gu.Login, "team": bySlug[c.TeamID], "from": string(c.From), "to": string(c.To)}); err != nil {
			return oauth.SyncResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE memberships SET synced_at = $2 WHERE user_id = $1 AND source = $3`, userID, now, SourceSync); err != nil {
		return oauth.SyncResult{}, fmt.Errorf("stamp memberships: %w", err)
	}

	var others int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM users WHERE is_platform_admin AND disabled_at IS NULL AND NOT is_system AND id <> $1`,
		userID).Scan(&others); err != nil {
		return oauth.SyncResult{}, fmt.Errorf("count admins: %w", err)
	}
	d := ReconcileAdmin(hasAdmin, s.mapping.IsAdmin(gu.TeamSlugs), others)
	if d.Changed {
		if _, err := tx.Exec(ctx, `UPDATE users SET is_platform_admin = $2 WHERE id = $1`, userID, d.Admin); err != nil {
			return oauth.SyncResult{}, fmt.Errorf("set platform admin: %w", err)
		}
		if err := s.event(ctx, tx, "rbac.platform_admin_changed", userID, nil, map[string]any{
			"login": gu.Login, "from": hasAdmin, "to": d.Admin}); err != nil {
			return oauth.SyncResult{}, err
		}
	}
	if d.Blocked {
		if err := s.event(ctx, tx, "rbac.admin_downgrade_blocked", userID, nil, map[string]any{
			"login": gu.Login, "reason": "last_platform_admin"}); err != nil {
			return oauth.SyncResult{}, err
		}
	}
	return oauth.SyncResult{UserID: userID, Login: gu.Login, PlatformAdmin: d.Admin}, nil
}

func (s *Syncer) teams(ctx context.Context, tx pgx.Tx) ([]Team, error) {
	rows, err := tx.Query(ctx, `SELECT id, slug, github_team_slug FROM teams`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Team, error) {
		var t Team
		return t, r.Scan(&t.ID, &t.Slug, &t.GitHubSlug)
	})
	if err != nil {
		return nil, fmt.Errorf("read teams: %w", err)
	}
	return out, nil
}

func (s *Syncer) memberships(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]Membership, error) {
	rows, err := tx.Query(ctx, `SELECT team_id, role::text, source FROM memberships WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Membership, error) {
		var m Membership
		var role string
		err := r.Scan(&m.TeamID, &role, &m.Source)
		m.Role = rbac.Role(role)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("read memberships: %w", err)
	}
	return out, nil
}

func (s *Syncer) apply(ctx context.Context, tx pgx.Tx, userID uuid.UUID, c Change, now time.Time) error {
	if c.To == rbac.RoleNone {
		_, err := tx.Exec(ctx, `DELETE FROM memberships WHERE team_id = $1 AND user_id = $2 AND source = $3`,
			c.TeamID, userID, SourceSync)
		if err != nil {
			return fmt.Errorf("remove membership: %w", err)
		}
		return nil
	}
	// A manual row is never overwritten (the WHERE on the conflict branch).
	_, err := tx.Exec(ctx, `
		INSERT INTO memberships (team_id, user_id, role, source, synced_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role, synced_at = EXCLUDED.synced_at
		WHERE memberships.source = $4`, c.TeamID, userID, string(c.To), SourceSync, now)
	if err != nil {
		return fmt.Errorf("upsert membership: %w", err)
	}
	return nil
}

func (s *Syncer) event(ctx context.Context, tx pgx.Tx, action string, userID uuid.UUID, teamID *uuid.UUID, p map[string]any) error {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}
	_, err = s.audit.Append(ctx, tx, audit.Event{ActorType: "system", ActorID: syncActor, Action: action,
		SubjectType: "user", SubjectID: userID.String(), TeamID: teamID, Payload: b})
	return err
}
