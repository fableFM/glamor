// Package projects — репозиторий проектов (D-80).
package projects

import (
	"context"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/pkg/commontx"
)

type Queries interface {
	CreateProject(ctx context.Context, req dtorep.CreateProjectRequest) (int64, error)
	GetProjectByID(ctx context.Context, id int64) (*dtorep.Project, error)
	GetProjectByPath(ctx context.Context, path string) (*dtorep.Project, error)
	ListProjects(ctx context.Context) ([]dtorep.Project, error)
	UpdateProject(ctx context.Context, id int64, req dtorep.PatchProjectRequest) error
	// DeleteProjectCascade — удаление проекта со всей историей (необратимо).
	DeleteProjectCascade(ctx context.Context, id int64) error
}

type RepositoryWithTX interface {
	OpenTx(ctx context.Context) (Tx, error)
	Queries
}

type Tx interface {
	Queries
	commontx.Tx
}
