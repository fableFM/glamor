// Package runs — репозиторий ранов (D-10: переходы только CAS).
package runs

import (
	"context"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateRun(ctx context.Context, req dtorep.CreateRunRequest) error
	GetRunByID(ctx context.Context, id string) (*dtorep.Run, error)
	GetRunByIdempotencyKey(ctx context.Context, key string) (*dtorep.Run, error)
	ListRuns(ctx context.Context, req dtorep.ListRunsRequest) ([]dtorep.Run, error)
	// TransitionRunState — CAS-переход (D-10): UPDATE ... WHERE id = ? AND state = from.
	// Возвращает false, если состояние уже изменено конкурентным писателем.
	TransitionRunState(ctx context.Context, id string, from, to dtorep.RunState, finishedAt *time.Time) (bool, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}
