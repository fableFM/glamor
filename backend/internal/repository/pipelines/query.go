package pipelines

import (
	"context"
	"fmt"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

type scanner interface {
	Scan(dest ...any) error
}

const pipelineColumns = `id, project_id, name, version, parent_version_id, spec_json, created_at`

func scanPipeline(s scanner) (pipeline, error) {
	var p pipeline
	err := s.Scan(&p.id, &p.projectID, &p.name, &p.version, &p.parentVersionID, &p.specJSON, &p.createdAt)
	return p, err
}

func (q *query) CreatePipeline(ctx context.Context, req dtorep.CreatePipelineRequest) (int64, error) {
	res, err := q.conn.ExecContext(ctx,
		`INSERT INTO pipelines (project_id, name, version, parent_version_id, spec_json)
		 VALUES (?, ?, ?, ?, ?)`,
		req.ProjectID, req.Name, req.Version, req.ParentVersionID, req.SpecJSON)
	if err != nil {
		return 0, fmt.Errorf("failed to insert pipeline: %w", store.MapError(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get pipeline id: %w", err)
	}
	return id, nil
}

func (q *query) GetPipelineByID(ctx context.Context, id int64) (*dtorep.Pipeline, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines WHERE id = ?`, id)
	p, err := scanPipeline(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get pipeline by id: %w", store.MapError(err))
	}
	dto := mapPipelineToDTO(p)
	return &dto, nil
}

func (q *query) GetLatestPipeline(ctx context.Context, projectID *int64, name string) (*dtorep.Pipeline, error) {
	// project_id IS NULL <=> глобальный пайплайн; NULL = NULL не работает,
	// поэтому условие «IS ?» (SQLite: IS сравнивает NULL корректно).
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines
		 WHERE project_id IS ? AND name = ?
		 ORDER BY version DESC LIMIT 1`, projectID, name)
	p, err := scanPipeline(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest pipeline: %w", store.MapError(err))
	}
	dto := mapPipelineToDTO(p)
	return &dto, nil
}

func (q *query) ListPipelines(ctx context.Context, projectID *int64) ([]dtorep.Pipeline, error) {
	rows, err := q.conn.QueryContext(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines
		 WHERE project_id IS ? ORDER BY name, version DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pipelines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pipeline: %w", err)
		}
		out = append(out, mapPipelineToDTO(p))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate pipelines: %w", err)
	}
	return out, nil
}

func (q *query) ListPipelinesForProject(ctx context.Context, projectID int64) ([]dtorep.Pipeline, error) {
	rows, err := q.conn.QueryContext(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines
		 WHERE project_id = ? OR project_id IS NULL
		 ORDER BY name, version DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pipelines for project: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pipeline: %w", err)
		}
		out = append(out, mapPipelineToDTO(p))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate pipelines: %w", err)
	}
	return out, nil
}

func (q *query) ListPipelineVersions(ctx context.Context, projectID *int64, name string) ([]dtorep.Pipeline, error) {
	rows, err := q.conn.QueryContext(ctx,
		`SELECT `+pipelineColumns+` FROM pipelines
		 WHERE project_id IS ? AND name = ?
		 ORDER BY version DESC`, projectID, name)
	if err != nil {
		return nil, fmt.Errorf("failed to list pipeline versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pipeline: %w", err)
		}
		out = append(out, mapPipelineToDTO(p))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate pipelines: %w", err)
	}
	return out, nil
}
