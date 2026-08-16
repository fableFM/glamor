package gates

import (
	"context"
	"fmt"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

type scanner interface {
	Scan(dest ...any) error
}

const gateColumns = `id, run_id, stage_id, kind, question, context_json, state,
	answer, idempotency_key, created_at, resolved_at`

func scanGate(s scanner) (gate, error) {
	var g gate
	err := s.Scan(
		&g.id, &g.runID, &g.stageID, &g.kind, &g.question, &g.contextJSON,
		&g.state, &g.answer, &g.idempotencyKey, &g.createdAt, &g.resolvedAt,
	)
	return g, err
}

func (q *query) CreateGate(ctx context.Context, req dtorep.CreateGateRequest) error {
	_, err := q.conn.ExecContext(ctx,
		`INSERT INTO gates (id, run_id, stage_id, kind, question, context_json, state, idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.RunID, req.StageID, string(req.Kind), req.Question, req.ContextJSON,
		string(dtorep.GateStateOpen), req.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("failed to insert gate: %w", store.MapError(err))
	}
	return nil
}

func (q *query) GetGateByID(ctx context.Context, id string) (*dtorep.Gate, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+gateColumns+` FROM gates WHERE id = ?`, id)
	g, err := scanGate(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get gate by id: %w", store.MapError(err))
	}
	dto := mapGateToDTO(g)
	return &dto, nil
}

func (q *query) GetGateByIdempotencyKey(ctx context.Context, key string) (*dtorep.Gate, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+gateColumns+` FROM gates WHERE idempotency_key = ?`, key)
	g, err := scanGate(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get gate by idempotency key: %w", store.MapError(err))
	}
	dto := mapGateToDTO(g)
	return &dto, nil
}

func (q *query) ListGatesByRun(ctx context.Context, runID string) ([]dtorep.Gate, error) {
	return q.listGates(ctx, `SELECT `+gateColumns+` FROM gates WHERE run_id = ? ORDER BY id`, runID)
}

func (q *query) ListOpenGates(ctx context.Context, runID string) ([]dtorep.Gate, error) {
	return q.listGates(ctx, `SELECT `+gateColumns+` FROM gates WHERE run_id = ? AND state = 'open' ORDER BY id`, runID)
}

func (q *query) listGates(ctx context.Context, sqlStr string, args ...any) ([]dtorep.Gate, error) {
	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list open gates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Gate
	for rows.Next() {
		g, err := scanGate(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan gate: %w", err)
		}
		out = append(out, mapGateToDTO(g))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate gates: %w", err)
	}
	return out, nil
}

func (q *query) ResolveGate(ctx context.Context, id string, to dtorep.GateState, answer *string, resolvedAt time.Time) (bool, error) {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE gates SET state = ?, answer = COALESCE(?, answer), resolved_at = ?
		 WHERE id = ? AND state = 'open'`,
		string(to), answer, resolvedAt, id)
	if err != nil {
		return false, fmt.Errorf("failed to resolve gate: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get affected rows: %w", err)
	}
	return affected == 1, nil
}
