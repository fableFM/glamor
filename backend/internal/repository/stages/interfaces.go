// Package stages — репозиторий стадий ранов (D-10: переходы только CAS).
package stages

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateStage(ctx context.Context, req dtorep.CreateStageRequest) (int64, error)
	GetStageByID(ctx context.Context, id int64) (*dtorep.Stage, error)
	// GetLatestStage — последняя попытка этапа (max iteration) для (run_id, stage_key).
	GetLatestStage(ctx context.Context, runID, stageKey string) (*dtorep.Stage, error)
	// GetStageByIteration — конкретная попытка этапа (для session_id предыдущей).
	GetStageByIteration(ctx context.Context, runID, stageKey string, iteration int64) (*dtorep.Stage, error)
	ListStagesByRun(ctx context.Context, runID string) ([]dtorep.Stage, error)
	// ListStagesByState — например все running (startup recovery, D-15).
	ListStagesByState(ctx context.Context, state dtorep.StageState) ([]dtorep.Stage, error)
	// TransitionStageState — CAS-переход (D-10): UPDATE ... WHERE id = ? AND state = from.
	// Возвращает false, если состояние уже изменено конкурентным писателем.
	TransitionStageState(ctx context.Context, id int64, from, to dtorep.StageState, fields dtorep.StageTransitionFields) (bool, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}
