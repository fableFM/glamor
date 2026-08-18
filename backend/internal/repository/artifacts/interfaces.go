// Package artifacts — репозиторий файлов-артефактов этапов (D-13).
package artifacts

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateArtifact(ctx context.Context, req dtorep.CreateArtifactRequest) (int64, error)
	GetArtifactByID(ctx context.Context, id int64) (*dtorep.Artifact, error)
	ListArtifactsByRun(ctx context.Context, runID string) ([]dtorep.Artifact, error)
	ListArtifactsByStage(ctx context.Context, stageID int64) ([]dtorep.Artifact, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}
