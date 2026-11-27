package requests

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
)

// StackHolder implements Repository: the stack at path, its lock and the lock holder's
// request, read in one repeatable-read snapshot so the three agree.
func (r *PostgresRepository) StackHolder(ctx context.Context, path string) (StackHolder, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return StackHolder{}, fmt.Errorf("begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	h := StackHolder{StackPath: path}
	err = tx.QueryRow(ctx, `SELECT id FROM stacks WHERE path = $1`, path).Scan(&h.StackID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StackHolder{}, &apperr.Error{Kind: apperr.KindNotFound, Code: "stack_not_found",
			Message: fmt.Sprintf("no stack at %s", path)}
	}
	if err != nil {
		return StackHolder{}, fmt.Errorf("query stack: %w", err)
	}
	var l locks.Lock
	err = tx.QueryRow(ctx, `SELECT stack_id, request_id, holder, fencing_token, acquired_at, expires_at
		FROM locks WHERE stack_id = $1`, h.StackID).
		Scan(&l.StackID, &l.RequestID, &l.Holder, &l.FencingToken, &l.AcquiredAt, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, nil
	}
	if err != nil {
		return StackHolder{}, fmt.Errorf("query stack lock: %w", err)
	}
	req, err := getRequest(ctx, tx, l.RequestID)
	if err != nil {
		return StackHolder{}, err
	}
	h.Lock, h.Request = &l, req
	err = tx.QueryRow(ctx, `SELECT github_login FROM users WHERE id = $1`, req.CreatedBy).Scan(&h.RequesterLogin)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return StackHolder{}, fmt.Errorf("query request author: %w", err)
	}
	return h, nil
}
