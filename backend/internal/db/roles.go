package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppRole is the least-privileged role the API connects as at runtime.
const AppRole = "iacp_app"

// ProvisionAppRole enables login for iacp_app with password, using an owner/superuser
// pool. It is idempotent. The password is quoted server-side with format('%L').
// Concurrent provisioning from several databases can race on the shared role row, so
// transient catalog conflicts are retried.
func ProvisionAppRole(ctx context.Context, owner *pgxpool.Pool, password string) error {
	if password == "" {
		return errors.New("provision app role: empty password")
	}
	var stmt string
	if err := owner.QueryRow(ctx, `SELECT format('ALTER ROLE %I WITH LOGIN PASSWORD %L', $1::text, $2::text)`,
		AppRole, password).Scan(&stmt); err != nil {
		return fmt.Errorf("provision app role: build statement: %w", err)
	}
	var err error
	for attempt := range 5 {
		if _, err = owner.Exec(ctx, stmt); err == nil || !isCatalogRace(err) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("provision app role: %w", err)
	}
	return nil
}

// isCatalogRace matches "tuple concurrently updated" (XX000) and serialization failures.
func isCatalogRace(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "XX000" || pgErr.Code == "40001" || pgErr.Code == "23505")
}

// Privileged reports whether the pool's role is a superuser or owns audit_events, in
// which case the append-only grants do not constrain it.
func Privileged(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var privileged bool
	err := pool.QueryRow(ctx, `
		SELECT r.rolsuper OR coalesce(pg_has_role(current_user, t.tableowner, 'USAGE'), false)
		FROM pg_roles r
		LEFT JOIN pg_tables t ON t.schemaname = 'public' AND t.tablename = 'audit_events'
		WHERE r.rolname = current_user`).Scan(&privileged)
	if err != nil {
		return false, fmt.Errorf("check connection privileges: %w", err)
	}
	return privileged, nil
}
