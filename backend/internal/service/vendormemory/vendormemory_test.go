package vendormemory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/repository/vendorindex"
	"github.com/fableFM/glamor/internal/service/pipeline"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/internal/service/supervisor"
	"github.com/fableFM/glamor/internal/service/vendormemory"
)

func newService(t *testing.T) (*vendormemory.Service, string, string) {
	t.Helper()
	svc, globalDir, projectPath, _, _ := newServiceFull(t)
	return svc, globalDir, projectPath
}

// newServiceFull — + runID/stageID для FK в artifacts.
func newServiceFull(t *testing.T) (*vendormemory.Service, string, string, string, int64) {
	t.Helper()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	stageID, err := stagesrep.NewRepository(db).CreateStage(context.Background(), dtorep.CreateStageRequest{
		RunID: runID, StageKey: "planner", Iteration: 1, Harness: "kimi",
	})
	require.NoError(t, err)
	globalDir := t.TempDir()
	svc := vendormemory.New(globalDir, vendorindex.NewRepository(db), artifactsrep.NewRepository(db))
	return svc, globalDir, t.TempDir(), runID, stageID
}

// Дельта применяется атомарно: локальная всегда, глобальная при auto,
// с git-коммитом и артефактом vendor_update (T-23).
func TestApplyDeltas(t *testing.T) {
	ctx := context.Background()
	svc, globalDir, projectPath, runID, stageID := newServiceFull(t)

	runDir := t.TempDir()
	updatesDir := filepath.Join(runDir, "vendor-updates")
	require.NoError(t, os.MkdirAll(updatesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(updatesDir, "kafka.md"),
		[]byte("## Ограничения\n- idempotency: producer retries...\n"), 0o644))

	applied, err := svc.ApplyDeltas(ctx, runID, stageID, runDir, projectPath, vendormemory.ScopeAuto)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	assert.Equal(t, "kafka", applied[0].Vendor)
	assert.Equal(t, "both", applied[0].Scope)

	// локальная
	localData, err := os.ReadFile(filepath.Join(projectPath, ".glamor", "vendors", "kafka.md"))
	require.NoError(t, err)
	assert.Contains(t, string(localData), "idempotency")

	// глобальная + git-коммит
	globalData, err := os.ReadFile(filepath.Join(globalDir, "kafka.md"))
	require.NoError(t, err)
	assert.Contains(t, string(globalData), "idempotency")
	_, err = os.Stat(filepath.Join(globalDir, ".git"))
	require.NoError(t, err, "глобальная память — git-репозиторий")

	history, err := svc.History("kafka")
	require.NoError(t, err)
	require.NotEmpty(t, history)
	assert.Contains(t, history[0].Message, "delta kafka")

	// gate-политика: глобальная не трогается
	_, err = svc.History("psql")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(updatesDir, "psql.md"), []byte("psql note"), 0o644))
	applied, err = svc.ApplyDeltas(ctx, runID, stageID, runDir, projectPath, vendormemory.ScopeGate)
	require.NoError(t, err)
	for _, d := range applied {
		assert.Equal(t, "local", d.Scope)
	}
	_, err = os.Stat(filepath.Join(globalDir, "psql.md"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// FTS5: запрос возвращает релевантный кусок, попадающий в промпт (T-23).
func TestFTSInjection(t *testing.T) {
	ctx := context.Background()
	svc, globalDir, _ := newService(t)

	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "kafka.md"), []byte(
		"# Vendor: kafka\n\n## Ограничения и подводные камни\n- idempotency: enable.idempotence=true гарантирует exactly-once в пределах сессии\n"), 0o644))
	require.NoError(t, svc.ReindexAll(ctx, nil))

	hits, err := svc.Search(ctx, `"kafka" OR "idempotency"`, 5)
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Contains(t, hits[0].Snippet, "idempot")

	// выдержка попадает в промпт планировщика ({{vendor_memory}})
	builder := pipeline.NewPromptBuilder(globalDir)
	builder.SetMemorySearcher(svc)
	out, err := builder.Build(ctx, supervisor.LaunchContext{
		Run:   &dtorep.Run{TaskText: "почини kafka idempotency в продьюсере", Depth: 1},
		Stage: &dtorep.Stage{StageKey: "planner", Iteration: 1},
		StageSpec: runsmachine.StageSpec{
			Key:            "planner",
			PromptTemplate: "Память:\n{{vendor_memory}}",
		},
		RunDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Contains(t, out, "kafka.md")
	assert.Contains(t, out, "idempot")
}

// Промоушн локальной записи в глобальную (T-23).
func TestPromoteToGlobal(t *testing.T) {
	ctx := context.Background()
	svc, _, projectPath := newService(t)

	localDir := vendormemory.LocalDir(projectPath)
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "internal-api.md"), []byte("# internal api facts"), 0o644))

	path, err := svc.PromoteToGlobal(ctx, projectPath, "internal-api")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "internal api facts")

	history, err := svc.History("internal-api")
	require.NoError(t, err)
	assert.NotEmpty(t, history)
}

// Ручная правка через WriteFile: атомарно + коммит + переиндексация.
func TestWriteFileManualEdit(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t)

	require.NoError(t, svc.WriteFile(ctx, "global", "", "redis", "# Vendor: redis\n\nretry on OOM"))

	content, err := svc.ReadFile("global", "", "redis")
	require.NoError(t, err)
	assert.Contains(t, content, "retry on OOM")

	hits, err := svc.Search(ctx, `"redis"`, 5)
	require.NoError(t, err)
	assert.NotEmpty(t, hits, "после правки файл переиндексирован")

	// защита от path traversal
	_, err = svc.ReadFile("global", "", "../../../etc/passwd")
	require.Error(t, err)
}
