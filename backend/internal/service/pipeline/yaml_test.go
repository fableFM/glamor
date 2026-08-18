package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/service/pipeline"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

func newSvc(t *testing.T) (*pipeline.Service, pipelinesrep.RepositoryWithTX) {
	t.Helper()
	db := testdb.New(t)
	repo := pipelinesrep.NewRepository(db)
	return pipeline.NewService(repo), repo
}

// Round-trip: экспорт → импорт даёт эквивалентный spec (T-21 acceptance).
func TestYAMLRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, repo := newSvc(t)

	require.NoError(t, svc.Seed(ctx))
	seeded, err := repo.GetLatestPipeline(ctx, nil, "default")
	require.NoError(t, err)

	yamlData, err := svc.ExportYAML(ctx, seeded.ID)
	require.NoError(t, err)
	assert.Contains(t, yamlData, "name: default")
	assert.Contains(t, yamlData, "prompt_template: |")

	imported, err := svc.ImportYAML(ctx, yamlData, pipeline.ImportNew)
	require.NoError(t, err)
	assert.Equal(t, "default-2", imported.Name, "new: уникальное имя с суффиксом")

	// глубокое сравнение спек
	specA, err := runsmachine.ParseSpec(seeded.SpecJSON)
	require.NoError(t, err)
	specB, err := runsmachine.ParseSpec(imported.SpecJSON)
	require.NoError(t, err)
	assert.Equal(t, specA, specB)
}

// on_conflict: fail → ошибка; version → новая версия с parent_version_id.
func TestImportConflicts(t *testing.T) {
	ctx := context.Background()
	svc, repo := newSvc(t)
	require.NoError(t, svc.Seed(ctx))

	seeded, err := repo.GetLatestPipeline(ctx, nil, "default")
	require.NoError(t, err)
	yamlData, err := svc.ExportYAML(ctx, seeded.ID)
	require.NoError(t, err)

	_, err = svc.ImportYAML(ctx, yamlData, pipeline.ImportFail)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	v2, err := svc.ImportYAML(ctx, yamlData, pipeline.ImportVersion)
	require.NoError(t, err)
	assert.Equal(t, "default", v2.Name)
	assert.Equal(t, int64(2), v2.Version)
	require.NotNil(t, v2.ParentVersionID)
	assert.Equal(t, seeded.ID, *v2.ParentVersionID)

	// спека импортированной версии не редактирует первую (неизменяемость)
	v1again, err := repo.GetPipelineByID(ctx, seeded.ID)
	require.NoError(t, err)
	assert.Equal(t, seeded.SpecJSON, v1again.SpecJSON)
}

// Битый/невалидный YAML — понятные ошибки (T-21 acceptance).
func TestImportInvalid(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	_, err := svc.ImportYAML(ctx, "not: [valid", pipeline.ImportNew)
	require.Error(t, err)

	_, err = svc.ImportYAML(ctx, "stages: []", pipeline.ImportNew)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is empty")

	// неизвестный плейсхолдер
	bad := `name: x
version: 1
stages:
  - key: a
    prompt_template: "{{nosuch}}"
`
	_, err = svc.ImportYAML(ctx, bad, pipeline.ImportNew)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown placeholder")

	// loop на несуществующий этап
	bad2 := `name: x
version: 1
stages:
  - key: a
loop: {from: ghost, to: a, max_iters: 2}
`
	_, err = svc.ImportYAML(ctx, bad2, pipeline.ImportNew)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such stage")
}

// default.yaml — канонический источник seed'а, синхронизирован с промптами
// (регенерация: GEN_YAML=1 go test -run TestGenerateDefaultYAML).
func TestDefaultYAML_UpToDate(t *testing.T) {
	spec, err := pipeline.DefaultSpec(pipeline.DefaultOverrides())
	require.NoError(t, err)

	// seed из embedded default.yaml даёт спеку, совпадающую с DefaultSpec
	db := testdb.New(t)
	repo := pipelinesrep.NewRepository(db)
	svc := pipeline.NewService(repo)
	require.NoError(t, svc.Seed(context.Background()))
	seeded, err := repo.GetLatestPipeline(context.Background(), nil, "default")
	require.NoError(t, err)

	specFromYAML := seeded.SpecJSON
	// default.yaml == спека из DefaultSpec
	assert.JSONEq(t, spec, specFromYAML, "default.yaml рассинхронизирован с DefaultSpec/prompts — регенерируй")
}
