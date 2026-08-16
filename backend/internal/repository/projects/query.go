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

const projectColumns = `id, path, name, default_branch, ide_command, created_at`

func scanProject(s scanner) (project, error) {
	var p project
	err := s.Scan(&p.id, &p.path, &p.name, &p.defaultBranch, &p.ideCommand, &p.createdAt)
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
