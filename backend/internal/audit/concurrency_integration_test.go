package audit_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// Concurrent writers serialise on the chain lock: every append succeeds and the chain verifies.
func TestConcurrentAuditWriters(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	ctx := context.Background()
	w := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	txr := db.NewTxRunner(pool)

	const n = 40
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = txr.InTx(ctx, func(tx pgx.Tx) error {
				_, err := w.Append(ctx, tx, audit.Event{ActorType: audit.ActorSystem, ActorID: "worker",
					Action: "test.concurrent", SubjectType: "request", SubjectID: fmt.Sprint(i)})
				return err
			})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	res, err := audit.NewVerifier(pool).Verify(ctx, 1, 0)
	if err != nil || !res.Valid || res.Checked != n {
		t.Fatalf("verify = %+v, %v", res, err)
	}
}
