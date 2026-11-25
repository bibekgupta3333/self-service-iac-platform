package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// PostgresRepository implements Repository with pgx.
type PostgresRepository struct {
	pool  *pgxpool.Pool
	tx    *db.TxRunner
	audit *audit.Writer
}

var _ Repository = (*PostgresRepository)(nil)

// NewPostgresRepository returns a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool, auditWriter *audit.Writer) *PostgresRepository {
	return &PostgresRepository{pool: pool, tx: db.NewTxRunner(pool), audit: auditWriter}
}

const moduleCols = `id, name, display_name, description, category, owners, repo_path, status, created_at, updated_at`

func scanModule(row pgx.Row) (Module, error) {
	var m Module
	err := row.Scan(&m.ID, &m.Name, &m.DisplayName, &m.Description, &m.Category, &m.Owners, &m.RepoPath,
		&m.Status, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// ListModules implements Repository.
func (r *PostgresRepository) ListModules(ctx context.Context, f ModuleFilter) ([]Module, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+moduleCols+` FROM modules
		WHERE ($1 = '' OR category = $1) AND ($2 = '' OR status::text = $2)
		ORDER BY name`, f.Category, string(f.Status))
	if err != nil {
		return nil, fmt.Errorf("query modules: %w", err)
	}
	mods, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Module, error) { return scanModule(row) })
	if err != nil {
		return nil, fmt.Errorf("scan modules: %w", err)
	}
	return mods, nil
}

// GetModule implements Repository.
func (r *PostgresRepository) GetModule(ctx context.Context, name string) (Module, error) {
	m, err := scanModule(r.pool.QueryRow(ctx, `SELECT `+moduleCols+` FROM modules WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Module{}, apperr.NotFound("module %q not found", name)
	}
	if err != nil {
		return Module{}, fmt.Errorf("query module: %w", err)
	}
	return m, nil
}

const versionCols = `v.id, v.module_id, m.name, v.version, v.git_tag, v.git_sha, v.manifest, v.inputs_schema,
	v.outputs, v.cost_hints, v.tf_version_range, v.aws_provider_range, coalesce(v.changelog_md, ''), v.status,
	coalesce(v.deprecation_note, ''), v.released_by, v.released_at`

func scanVersion(row pgx.Row) (ModuleVersion, error) {
	var (
		v                ModuleVersion
		version          string
		outputs          []byte
		manifest, schema []byte
		costHints        []byte
	)
	err := row.Scan(&v.ID, &v.ModuleID, &v.ModuleName, &version, &v.GitTag, &v.GitSHA, &manifest, &schema,
		&outputs, &costHints, &v.TFVersionRange, &v.AWSProviderRange, &v.Changelog, &v.Status,
		&v.DeprecationNote, &v.ReleasedBy, &v.ReleasedAt)
	if err != nil {
		return ModuleVersion{}, err
	}
	if v.Version, err = ParseSemVer(version); err != nil {
		return ModuleVersion{}, err
	}
	if err := json.Unmarshal(outputs, &v.Outputs); err != nil {
		return ModuleVersion{}, fmt.Errorf("decode outputs: %w", err)
	}
	v.Manifest, v.InputsSchema, v.CostHints = manifest, schema, costHints
	return v, nil
}

// ListVersions implements Repository.
func (r *PostgresRepository) ListVersions(ctx context.Context, moduleID *uuid.UUID) ([]ModuleVersion, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+versionCols+`
		FROM module_versions v JOIN modules m ON m.id = v.module_id
		WHERE $1::uuid IS NULL OR v.module_id = $1
		ORDER BY m.name, v.major DESC, v.minor DESC, v.patch DESC`, moduleID)
	if err != nil {
		return nil, fmt.Errorf("query module versions: %w", err)
	}
	vs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ModuleVersion, error) { return scanVersion(row) })
	if err != nil {
		return nil, fmt.Errorf("scan module versions: %w", err)
	}
	return vs, nil
}

// GetVersion implements Repository.
func (r *PostgresRepository) GetVersion(ctx context.Context, module string, sv SemVer) (ModuleVersion, error) {
	return getVersion(ctx, r.pool, module, sv)
}

func getVersion(ctx context.Context, q db.DBTX, module string, sv SemVer) (ModuleVersion, error) {
	v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionCols+`
		FROM module_versions v JOIN modules m ON m.id = v.module_id
		WHERE m.name = $1 AND v.version = $2`, module, sv.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return ModuleVersion{}, apperr.NotFound("module %s has no version %s", module, sv)
	}
	if err != nil {
		return ModuleVersion{}, fmt.Errorf("query module version: %w", err)
	}
	return v, nil
}

// Index implements Repository.
func (r *PostgresRepository) Index(ctx context.Context, m Module, v ModuleVersion, ev audit.Event) (ModuleVersion, bool, error) {
	var (
		stored  ModuleVersion
		created bool
	)
	err := r.tx.InTx(ctx, func(tx pgx.Tx) error {
		var moduleID uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO modules (name, display_name, description, category, owners, repo_path)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (name) DO UPDATE SET
				display_name = EXCLUDED.display_name, description = EXCLUDED.description,
				category = EXCLUDED.category, owners = EXCLUDED.owners, updated_at = now()
			RETURNING id`, m.Name, m.DisplayName, m.Description, m.Category, m.Owners, m.RepoPath).Scan(&moduleID)
		if err != nil {
			return fmt.Errorf("upsert module: %w", err)
		}
		outputs, err := json.Marshal(v.Outputs)
		if err != nil {
			return fmt.Errorf("encode outputs: %w", err)
		}
		var costHints any
		if len(v.CostHints) > 0 {
			costHints = []byte(v.CostHints)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO module_versions (module_id, version, major, minor, patch, git_tag, git_sha, manifest,
				inputs_schema, outputs, cost_hints, tf_version_range, aws_provider_range, changelog_md, released_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, nullif($14, ''), $15)
			ON CONFLICT (module_id, version) DO NOTHING`,
			moduleID, v.Version.String(), v.Version.Major, v.Version.Minor, v.Version.Patch, v.GitTag, v.GitSHA,
			[]byte(v.Manifest), []byte(v.InputsSchema), outputs, costHints, v.TFVersionRange, v.AWSProviderRange,
			v.Changelog, v.ReleasedBy)
		if err != nil {
			if db.IsUniqueViolation(err, "module_versions_git_tag_key") {
				return apperr.Conflict("version_immutable", "git tag "+v.GitTag+" is already indexed")
			}
			return fmt.Errorf("insert module version: %w", err)
		}
		created = tag.RowsAffected() == 1
		if stored, err = getVersion(ctx, tx, m.Name, v.Version); err != nil {
			return err
		}
		if !created {
			if !strings.EqualFold(stored.GitSHA, v.GitSHA) {
				return apperr.Conflict("version_immutable",
					fmt.Sprintf("%s is already indexed at commit %s", v.GitTag, stored.GitSHA))
			}
			return nil
		}
		if _, err := r.audit.Append(ctx, tx, ev); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		return nil
	})
	if err != nil {
		return ModuleVersion{}, false, err
	}
	return stored, created, nil
}
