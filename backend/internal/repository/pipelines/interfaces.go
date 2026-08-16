// Package pipelines — репозиторий версий пайплайнов (версии неизменяемы, T-21).
package pipelines

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreatePipeline(ctx context.Context, req dtorep.CreatePipelineRequest) (int64, error)
	GetPipelineByID(ctx context.Context, id int64) (*dtorep.Pipeline, error)
	// GetLatestPipeline — последняя версия пайплайна по имени;
	// projectID = nil — глобальный пайплайн.
	GetLatestPipeline(ctx context.Context, projectID *int64, name string) (*dtorep.Pipeline, error)
	ListPipelines(ctx context.Context, projectID *int64) ([]dtorep.Pipeline, error)
	// ListPipelinesForProject — пайплайны проекта + глобальные (project_id IS NULL).
	ListPipelinesForProject(ctx context.Context, projectID int64) ([]dtorep.Pipeline, error)
	// ListPipelineVersions — все версии пайплайна по имени (projectID = nil — глобальный).
	ListPipelineVersions(ctx context.Context, projectID *int64, name string) ([]dtorep.Pipeline, error)
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}
