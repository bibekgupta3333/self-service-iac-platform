package recovery

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// lifecycleRepo is the recovery repository wired to the request lifecycle, as in the API.
func lifecycleRepo(pool *pgxpool.Pool, w *audit.Writer) *PostgresRepository {
	reqRepo := requests.NewPostgresRepository(pool, w)
	lockSvc := locks.NewService(locks.NewPostgresRepository(pool, w), platform.SystemClock{}, time.Hour)
	svc := requests.NewService(reqRepo, nil, reqRepo, lockSvc, platform.SystemClock{}, platform.RandomIDs{})
	return NewPostgresRepository(pool, w).WithLifecycle(requests.NewApplyDriver(svc, w).ForTx)
}
