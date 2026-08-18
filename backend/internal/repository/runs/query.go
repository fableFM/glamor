package runs

import (
	"context"
	"fmt"
	"time"

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

var runColumns = []string{
	"id", "project_id", "pipeline_version_id", "task_text", "base_branch",
	"branch", "state", "depth", "notify_tg", "idempotency_key",
	"created_at", "finished_at", "tg_root_message_id",
}

func scanRun(s scanner) (run, error) {
	var r run
	err := s.Scan(
		&r.id, &r.projectID, &r.pipelineVersionID, &r.taskText, &r.baseBranch,
		&r.branch, &r.state, &r.depth, &r.notifyTG, &r.idempotencyKey,
		&r.createdAt, &r.finishedAt, &r.tgRootMessageID,
	)
	return r, err
}

func (q *query) CreateRun(ctx context.Context, req dtorep.CreateRunRequest) error {
	_, err := q.conn.ExecContext(ctx,
		`INSERT INTO runs (id, project_id, pipeline_version_id, task_text, base_branch,
			branch, state, depth, notify_tg, idempotency_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.ProjectID, req.PipelineVersionID, req.TaskText, req.BaseBranch,
		req.Branch, string(req.State), req.Depth, req.NotifyTG, req.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("failed to insert run: %w", store.MapError(err))
	}
	return nil
}

func (q *query) GetRunByID(ctx context.Context, id string) (*dtorep.Run, error) {
	sb := sqlbuilder.SQLite.NewSelectBuilder()
	sb.Select(runColumns...).From("runs").Where(sb.Equal("id", id))
	sqlStr, args := sb.Build()

	r, err := scanRun(q.conn.QueryRowContext(ctx, sqlStr, args...))
	if err != nil {
		return nil, fmt.Errorf("failed to get run by id: %w", store.MapError(err))
	}
	dto := mapRunToDTO(r)
	return &dto, nil
}

func (q *query) GetRunByIdempotencyKey(ctx context.Context, key string) (*dtorep.Run, error) {
	sb := sqlbuilder.SQLite.NewSelectBuilder()
	sb.Select(runColumns...).From("runs").Where(sb.Equal("idempotency_key", key))
	sqlStr, args := sb.Build()

	r, err := scanRun(q.conn.QueryRowContext(ctx, sqlStr, args...))
	if err != nil {
		return nil, fmt.Errorf("failed to get run by idempotency key: %w", store.MapError(err))
	}
	dto := mapRunToDTO(r)
	return &dto, nil
}

// GetActiveRunByBranch — активный ран на (project_id, branch) (D-33).
// Активные состояния — как в частичном UNIQUE-индексе idx_runs_active_branch.
func (q *query) GetActiveRunByBranch(ctx context.Context, projectID int64, branch string) (*dtorep.Run, error) {
	sb := sqlbuilder.SQLite.NewSelectBuilder()
	sb.Select(runColumns...).From("runs").Where(
		sb.Equal("project_id", projectID),
		sb.Equal("branch", branch),
		sb.In("state", string(dtorep.RunStateDraft), string(dtorep.RunStateRunning),
			string(dtorep.RunStateWaitingGate)),
	)
	sqlStr, args := sb.Build()

	r, err := scanRun(q.conn.QueryRowContext(ctx, sqlStr, args...))
	if err != nil {
		return nil, fmt.Errorf("failed to get active run by branch: %w", store.MapError(err))
	}
	dto := mapRunToDTO(r)
	return &dto, nil
}

func (q *query) ListRuns(ctx context.Context, req dtorep.ListRunsRequest) ([]dtorep.Run, error) {
	sb := sqlbuilder.SQLite.NewSelectBuilder()
	sb.Select(runColumns...).From("runs")

	if req.ProjectID != nil {
		sb.Where(sb.Equal("project_id", *req.ProjectID))
	}
	if req.PipelineVersionID != nil {
		sb.Where(sb.Equal("pipeline_version_id", *req.PipelineVersionID))
	}
	if req.Since != nil {
		sb.Where(sb.GreaterEqualThan("created_at", *req.Since))
	}
	if len(req.States) > 0 {
		states := make([]any, 0, len(req.States))
		for _, s := range req.States {
			states = append(states, string(s))
		}
		sb.Where(sb.In("state", states...))
	}
	sb.OrderByDesc("created_at")
	if req.Limit > 0 {
		sb.Limit(req.Limit)
	}

	sqlStr, args := sb.Build()
	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan run: %w", err)
		}
		out = append(out, mapRunToDTO(r))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate runs: %w", err)
	}
	return out, nil
}

// SetTgRootMessageID — CAS-установка корневого TG-сообщения рана (T-19):
// UPDATE ... WHERE tg_root_message_id IS NULL. Возвращает false, если корень
// уже выставлен (конкурентный отправитель — читатель использует существующий).
func (q *query) SetTgRootMessageID(ctx context.Context, id string, messageID int64) (bool, error) {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE runs SET tg_root_message_id = ? WHERE id = ? AND tg_root_message_id IS NULL`,
		messageID, id)
	if err != nil {
		return false, fmt.Errorf("failed to set tg root message id: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get affected rows: %w", err)
	}
	return affected == 1, nil
}

// GetRunByTgRootMessageID — ран по id корневого TG-сообщения (reply на
// «тред» рана → queue note / stop, T-19). Нет рана → cstmerrors.ErrNotFound.
func (q *query) GetRunByTgRootMessageID(ctx context.Context, messageID int64) (*dtorep.Run, error) {
	sb := sqlbuilder.SQLite.NewSelectBuilder()
	sb.Select(runColumns...).From("runs").Where(sb.Equal("tg_root_message_id", messageID))
	sqlStr, args := sb.Build()

	r, err := scanRun(q.conn.QueryRowContext(ctx, sqlStr, args...))
	if err != nil {
		return nil, fmt.Errorf("failed to get run by tg root message id: %w", store.MapError(err))
	}
	dto := mapRunToDTO(r)
	return &dto, nil
}

func (q *query) TransitionRunState(ctx context.Context, id string, from, to dtorep.RunState, finishedAt *time.Time) (bool, error) {
	res, err := q.conn.ExecContext(ctx,
		`UPDATE runs SET state = ?, finished_at = COALESCE(?, finished_at)
		 WHERE id = ? AND state = ?`,
		string(to), finishedAt, id, string(from))
	if err != nil {
		return false, fmt.Errorf("failed to transition run state: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get affected rows: %w", err)
	}
	return affected == 1, nil
}
