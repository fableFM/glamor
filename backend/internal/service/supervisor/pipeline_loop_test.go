package supervisor_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/harness"
	"github.com/fableFM/glamor/internal/repository"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/service/pipeline"
	usecase "github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/internal/service/supervisor"
	"github.com/fableFM/glamor/pkg/uuid"
)

// Спека дефолтного пайплайна с минимальными inline-шаблонами (маркеры
// «Роль: X» для fake-скрипта + плейсхолдеры артефактов).
func pipelineSpecJSON(maxIters int64) string {
	tpl := func(role, extra string) string {
		return fmt.Sprintf("# Роль: %s\nЗадача: {{task}}\nSpec: {{artifact.spec.md}}\n"+
			"Handoff: {{artifact.handoff.md}}\nVerdict: {{artifact.verdict.json}}\n%s", role, extra)
	}
	return fmt.Sprintf(`{"stages":[
		{"key":"planner","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/spec.md","required":true},
		 "questions_path":"{run_dir}/questions.md","gate_after":"plan_approval"},
		{"key":"coder","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/handoff.md","required":true}},
		{"key":"reviewer","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/verdict.json","required":true}},
		{"key":"fixer","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/handoff.md","required":true}}
	],"loop":{"from":"reviewer","to":"fixer","max_iters":%d},"final_gate":"final_review"}`,
		tpl("PLANNER", ""), tpl("CODER", ""),
		tpl("REVIEWER", ""), tpl("FIXER", "VerdictJSON: {{verdict}}"),
		maxIters)
}

type loopFixture struct {
	db        fixtureDB2
	sup       *supervisor.Supervisor
	machine   *runsmachine.Machine
	runsSvc   *usecase.Service
	journal   *events.Journal
	runID     string
	workDir   string
	runsDir   string
	stages    stagesrep.RepositoryWithTX
	runs      runsrep.RepositoryWithTX
	gates     gatesrep.RepositoryWithTX
	artifacts artifactsrep.RepositoryWithTX
	cancel    context.CancelFunc
	done      chan struct{}
}

type fixtureDB2 = interface{ Close() error }

func newLoopFixture(t *testing.T, script string, maxIters int64) *loopFixture {
	t.Helper()

	scriptAbs, err := filepath.Abs(script)
	require.NoError(t, err)

	db := testdb.New(t)
	workDir := t.TempDir()
	runsDir := t.TempDir()
	ctx := context.Background()

	projectID, err := projectsrep.NewRepository(db).CreateProject(ctx, dtorep.CreateProjectRequest{
		Path: workDir, Name: "test", DefaultBranch: "main",
	})
	require.NoError(t, err)
	pipelineID, err := pipelinesrep.NewRepository(db).CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name: "default", Version: 1, SpecJSON: pipelineSpecJSON(maxIters),
	})
	require.NoError(t, err)

	hub := events.NewHub()
	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), hub)

	runsRepo := runsrep.NewRepository(db)
	stagesRepo := stagesrep.NewRepository(db)
	gatesRepo := gatesrep.NewRepository(db)
	pipelinesRepo := pipelinesrep.NewRepository(db)
	projectsRepo := projectsrep.NewRepository(db)
	notesRepo := notesrep.NewRepository(db)
	artifactsRepo := artifactsrep.NewRepository(db)

	machine := runsmachine.NewMachine(runsRepo, stagesRepo, gatesRepo,
		pipelinesRepo, projectsRepo, notesRepo, journal, journal)
	runsSvc := usecase.New(machine, journal, journal,
		runsRepo, stagesRepo, notesRepo, projectsRepo, pipelinesRepo, gatesRepo)

	run, already, err := runsSvc.CreateRun(ctx, usecase.CreateRunParams{
		ProjectID:         projectID,
		PipelineVersionID: pipelineID,
		TaskText:          "полный пайплайн",
		BaseBranch:        "main",
		Depth:             1,
		IdempotencyKey:    uuid.New(),
	})
	require.NoError(t, err)
	require.False(t, already)

	registry := harness.NewRegistry(nil)
	registry.Register(fakeAdapter{script: scriptAbs})

	cfg := supervisor.DefaultConfig(runsDir)
	cfg.PollInterval = 50 * time.Millisecond
	cfg.StallTimeout = 5 * time.Second
	cfg.KillGrace = 200 * time.Millisecond
	cfg.StageTimeout = 30 * time.Second
	cfg.Backoff = []time.Duration{50 * time.Millisecond}
	cfg.MaxAutoResumes = 2
	cfg.MaxParallel = 2

	promptBuilder := pipeline.NewPromptBuilder(t.TempDir())
	sup := supervisor.New(machine, registry, journal, cfg, promptBuilder.Build,
		runsRepo, stagesRepo, projectsRepo, pipelinesRepo, notesRepo, artifactsRepo, gatesRepo)
	sup.SetOnStageSucceeded(pipeline.NewLoopHook(machine, runsDir).OnStageSucceeded)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = sup.Run(runCtx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return &loopFixture{
		db: db, sup: sup, machine: machine, runsSvc: runsSvc, journal: journal,
		runID: run.ID, workDir: workDir, runsDir: runsDir,
		stages: stagesRepo, runs: runsRepo, gates: gatesRepo, artifacts: artifactsRepo,
		cancel: cancel, done: done,
	}
}

// resolveOpenGate резолвит единственный открытый гейт рана действием.
func (f *loopFixture) resolveOpenGate(t *testing.T, kind dtorep.GateKind, action usecase.GateAction) {
	t.Helper()
	ctx := context.Background()
	require.Eventually(t, func() bool {
		gates, err := f.gates.ListOpenGates(ctx, f.runID)
		if err != nil || len(gates) != 1 || gates[0].Kind != kind {
			return false
		}
		_, _, err = f.runsSvc.ResolveGateAPI(ctx, gates[0].ID, action, nil, nil)
		return err == nil
	}, 15*time.Second, 100*time.Millisecond, "gate %s must open and resolve", kind)
}

func (f *loopFixture) runState(t *testing.T) dtorep.RunState {
	t.Helper()
	run, err := f.runs.GetRunByID(context.Background(), f.runID)
	require.NoError(t, err)
	return run.State
}

// Полный прогон дефолтного пайплайна (T-17 acceptance):
// spec → plan_approval → код → verdict changes_required → fix →
// verdict approved → final_review → succeeded. Промпты сохранены.
func TestDefaultPipelineFullRun(t *testing.T) {
	f := newLoopFixture(t, "testdata/pipeline_roles.sh", 4)
	ctx := context.Background()

	// planner → plan_approval
	f.resolveOpenGate(t, dtorep.GateKindPlanApproval, usecase.GateActionApprove)

	// reviewer#1 changes_required → fixer → reviewer#2 approved → final gate
	f.resolveOpenGate(t, dtorep.GateKindFinalReview, usecase.GateActionApprove)

	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond)

	// этапы: planner=1, coder=1, reviewer=2, fixer=1 попыток
	latest := func(key string) *dtorep.Stage {
		st, err := f.stages.GetLatestStage(ctx, f.runID, key)
		require.NoError(t, err)
		return st
	}
	assert.Equal(t, int64(1), latest("planner").Iteration)
	assert.Equal(t, int64(1), latest("coder").Iteration)
	assert.Equal(t, int64(2), latest("reviewer").Iteration)
	fixer := latest("fixer")
	assert.Equal(t, int64(1), fixer.Iteration)
	assert.Equal(t, int64(0), fixer.ResumeCount, "fix-петля — не auto-resume")

	// промпты сохранены как артефакты (T-17 acceptance)
	artifacts, err := f.artifacts.ListArtifactsByRun(ctx, f.runID)
	require.NoError(t, err)
	var promptArtifacts int
	for _, a := range artifacts {
		if a.Kind == "prompt" {
			promptArtifacts++
			data, err := os.ReadFile(a.Path)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "{{", "промпт отрендерен: %s", a.Path)
		}
	}
	assert.GreaterOrEqual(t, promptArtifacts, 5, "по промпту на каждую попытку")
}

// Эскалация петли (D-23): verdict всегда changes_required → после
// max_iters — эскалация-гейт; ответ пользователя → ре-вход fixer.
func TestPipelineLoopEscalation(t *testing.T) {
	f := newLoopFixture(t, "testdata/pipeline_roles_always_changes.sh", 2)

	f.resolveOpenGate(t, dtorep.GateKindPlanApproval, usecase.GateActionApprove)

	// эскалация после 2 итераций
	ctx := context.Background()
	require.Eventually(t, func() bool {
		gates, err := f.gates.ListOpenGates(ctx, f.runID)
		if err != nil || len(gates) != 1 {
			return false
		}
		return gates[0].Kind == dtorep.GateKindEscalation
	}, 30*time.Second, 100*time.Millisecond)

	gates, err := f.gates.ListOpenGates(ctx, f.runID)
	require.NoError(t, err)
	require.Len(t, gates, 1)
	assert.Contains(t, gates[0].Question, "REV-001", "findings в эскалации")

	// ответ пользователя → ре-вход fixer (диалоговый контур T-11)
	answer := "просто напиши файл и заканчивай"
	_, _, err = f.runsSvc.ResolveGateAPI(ctx, gates[0].ID, usecase.GateActionAnswer, &answer, nil)
	require.NoError(t, err)

	fixer, err := f.stages.GetLatestStage(ctx, f.runID, "fixer")
	require.NoError(t, err)
	assert.Equal(t, int64(3), fixer.Iteration, "ответ на эскалацию — новая попытка fixer")
}
