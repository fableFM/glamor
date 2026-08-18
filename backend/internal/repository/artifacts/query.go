package artifacts

import (
	"context"
	"database/sql"
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

// artifact — приватная модель строки таблицы artifacts.
type artifact struct {
	id        int64
	runID     string
	stageID   sql.NullInt64
	path      string
	kind      string
	createdAt time.Time
}

func mapArtifactToDTO(a artifact) dtorep.Artifact {
	out := dtorep.Artifact{
		ID:        a.id,
		RunID:     a.runID,
		Path:      a.path,
		Kind:      a.kind,
		CreatedAt: a.createdAt,
	}
	if a.stageID.Valid {
		out.StageID = &a.stageID.Int64
	}
	return out
}

func scanArtifact(s scanner) (artifact, error) {
	var a artifact
	err := s.Scan(&a.id, &a.runID, &a.stageID, &a.path, &a.kind, &a.createdAt)
	return a, err
}

const artifactColumns = `id, run_id, stage_id, path, kind, created_at`

func (q *query) CreateArtifact(ctx context.Context, req dtorep.CreateArtifactRequest) (int64, error) {
	res, err := q.conn.ExecContext(ctx,
		`INSERT INTO artifacts (run_id, stage_id, path, kind) VALUES (?, ?, ?, ?)`,
		req.RunID, req.StageID, req.Path, req.Kind)
	if err != nil {
		return 0, fmt.Errorf("failed to insert artifact: %w", store.MapError(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get artifact id: %w", err)
	}
	return id, nil
}

func (q *query) GetArtifactByID(ctx context.Context, id int64) (*dtorep.Artifact, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE id = ?`, id)
	a, err := scanArtifact(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get artifact by id: %w", store.MapError(err))
	}
	dto := mapArtifactToDTO(a)
	return &dto, nil
}

func (q *query) ListArtifactsByRun(ctx context.Context, runID string) ([]dtorep.Artifact, error) {
	return q.listArtifacts(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE run_id = ? ORDER BY id`, runID)
}

func (q *query) ListArtifactsByStage(ctx context.Context, stageID int64) ([]dtorep.Artifact, error) {
	return q.listArtifacts(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE stage_id = ? ORDER BY id`, stageID)
}

func (q *query) listArtifacts(ctx context.Context, sqlStr string, args ...any) ([]dtorep.Artifact, error) {
	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan artifact: %w", err)
		}
		out = append(out, mapArtifactToDTO(a))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate artifacts: %w", err)
	}
	return out, nil
}
