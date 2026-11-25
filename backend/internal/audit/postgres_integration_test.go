package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

func TestWriterAndVerifierAgainstPostgres(t *testing.T) {
	pool, owner := dbtest.NewDatabaseWithOwner(t)
	ctx := context.Background()
	w := audit.NewWriter(platform.SystemClock{}, platform.RandomIDs{})
	txr := db.NewTxRunner(pool)
	team := uuid.New()

	for i := range 5 {
		err := txr.InTx(ctx, func(tx pgx.Tx) error {
			_, err := w.Append(ctx, tx, audit.Event{
				ActorType: audit.ActorSystem, ActorID: "worker", ActorIP: "::ffff:198.51.100.4", Action: "test.event",
				SubjectType: "request", SubjectID: fmt.Sprintf("req-%d", i), TeamID: &team,
				Payload: json.RawMessage(fmt.Sprintf(`{"z": %d, "a": [1, 2.50, "x", 1e2, 0.10],
					"big": 9007199254740993, "huge": 123456789012345678901234567890.5,
					"nested": {"b": true, "a": null, "s": "<&>é"}}`, i)),
			})
			return err
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// A rolled-back append must leave no trace and not break the chain (seq gap is fine).
	_ = txr.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := w.Append(ctx, tx, audit.Event{ActorType: audit.ActorSystem, ActorID: "worker",
			Action: "test.rolled_back", SubjectType: "request", SubjectID: "x"}); err != nil {
			return err
		}
		return fmt.Errorf("force rollback")
	})
	_ = txr.InTx(ctx, func(tx pgx.Tx) error {
		_, err := w.Append(ctx, tx, audit.Event{ActorType: audit.ActorUser, ActorID: "u", Action: "test.after_gap",
			SubjectType: "request", SubjectID: "y"})
		return err
	})

	v := audit.NewVerifier(pool)
	res, err := v.Verify(ctx, 1, 0)
	if err != nil || !res.Valid || res.Checked != 6 {
		t.Fatalf("verify full chain = %+v, %v", res, err)
	}
	res, err = v.Verify(ctx, 3, 4)
	if err != nil || !res.Valid || res.Checked != 2 {
		t.Fatalf("verify range = %+v, %v", res, err)
	}

	t.Run("append-only", func(t *testing.T) {
		for _, stmt := range []string{
			"UPDATE audit_events SET action = 'tampered' WHERE seq = 2",
			"DELETE FROM audit_events WHERE seq = 2",
			"TRUNCATE audit_events",
		} {
			if _, err := pool.Exec(ctx, stmt); err == nil {
				t.Errorf("%q succeeded; audit_events must be append-only", stmt)
			}
		}
	})

	t.Run("link check rejects forged prev_hash", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO audit_events (id, occurred_at, actor_type, actor_id, action, subject_type,
			subject_id, prev_hash, hash) VALUES (gen_random_uuid(), now(), 'system', 'x', 'forged', 'request', 'x',
			decode(repeat('00', 32), 'hex'), decode(repeat('ab', 32), 'hex'))`)
		if err == nil {
			t.Fatal("insert with stale prev_hash succeeded")
		}
	})

	t.Run("app role cannot bypass triggers or modify rows", func(t *testing.T) {
		for _, stmt := range []string{
			"SET session_replication_role = replica",
			"UPDATE audit_events SET action = 'x' WHERE seq = 1",
			"DELETE FROM audit_events WHERE seq = 1",
			"ALTER TABLE audit_events DISABLE TRIGGER USER",
		} {
			if _, err := pool.Exec(ctx, stmt); err == nil {
				t.Errorf("iacp_app could run %q", stmt)
			}
		}
	})

	t.Run("tampering is detected", func(t *testing.T) {
		// Simulate a superuser (not the app role) bypassing triggers.
		err := db.NewTxRunner(owner).InTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE audit_events SET payload = '{"z": 99}' WHERE seq = 3`)
			return err
		})
		if err != nil {
			t.Fatalf("tamper: %v", err)
		}
		res, err := v.Verify(ctx, 1, 0)
		if err != nil || res.Valid || res.FirstBadSeq == nil || *res.FirstBadSeq != 3 {
			t.Fatalf("verify after tamper = %+v, %v", res, err)
		}
	})
}
