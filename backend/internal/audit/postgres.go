package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// chainLockKey is the advisory lock shared with the audit_events_link_check trigger.
const chainLockKey = 727274

// Writer appends events to audit_events. It is the only write path for the table;
// there is deliberately no update or delete method.
type Writer struct {
	clock platform.Clock
	ids   platform.IDGenerator
}

// NewWriter returns a Writer.
func NewWriter(clock platform.Clock, ids platform.IDGenerator) *Writer {
	return &Writer{clock: clock, ids: ids}
}

// Append seals e onto the chain inside tx, so the audit row commits or rolls back
// together with the state change it describes.
func (w *Writer) Append(ctx context.Context, tx pgx.Tx, e Event) (Record, error) {
	if e.ID == uuid.Nil {
		e.ID = w.ids.NewID()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = w.clock.Now()
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", chainLockKey); err != nil {
		return Record{}, fmt.Errorf("audit chain lock: %w", err)
	}
	prev, err := headHash(ctx, tx)
	if err != nil {
		return Record{}, err
	}
	rec, err := Seal(prev, e)
	if err != nil {
		return Record{}, fmt.Errorf("seal audit event: %w", err)
	}
	var corr *string
	if e.CorrelationID != "" {
		corr = &e.CorrelationID
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO audit_events (id, occurred_at, actor_type, actor_id, action, subject_type, subject_id,
		                          team_id, correlation_id, payload, prev_hash, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING seq`,
		e.ID, e.OccurredAt, e.ActorType, e.ActorID, e.Action, e.SubjectType, e.SubjectID,
		e.TeamID, corr, []byte(e.Payload), rec.PrevHash, rec.Hash,
	).Scan(&rec.Seq)
	if err != nil {
		return Record{}, fmt.Errorf("insert audit event: %w", err)
	}
	return rec, nil
}

func headHash(ctx context.Context, q db.DBTX) ([]byte, error) {
	var h []byte
	err := q.QueryRow(ctx, "SELECT hash FROM audit_events ORDER BY seq DESC LIMIT 1").Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return GenesisHash, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit chain head: %w", err)
	}
	return h, nil
}

// Verifier recomputes the chain from stored rows.
type Verifier struct{ q db.DBTX }

// NewVerifier returns a Verifier reading through q.
func NewVerifier(q db.DBTX) *Verifier { return &Verifier{q: q} }

// Verify checks events with fromSeq <= seq <= toSeq (toSeq <= 0 means "to the head").
func (v *Verifier) Verify(ctx context.Context, fromSeq, toSeq int64) (VerifyResult, error) {
	anchor := GenesisHash
	var prev []byte
	err := v.q.QueryRow(ctx, "SELECT hash FROM audit_events WHERE seq < $1 ORDER BY seq DESC LIMIT 1", fromSeq).Scan(&prev)
	switch {
	case err == nil:
		anchor = prev
	case !errors.Is(err, pgx.ErrNoRows):
		return VerifyResult{}, fmt.Errorf("read audit anchor: %w", err)
	}

	rows, err := v.q.Query(ctx, `
		SELECT seq, id, occurred_at, actor_type, actor_id, action, subject_type, subject_id,
		       team_id, coalesce(correlation_id, ''), payload, prev_hash, hash
		FROM audit_events
		WHERE seq >= $1 AND ($2 <= 0 OR seq <= $2)
		ORDER BY seq`, fromSeq, toSeq)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("query audit events: %w", err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Record, error) {
		var r Record
		var payload []byte
		err := row.Scan(&r.Seq, &r.ID, &r.OccurredAt, &r.ActorType, &r.ActorID, &r.Action, &r.SubjectType,
			&r.SubjectID, &r.TeamID, &r.CorrelationID, &payload, &r.PrevHash, &r.Hash)
		r.Payload = payload
		return r, err
	})
	if err != nil {
		return VerifyResult{}, fmt.Errorf("scan audit events: %w", err)
	}
	return VerifyChain(anchor, records), nil
}
