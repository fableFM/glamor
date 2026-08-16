package events

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huandu/go-sqlbuilder"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

type scanner interface {
	Scan(dest ...any) error
}

var eventColumns = []string{"id", "run_id", "stage_id", "ts", "kind", "payload_json"}

func scanEvent(s scanner) (event, error) {
	var e event
	err := s.Scan(&e.id, &e.runID, &e.stageID, &e.ts, &e.kind, &e.payloadJSON)
	return e, err
}

func (q *query) AppendEvent(ctx context.Context, ev dtorep.Event) (int64, error) {
	res, err := q.conn.ExecContext(ctx,
		`INSERT INTO events (run_id, stage_id, kind, payload_json) VALUES (?, ?, ?, ?)`,
		ev.RunID, ev.StageID, ev.Kind, ev.PayloadJSON)
	if err != nil {
		return 0, fmt.Errorf("failed to append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get event id: %w", err)
	}
	return id, nil
}

func (q *query) ReplayEvents(ctx context.Context, runID string, afterID int64, limit int) ([]dtorep.Event, error) {
	sb := sqlbuilder.SQLite.NewSelectBuilder()
	sb.Select(eventColumns...).From("events")
	sb.Where(sb.GreaterThan("id", afterID))
	if runID != RunIDAll {
		sb.Where(sb.Equal("run_id", runID))
	}
	sb.OrderByAsc("id")
	if limit > 0 {
		sb.Limit(limit)
	}

	sqlStr, args := sb.Build()
	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to replay events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		out = append(out, mapEventToDTO(e))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate events: %w", err)
	}
	return out, nil
}

func (q *query) LatestEventID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	if err := q.conn.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("failed to get latest event id: %w", err)
	}
	return id.Int64, nil
}
