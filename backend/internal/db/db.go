// Package db holds the PostgreSQL adapters shared by repositories: pool setup,
// schema migrations and a transaction runner.
package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// DBTX is the query surface shared by *pgxpool.Pool and pgx.Tx, so repositories
// work both inside and outside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Open creates a connection pool and verifies connectivity.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrator applies embedded goose migrations.
//
// goose is used (rather than a hand-rolled runner) because docs/03 already names it,
// it handles plpgsql statement blocks and down migrations, and its Provider API needs
// no global state.
type Migrator struct {
	pool   *pgxpool.Pool
	fsys   fs.FS
	logger *slog.Logger
}

// NewMigrator returns a Migrator for the migrations in fsys.
func NewMigrator(pool *pgxpool.Pool, fsys fs.FS, logger *slog.Logger) *Migrator {
	return &Migrator{pool: pool, fsys: fsys, logger: logger}
}

func (m *Migrator) provider() (*goose.Provider, func() error, error) {
	sqlDB := stdlib.OpenDBFromPool(m.pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, m.fsys)
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("goose provider: %w", err)
	}
	return p, sqlDB.Close, nil
}

// Up applies all pending migrations.
func (m *Migrator) Up(ctx context.Context) error {
	p, closeFn, err := m.provider()
	if err != nil {
		return err
	}
	defer func() { _ = closeFn() }()
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	for _, r := range results {
		m.logger.InfoContext(ctx, "migration applied", "version", r.Source.Version, "duration_ms", r.Duration.Milliseconds())
	}
	return nil
}

// Version returns the current schema version.
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	p, closeFn, err := m.provider()
	if err != nil {
		return 0, err
	}
	defer func() { _ = closeFn() }()
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	return v, nil
}

// TxRunner runs functions inside a database transaction.
type TxRunner struct{ pool *pgxpool.Pool }

// NewTxRunner returns a TxRunner over pool.
func NewTxRunner(pool *pgxpool.Pool) *TxRunner { return &TxRunner{pool: pool} }

// InTx runs fn in a transaction, committing on nil error and rolling back otherwise.
// If fn panics the transaction is rolled back (returning its connection to the pool)
// and the panic is re-raised for the caller's recovery middleware.
func (r *TxRunner) InTx(ctx context.Context, fn func(tx pgx.Tx) error) (err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			// Use a fresh context: ctx may already be cancelled, and the connection must go back.
			rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rbCtx)
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
			}
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// IsUniqueViolation reports whether err is a unique_violation, optionally on a named constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
