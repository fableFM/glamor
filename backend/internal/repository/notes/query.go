package notes

import (
	"context"
	"fmt"
	"time"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

type scanner interface {
	Scan(dest ...any) error
}

const noteColumns = `id, run_id, stage_id, kind, text, consumed, idempotency_key, created_at, consumed_at`

func mapNoteToDTO(n note) dtorep.Note {
	out := dtorep.Note{
		ID:             n.id,
		RunID:          n.runID,
		Kind:           dtorep.NoteKind(n.kind),
		Text:           n.text,
		Consumed:       n.consumed,
		IdempotencyKey: n.idempotencyKey,
		CreatedAt:      n.createdAt,
	}
	if n.stageID.Valid {
		out.StageID = &n.stageID.Int64
	}
	if n.consumedAt.Valid {
		t := n.consumedAt.Time
		out.ConsumedAt = &t
	}
	return out
}

func scanNote(s scanner) (note, error) {
	var n note
	err := s.Scan(&n.id, &n.runID, &n.stageID, &n.kind, &n.text, &n.consumed,
		&n.idempotencyKey, &n.createdAt, &n.consumedAt)
	return n, err
}

func (q *query) CreateNote(ctx context.Context, req dtorep.CreateNoteRequest) (int64, error) {
	res, err := q.conn.ExecContext(ctx,
		`INSERT INTO notes (run_id, stage_id, kind, text, idempotency_key)
		 VALUES (?, ?, ?, ?, ?)`,
		req.RunID, req.StageID, string(req.Kind), req.Text, req.IdempotencyKey)
	if err != nil {
		return 0, fmt.Errorf("failed to insert note: %w", store.MapError(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get note id: %w", err)
	}
	return id, nil
}

func (q *query) GetNoteByIdempotencyKey(ctx context.Context, key string) (*dtorep.Note, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+noteColumns+` FROM notes WHERE idempotency_key = ?`, key)
	n, err := scanNote(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get note by idempotency key: %w", store.MapError(err))
	}
	dto := mapNoteToDTO(n)
	return &dto, nil
}

func (q *query) ListNotesByRun(ctx context.Context, runID string) ([]dtorep.Note, error) {
	return q.listNotes(ctx, `SELECT `+noteColumns+` FROM notes WHERE run_id = ? ORDER BY id`, runID)
}

func (q *query) ListUnconsumedNotes(ctx context.Context, runID string) ([]dtorep.Note, error) {
	return q.listNotes(ctx,
		`SELECT `+noteColumns+` FROM notes WHERE run_id = ? AND consumed = 0 AND kind = 'note' ORDER BY id`, runID)
}

func (q *query) ListUnconsumedSteers(ctx context.Context, stageID int64) ([]dtorep.Note, error) {
	return q.listNotes(ctx,
		`SELECT `+noteColumns+` FROM notes WHERE stage_id = ? AND consumed = 0 AND kind = 'steer' ORDER BY id`, stageID)
}

func (q *query) listNotes(ctx context.Context, sqlStr string, args ...any) ([]dtorep.Note, error) {
	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list notes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan note: %w", err)
		}
		out = append(out, mapNoteToDTO(n))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate notes: %w", err)
	}
	return out, nil
}

func (q *query) MarkNoteConsumed(ctx context.Context, id int64) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE notes SET consumed = 1, consumed_at = ? WHERE id = ? AND consumed = 0`,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to mark note consumed: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("note %d: %w", id, cstmerrors.ErrNotFound)
	}
	return nil
}
