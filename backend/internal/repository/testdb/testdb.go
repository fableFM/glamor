// Package testdb — хелпер для тестов store-слоя: временная SQLite-БД
// с применёнными миграциями и integrity_check в cleanup.
package testdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/repository"
	"github.com/fableFM/glamor/internal/repository/pipelines"
	"github.com/fableFM/glamor/internal/repository/projects"
	"github.com/fableFM/glamor/internal/repository/runs"
	// регистрация goose-миграций
	_ "github.com/fableFM/glamor/migrations"
	"github.com/fableFM/glamor/pkg/uuid"
)

// New открывает временную БД с миграциями; в cleanup — integrity_check.
func New(t *testing.T) *sql.DB {
	t.Helper()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := repository.Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, repository.Migrate(ctx, db))

	t.Cleanup(func() {
		require.NoError(t, repository.IntegrityCheck(ctx, db))
		require.NoError(t, db.Close())
	})

	return db
}

// SeedRun создаёт project + pipeline + run и возвращает id рана.
func SeedRun(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()

	projectID, err := projects.NewRepository(db).CreateProject(ctx, dtorep.CreateProjectRequest{
		Path:          "/tmp/" + uuid.New(),
		Name:          "test-project",
		DefaultBranch: "main",
	})
	require.NoError(t, err)

	spec := `{"stages":[{"key":"plan"},{"key":"code"}]}`
	pipelineID, err := pipelines.NewRepository(db).CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name:     "default",
		Version:  1,
		SpecJSON: spec,
	})
	require.NoError(t, err)

	runID := uuid.New()
	err = runs.NewRepository(db).CreateRun(ctx, dtorep.CreateRunRequest{
		ID:                runID,
		ProjectID:         projectID,
		PipelineVersionID: pipelineID,
		TaskText:          "test task",
		BaseBranch:        "main",
		Branch:            "glamor/test",
		State:             dtorep.RunStateDraft,
		IdempotencyKey:    uuid.New(),
	})
	require.NoError(t, err)

	return runID
}
