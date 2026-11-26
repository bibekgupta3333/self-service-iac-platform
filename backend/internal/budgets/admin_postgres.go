package budgets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
)

// PostgresAdminRepository implements AdminRepository with pgx. Writes and their audit
// events share one transaction.
type PostgresAdminRepository struct {
	pool  *pgxpool.Pool
	tx    *db.TxRunner
	audit *audit.Writer
	clock platform.Clock
}

var _ AdminRepository = (*PostgresAdminRepository)(nil)

// NewPostgresAdminRepository returns a PostgresAdminRepository.
func NewPostgresAdminRepository(pool *pgxpool.Pool, auditWriter *audit.Writer, clock platform.Clock) *PostgresAdminRepository {
	return &PostgresAdminRepository{pool: pool, tx: db.NewTxRunner(pool), audit: auditWriter, clock: clock}
}

// RoleInTeam implements AdminRepository. Platform admins count as team admins; disabled or
// unknown users have no role.
func (r *PostgresAdminRepository) RoleInTeam(ctx context.Context, team string, userID uuid.UUID) (rbac.Role, error) {
	var role string
	err := r.pool.QueryRow(ctx, `
		SELECT coalesce(
		         (SELECT CASE WHEN u.is_platform_admin THEN 'admin' ELSE coalesce(m.role::text, '') END
		          FROM users u
		          LEFT JOIN memberships m ON m.user_id = u.id AND m.team_id = t.id
		          WHERE u.id = $2 AND u.disabled_at IS NULL), '')
		FROM teams t WHERE t.slug = $1`, team, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return rbac.RoleNone, apperr.NotFound("team %q not found", team)
	}
	if err != nil {
		return rbac.RoleNone, fmt.Errorf("query membership: %w", err)
	}
	return rbac.Role(role), nil
}

const settingSelect = `
	SELECT b.id, t.slug, coalesce(e.name::text, ''), b.month, b.limit_usd::float8, b.spent_usd::float8, b.actual_usd::float8,
	       b.hard_block, b.alert_thresholds, o.github_login::text, b.override_reason, b.override_expires_at,
	       u.github_login::text, b.updated_at
	FROM budgets b
	JOIN teams t ON t.id = b.team_id
	LEFT JOIN environments e ON e.id = b.environment_id
	LEFT JOIN users o ON o.id = b.override_by
	LEFT JOIN users u ON u.id = b.updated_by`

func scanSetting(row pgx.Row) (Setting, error) {
	var (
		s      Setting
		thr    []int32
		ovBy   *string
		ovWhy  *string
		ovExp  *time.Time
		byName *string
	)
	err := row.Scan(&s.ID, &s.Team, &s.Environment, &s.Month, &s.LimitUSD, &s.SpentUSD, &s.ActualUSD, &s.HardBlock,
		&thr, &ovBy, &ovWhy, &ovExp, &byName, &s.UpdatedAt)
	if err != nil {
		return Setting{}, err
	}
	s.Month = s.Month.UTC()
	for _, t := range thr {
		s.AlertThresholds = append(s.AlertThresholds, int(t))
	}
	if ovBy != nil && ovWhy != nil && ovExp != nil {
		s.Override = &Override{GrantedBy: *ovBy, Reason: *ovWhy, ExpiresAt: ovExp.UTC()}
	}
	if byName != nil {
		s.UpdatedBy = *byName
	}
	return s, nil
}

// target resolves the team and optional environment of a budget inside tx and locks the
// team row (FOR NO KEY UPDATE does not block foreign keys) so concurrent writers of the
// same budget serialise instead of racing on the unique constraint.
func target(ctx context.Context, tx pgx.Tx, team, env string) (teamID uuid.UUID, envID *uuid.UUID, err error) {
	err = tx.QueryRow(ctx, `SELECT id FROM teams WHERE slug = $1 FOR NO KEY UPDATE`, team).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, apperr.NotFound("team %q not found", team)
	}
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("lock team %s: %w", team, err)
	}
	if env == "" {
		return teamID, nil, nil
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM environments WHERE name::text = $1`, env).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, apperr.Invalid("unknown_environment", fmt.Sprintf("environment %q is not configured", env))
	}
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("load environment %s: %w", env, err)
	}
	return teamID, &id, nil
}

func (r *PostgresAdminRepository) appendAll(ctx context.Context, tx pgx.Tx, teamID uuid.UUID, events []audit.Event) error {
	for _, e := range events {
		e.TeamID = &teamID
		if _, err := r.audit.Append(ctx, tx, e); err != nil {
			return fmt.Errorf("audit %s: %w", e.Action, err)
		}
	}
	return nil
}

// Set implements AdminRepository.
func (r *PostgresAdminRepository) Set(ctx context.Context, w Write, by identity.Principal, correlationID string) (Change, error) {
	var ch Change
	err := r.tx.InTx(ctx, func(tx pgx.Tx) error {
		teamID, envID, err := target(ctx, tx, w.Team, w.Environment)
		if err != nil {
			return err
		}
		var id uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM budgets WHERE team_id = $1 AND environment_id IS NOT DISTINCT FROM $2
			AND month = $3::date`, teamID, envID, w.Month).Scan(&id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			id = uuid.Nil
		case err != nil:
			return fmt.Errorf("load budget: %w", err)
		default:
			before, err := scanSetting(tx.QueryRow(ctx, settingSelect+` WHERE b.id = $1`, id))
			if err != nil {
				return fmt.Errorf("load budget: %w", err)
			}
			ch.Before = &before
		}
		var ovBy *uuid.UUID
		var ovWhy *string
		var ovExp *time.Time
		if w.Override != nil {
			ovBy, ovWhy, ovExp = &by.UserID, &w.Override.Reason, &w.Override.ExpiresAt
		}
		now := r.clock.Now()
		if id == uuid.Nil {
			err = tx.QueryRow(ctx, `
				INSERT INTO budgets (team_id, environment_id, month, limit_usd, alert_thresholds, hard_block,
				                     override_by, override_reason, override_expires_at, created_by, updated_by, updated_at)
				VALUES ($1, $2, $3::date, $4::numeric / 100, $5, $6, $7, $8, $9, $10, $10, $11)
				RETURNING id`, teamID, envID, w.Month, w.LimitCents, w.Thresholds, w.HardBlock,
				ovBy, ovWhy, ovExp, by.UserID, now).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE budgets SET limit_usd = $2::numeric / 100, alert_thresholds = $3, hard_block = $4,
				       override_by = $5, override_reason = $6, override_expires_at = $7, updated_by = $8, updated_at = $9
				WHERE id = $1`, id, w.LimitCents, w.Thresholds, w.HardBlock, ovBy, ovWhy, ovExp, by.UserID, now)
		}
		if err != nil {
			return fmt.Errorf("store budget: %w", err)
		}
		if ch.After, err = scanSetting(tx.QueryRow(ctx, settingSelect+` WHERE b.id = $1`, id)); err != nil {
			return fmt.Errorf("reload budget: %w", err)
		}
		return r.appendAll(ctx, tx, teamID, AuditEvents(w, ch, by, correlationID, now))
	})
	if err != nil {
		return Change{}, err
	}
	return ch, nil
}

// List implements AdminRepository.
func (r *PostgresAdminRepository) List(ctx context.Context, team string, month *time.Time) ([]Setting, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE slug = $1)`, team).Scan(&exists); err != nil {
		return nil, fmt.Errorf("find team: %w", err)
	}
	if !exists {
		return nil, apperr.NotFound("team %q not found", team)
	}
	rows, err := r.pool.Query(ctx, settingSelect+`
		WHERE t.slug = $1 AND ($2::date IS NULL OR b.month = $2::date)
		ORDER BY b.month DESC, b.environment_id IS NOT NULL, e.name`, team, month)
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Setting, error) { return scanSetting(row) })
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	return out, nil
}

// RecordActual implements AdminRepository.
func (r *PostgresAdminRepository) RecordActual(ctx context.Context, a ActualWrite, by identity.Principal, correlationID string) (Setting, error) {
	var out Setting
	err := r.tx.InTx(ctx, func(tx pgx.Tx) error {
		teamID, envID, err := target(ctx, tx, a.Team, a.Environment)
		if err != nil {
			return err
		}
		now := r.clock.Now()
		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			UPDATE budgets SET actual_usd = $4::numeric / 100, actual_recorded_at = $5
			WHERE team_id = $1 AND environment_id IS NOT DISTINCT FROM $2 AND month = $3::date
			RETURNING id`, teamID, envID, a.Month, a.ActualCents, now).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("team %s has no %s budget for %s", a.Team, scopeName(a.Environment), a.Month.Format("2006-01"))
		}
		if err != nil {
			return fmt.Errorf("record actual: %w", err)
		}
		if out, err = scanSetting(tx.QueryRow(ctx, settingSelect+` WHERE b.id = $1`, id)); err != nil {
			return fmt.Errorf("reload budget: %w", err)
		}
		return r.appendAll(ctx, tx, teamID, []audit.Event{ActualAuditEvent(a, out, by, correlationID, now)})
	})
	if err != nil {
		return Setting{}, err
	}
	return out, nil
}
