package lessons

import (
	"context"
	"database/sql"
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

// lesson — приватная модель строки таблицы lessons.
type lesson struct {
	id           string
	title        string
	scope        string
	status       string
	projectID    sql.NullInt64
	path         string
	triggersJSON string
	runID        sql.NullString
	stageKey     string
	appliedCount int64
	relapseCount int64
	createdAt    time.Time
	updatedAt    sql.NullTime
}

const lessonColumns = `id, title, scope, status, project_id, path, triggers_json,
	run_id, stage_key, applied_count, relapse_count, created_at, updated_at`

func mapLessonToDTO(l lesson) *dtorep.Lesson {
	out := &dtorep.Lesson{
		ID:           l.id,
		Title:        l.title,
		Scope:        l.scope,
		Status:       l.status,
		Path:         l.path,
		TriggersJSON: l.triggersJSON,
		StageKey:     l.stageKey,
		AppliedCount: l.appliedCount,
		RelapseCount: l.relapseCount,
		CreatedAt:    l.createdAt,
	}
	if l.projectID.Valid {
		out.ProjectID = &l.projectID.Int64
	}
	if l.runID.Valid {
		out.RunID = &l.runID.String
	}
	if l.updatedAt.Valid {
		t := l.updatedAt.Time
		out.UpdatedAt = &t
	}
	return out
}

func scanLesson(s scanner) (lesson, error) {
	var l lesson
	err := s.Scan(&l.id, &l.title, &l.scope, &l.status, &l.projectID, &l.path,
		&l.triggersJSON, &l.runID, &l.stageKey, &l.appliedCount, &l.relapseCount,
		&l.createdAt, &l.updatedAt)
	return l, err
}

func (q *query) CreateLesson(ctx context.Context, l *dtorep.Lesson) error {
	_, err := q.conn.ExecContext(ctx,
		`INSERT INTO lessons (id, title, scope, status, project_id, path, triggers_json, run_id, stage_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.Title, l.Scope, l.Status, l.ProjectID, l.Path, l.TriggersJSON, l.RunID, l.StageKey)
	if err != nil {
		return fmt.Errorf("failed to insert lesson: %w", store.MapError(err))
	}
	return nil
}

func (q *query) GetLessonByID(ctx context.Context, id string) (*dtorep.Lesson, error) {
	row := q.conn.QueryRowContext(ctx, `SELECT `+lessonColumns+` FROM lessons WHERE id = ?`, id)
	l, err := scanLesson(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get lesson: %w", store.MapError(err))
	}
	return mapLessonToDTO(l), nil
}

func (q *query) ListLessons(ctx context.Context, status, scope string, projectID *int64) ([]dtorep.Lesson, error) {
	sqlStr := `SELECT ` + lessonColumns + ` FROM lessons WHERE 1=1`
	args := []any{}
	if status != "" {
		sqlStr += ` AND status = ?`
		args = append(args, status)
	}
	if scope != "" {
		sqlStr += ` AND scope = ?`
		args = append(args, scope)
	}
	if projectID != nil {
		sqlStr += ` AND project_id = ?`
		args = append(args, *projectID)
	}
	sqlStr += ` ORDER BY created_at DESC`

	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list lessons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan lesson: %w", err)
		}
		out = append(out, *mapLessonToDTO(l))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lessons: %w", err)
	}
	return out, nil
}

func (q *query) UpdateLessonStatus(ctx context.Context, id, status string) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update lesson status: %w", err)
	}
	return mustAffected(res, id)
}

func (q *query) UpdateLessonContent(ctx context.Context, id, title, triggersJSON, path string) error {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET title = ?, triggers_json = ?, path = ?, updated_at = ? WHERE id = ?`,
		title, triggersJSON, path, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update lesson content: %w", err)
	}
	return mustAffected(res, id)
}

func (q *query) IncrementApplied(ctx context.Context, id string) error {
	_, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET applied_count = applied_count + 1, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to increment applied_count: %w", err)
	}
	return nil
}

func (q *query) IncrementRelapse(ctx context.Context, id string) error {
	_, err := q.conn.ExecContext(ctx,
		`UPDATE lessons SET relapse_count = relapse_count + 1, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to increment relapse_count: %w", err)
	}
	return nil
}

func mustAffected(res sql.Result, id string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("lesson %s: %w", id, cstmerrors.ErrNotFound)
	}
	return nil
}
