package requests

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StatusEventsAfter implements Repository: the status changes of a request recorded in
// the audit log after seq, oldest first. The audit writer holds one advisory lock from
// insert to commit, so rows become visible in seq order and a reader never skips one.
func (r *PostgresRepository) StatusEventsAfter(ctx context.Context, id uuid.UUID, after int64, limit int) ([]StatusEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT seq, occurred_at, action, payload
		FROM audit_events
		WHERE subject_type = 'request' AND subject_id = $1 AND action = ANY($2) AND seq > $3
		ORDER BY seq
		LIMIT $4`, id.String(), StatusActions, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query status events: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StatusEvent, error) {
		var (
			e       StatusEvent
			action  string
			payload []byte
		)
		if err := row.Scan(&e.Seq, &e.At, &action, &payload); err != nil {
			return StatusEvent{}, err
		}
		parsed, ok := ParseStatusEvent(e.Seq, e.At, action, payload)
		if !ok {
			// Our own writer produced the payload, so this is not expected; the row is skipped.
			return StatusEvent{Seq: e.Seq, At: e.At}, nil
		}
		return parsed, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan status events: %w", err)
	}
	// Skipped rows carry no status.
	out := events[:0]
	for _, e := range events {
		if e.Status != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// LatestStatusSeq implements Repository: the newest status event of a request, 0 if none.
func (r *PostgresRepository) LatestStatusSeq(ctx context.Context, id uuid.UUID) (int64, error) {
	var seq int64
	err := r.pool.QueryRow(ctx, `SELECT coalesce(max(seq), 0) FROM audit_events
		WHERE subject_type = 'request' AND subject_id = $1 AND action = ANY($2)`, id.String(), StatusActions).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("query latest status event: %w", err)
	}
	return seq, nil
}
