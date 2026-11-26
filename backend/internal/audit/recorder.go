package audit

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
)

// Recorder appends stand-alone audit events (those with no state change to commit with,
// such as a login or an RBAC denial) in a transaction of their own. It is still append-only:
// the only operation is Append.
type Recorder struct {
	txr    *db.TxRunner
	writer *Writer
}

// NewRecorder returns a Recorder.
func NewRecorder(txr *db.TxRunner, w *Writer) *Recorder { return &Recorder{txr: txr, writer: w} }

// Record seals and stores e.
func (r *Recorder) Record(ctx context.Context, e Event) error {
	return r.txr.InTx(ctx, func(tx pgx.Tx) error {
		_, err := r.writer.Append(ctx, tx, e)
		return err
	})
}
