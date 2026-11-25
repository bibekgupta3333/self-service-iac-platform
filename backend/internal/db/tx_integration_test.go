package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
)

func TestInTxRollsBackOnPanic(t *testing.T) {
	_, pool := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	txr := db.NewTxRunner(pool)
	if _, err := pool.Exec(ctx, "CREATE TABLE panic_probe (n int)"); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("call %d: panic was swallowed; InTx must re-panic", i)
				}
			}()
			_ = txr.InTx(ctx, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, "INSERT INTO panic_probe VALUES (1)"); err != nil {
					return err
				}
				panic("boom")
			})
		}()
	}

	if n := pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("acquired connections after panics = %d, want 0 (leaked transactions)", n)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM panic_probe").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows = %d, %v; panicking transactions must not commit", rows, err)
	}
}

func TestInTxCommitAndRollback(t *testing.T) {
	_, pool := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	txr := db.NewTxRunner(pool)
	if _, err := pool.Exec(ctx, "CREATE TABLE tx_probe (n int)"); err != nil {
		t.Fatal(err)
	}
	insert := func(tx pgx.Tx) error { _, err := tx.Exec(ctx, "INSERT INTO tx_probe VALUES (1)"); return err }

	if err := txr.InTx(ctx, insert); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("fail")
	err := txr.InTx(ctx, func(tx pgx.Tx) error {
		if err := insert(tx); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tx_probe").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows = %d, %v; want only the committed insert", rows, err)
	}
	if n := pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("acquired connections = %d", n)
	}
}
