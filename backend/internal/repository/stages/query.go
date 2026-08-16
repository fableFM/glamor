package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	store "github.com/fableFM/glamor/internal/repository"
)

type query struct {
	conn store.Conn
}

type scanner interface {
	Scan(dest ...any) error
}

const stageColumns = `id, run_id, stage_key, iteration, state, harness, session_id,
	pid, exit_code, stop_requested_by, resume_count, started_at, finished_at,
	tokens_in, tokens_out, error`

func scanStage(s scanner) (stage, error) {
	var st stage
	err := s.Scan(
		&st.id, &st.runID, &st.stageKey, &st.iteration, &st.state, &st.harness,
		&st.sessionID, &st.pid, &st.exitCode, &st.stopRequestedBy, &st.resumeCount,
		&st.startedAt, &st.finishedAt, &st.tokensIn, &st.tokensOut, &st.err,
	)
	return st, err
}

func (q *query) CreateStage(ctx context.Context, req dtorep.CreateStageRequest) (int64, error) {
	res, err := q.conn.ExecContext(ctx,
		`INSERT INTO run_stages (run_id, stage_key, iteration, state, harness, resume_count)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		req.RunID, req.StageKey, req.Iteration, string(dtorep.StageStatePending), req.Harness, req.ResumeCount)
	if err != nil {
		return 0, fmt.Errorf("failed to insert stage: %w", store.MapError(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get stage id: %w", err)
	}
	return id, nil
}

func (q *query) GetStageByID(ctx context.Context, id int64) (*dtorep.Stage, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+stageColumns+` FROM run_stages WHERE id = ?`, id)
	st, err := scanStage(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get stage by id: %w", store.MapError(err))
	}
	dto := mapStageToDTO(st)
	return &dto, nil
}

func (q *query) GetLatestStage(ctx context.Context, runID, stageKey string) (*dtorep.Stage, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+stageColumns+` FROM run_stages
		 WHERE run_id = ? AND stage_key = ?
		 ORDER BY iteration DESC LIMIT 1`, runID, stageKey)
	st, err := scanStage(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest stage: %w", store.MapError(err))
	}
	dto := mapStageToDTO(st)
	return &dto, nil
}

func (q *query) GetStageByIteration(ctx context.Context, runID, stageKey string, iteration int64) (*dtorep.Stage, error) {
	row := q.conn.QueryRowContext(ctx,
		`SELECT `+stageColumns+` FROM run_stages
		 WHERE run_id = ? AND stage_key = ? AND iteration = ?`, runID, stageKey, iteration)
	st, err := scanStage(row)
	if err != nil {
		return nil, fmt.Errorf("failed to get stage by iteration: %w", store.MapError(err))
	}
	dto := mapStageToDTO(st)
	return &dto, nil
}

func (q *query) ListStagesByRun(ctx context.Context, runID string) ([]dtorep.Stage, error) {
	return q.listStages(ctx, `SELECT `+stageColumns+` FROM run_stages WHERE run_id = ? ORDER BY id`, runID)
}

func (q *query) ListStagesByState(ctx context.Context, state dtorep.StageState) ([]dtorep.Stage, error) {
	return q.listStages(ctx, `SELECT `+stageColumns+` FROM run_stages WHERE state = ? ORDER BY id`, string(state))
}

func (q *query) listStages(ctx context.Context, sqlStr string, args ...any) ([]dtorep.Stage, error) {
	rows, err := q.conn.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list stages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []dtorep.Stage
	for rows.Next() {
		st, err := scanStage(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stage: %w", err)
		}
		out = append(out, mapStageToDTO(st))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate stages: %w", err)
	}
	return out, nil
}

func (q *query) TransitionStageState(ctx context.Context, id int64, from, to dtorep.StageState, fields dtorep.StageTransitionFields) (bool, error) {
	sets := []string{"state = ?"}
	args := []any{string(to)}

	if fields.SessionID != nil {
		sets = append(sets, "session_id = ?")
		args = append(args, *fields.SessionID)
	}
	if fields.PID != nil {
		sets = append(sets, "pid = ?")
		args = append(args, *fields.PID)
	}
	if fields.ExitCode != nil {
		sets = append(sets, "exit_code = ?")
		args = append(args, *fields.ExitCode)
	}
	if fields.StopRequestedBy != nil {
		sets = append(sets, "stop_requested_by = ?")
		args = append(args, *fields.StopRequestedBy)
	}
	if fields.StartedAt != nil {
		sets = append(sets, "started_at = ?")
		args = append(args, *fields.StartedAt)
	}
	if fields.FinishedAt != nil {
		sets = append(sets, "finished_at = ?")
		args = append(args, *fields.FinishedAt)
	}
	if fields.Error != nil {
		sets = append(sets, "error = ?")
		args = append(args, *fields.Error)
	}
	if fields.TokensIn != nil {
		sets = append(sets, "tokens_in = ?")
		args = append(args, *fields.TokensIn)
	}
	if fields.TokensOut != nil {
		sets = append(sets, "tokens_out = ?")
		args = append(args, *fields.TokensOut)
	}

	sqlStr := fmt.Sprintf(`UPDATE run_stages SET %s WHERE id = ? AND state = ?`, strings.Join(sets, ", "))
	args = append(args, id, string(from))

	res, err := q.conn.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return false, fmt.Errorf("failed to transition stage state: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get affected rows: %w", err)
	}
	return affected == 1, nil
}
