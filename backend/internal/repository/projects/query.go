package projects

import (
	"context"
	"fmt"
	"strings"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

// scanner — общий интерфейс *sql.Row / *sql.Rows для сканирования одной строки.
type scanner interface {
	Scan(dest ...any) error
}

const projectColumns = `id, path, name, default_branch, ide_command, notify_tg_default, created_at`

func scanProject(s scanner) (project, error) {
	var p project
	err := s.Scan(&p.id, &p.path, &p.name, &p.defaultBranch, &p.ideCommand, &p.notifyTGDefault, &p.createdAt)
	return p, err
}

func (q *query) CreateProject(ctx context.Context, req dtorep.CreateProjectRequest) (int64, error) {
	res, err := q.conn.ExecContext(ctx,
		`INSERT INTO projects (path, name, default_branch, ide_command) VALUES (?, ?, ?, ?)`,
		req.Path, req.Name, req.DefaultBranch, req.IDECommand)
	if err != nil {
		return 0, fmt.Errorf("failed to insert project: %w", store.MapError(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get project id: %w", err)
	}
	return id, nil
}

func (q *query) GetProjectByID(ctx context.Context, id int64) (*dtorep.Project, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get project by id: %w", store.MapError(err))
	}
	dto := mapProjectToDTO(p)
	return &dto, nil
}

func (q *query) GetProjectByPath(ctx context.Context, path string) (*dtorep.Project, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE path = ?`, path)
	p, err := scanProject(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get project by path: %w", store.MapError(err))
	}
	dto := mapProjectToDTO(p)
	return &dto, nil
}

func (q *query) ListProjects(ctx context.Context) ([]dtorep.Project, error) {
	rows, err := q.conn.QueryContext(ctx,
		`SELECT `+projectColumns+` FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan project: %w", err)
		}
		out = append(out, mapProjectToDTO(p))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate projects: %w", err)
	}
	return out, nil
}

func (q *query) UpdateProject(ctx context.Context, id int64, req dtorep.PatchProjectRequest) error {
	sets := []string{}
	args := []any{}
	if req.DefaultBranch != nil {
		sets = append(sets, "default_branch = ?")
		args = append(args, *req.DefaultBranch)
	}
	if req.IDECommand != nil {
		sets = append(sets, "ide_command = ?")
		args = append(args, *req.IDECommand)
	}
	if req.NotifyTgDefault != nil {
		sets = append(sets, "notify_tg_default = ?")
		args = append(args, *req.NotifyTgDefault)
	}
	if len(sets) == 0 {
		return nil
	}
	sqlStr := fmt.Sprintf(`UPDATE projects SET %s WHERE id = ?`, strings.Join(sets, ", "))
	args = append(args, id)

	res, err := q.conn.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return fmt.Errorf("failed to update project: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get affected rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("project %d: %w", id, cstmerrors.ErrNotFound)
	}
	return nil
}

// DeleteProjectCascade — удаление проекта со ВСЕЙ историей (раны, стадии,
// события, гейты, заметки, артефакты) в одной транзакции через вызывающий
// слой. Порядок — от детей к родителям (FK).
func (q *query) DeleteProjectCascade(ctx context.Context, id int64) error {
	// FK: events/gates/notes/artifacts ссылаются на runs; run_stages на runs
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM notes WHERE run_id IN (SELECT id FROM runs WHERE project_id = ?)`, []any{id}},
		{`DELETE FROM gates WHERE run_id IN (SELECT id FROM runs WHERE project_id = ?)`, []any{id}},
		{`DELETE FROM artifacts WHERE run_id IN (SELECT id FROM runs WHERE project_id = ?)`, []any{id}},
		{`DELETE FROM events WHERE run_id IN (SELECT id FROM runs WHERE project_id = ?)`, []any{id}},
		{`DELETE FROM run_stages WHERE run_id IN (SELECT id FROM runs WHERE project_id = ?)`, []any{id}},
		{`DELETE FROM runs WHERE project_id = ?`, []any{id}},
		{`DELETE FROM lessons WHERE project_id = ?`, []any{id}},
		{`DELETE FROM pipelines WHERE project_id = ?`, []any{id}},
		{`DELETE FROM projects WHERE id = ?`, []any{id}},
	}
	for _, stmt := range statements {
		if _, err := q.conn.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return fmt.Errorf("failed to cascade delete project %d: %w", id, err)
		}
	}
	return nil
}
