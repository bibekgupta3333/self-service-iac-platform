package budgets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// PostgresRepository implements Repository with pgx. It only reads.
type PostgresRepository struct{ db db.DBTX }

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository over conn.
func NewPostgresRepository(conn db.DBTX) *PostgresRepository { return &PostgresRepository{db: conn} }

// Environment implements Repository.
func (r *PostgresRepository) Environment(ctx context.Context, name string) (Environment, error) {
	e := Environment{Name: name}
	err := r.db.QueryRow(ctx, `SELECT allowed_regions FROM environments WHERE name::text = $1`, name).
		Scan(&e.AllowedRegions)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "environment_not_found",
			Message: fmt.Sprintf("environment %q is not configured", name)}
	}
	if err != nil {
		return Environment{}, fmt.Errorf("load environment %s: %w", name, err)
	}
	return e, nil
}

// MonthlyBudget implements Repository.
func (r *PostgresRepository) MonthlyBudget(ctx context.Context, team, env string, month time.Time) (*Budget, error) {
	b := Budget{Month: month}
	var envSpecific bool
	err := r.db.QueryRow(ctx, `
		SELECT b.limit_usd::float8, b.spent_usd::float8, b.hard_block, b.environment_id IS NOT NULL
		FROM budgets b
		JOIN teams t ON t.id = b.team_id
		LEFT JOIN environments e ON e.id = b.environment_id
		WHERE t.slug = $1 AND b.month = $3::date
		  AND (b.environment_id IS NULL OR e.name::text = $2)
		ORDER BY b.environment_id IS NULL
		LIMIT 1`, team, env, month).Scan(&b.LimitUSD, &b.SpentUSD, &b.HardBlock, &envSpecific)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load budget for %s/%s: %w", team, env, err)
	}
	b.Scope = ScopeTeam
	if envSpecific {
		b.Scope = ScopeEnvironment
	}
	return &b, nil
}

// YankedVersions implements Repository.
func (r *PostgresRepository) YankedVersions(ctx context.Context) ([]YankedVersion, error) {
	rows, err := r.db.Query(ctx, `
		SELECT m.name, v.version
		FROM module_versions v
		JOIN modules m ON m.id = v.module_id
		WHERE v.status = 'yanked' OR m.status = 'yanked'
		ORDER BY m.name, v.major, v.minor, v.patch, v.version`)
	if err != nil {
		return nil, fmt.Errorf("list yanked versions: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (YankedVersion, error) {
		var y YankedVersion
		return y, row.Scan(&y.Module, &y.Version)
	})
	if err != nil {
		return nil, fmt.Errorf("list yanked versions: %w", err)
	}
	return out, nil
}
