package supervisor_test

import (
	"context"
	"database/sql"
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

// Спеки для тестов контура уроков (T-30). Промпт-шаблоны минимальны
// (маркеры «Роль: X» для fake-скрипта + плейсхолдеры артефактов).
func lessonsSpecJSON(withDistill bool, lessonsFlag string) string {
	tpl := func(role string) string {
		return fmt.Sprintf("# Роль: %s\nЗадача: {{task}}\nSpec: {{artifact.spec.md}}\n"+
			"Handoff: {{artifact.handoff.md}}\nVerdict: {{artifact.verdict.json}}\n", role)
	}
	stages := fmt.Sprintf(`{"key":"planner","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/spec.md","required":true},
		 "questions_path":"{run_dir}/questions.md","gate_after":"plan_approval"},
		{"key":"coder","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/handoff.md","required":true}},
		{"key":"reviewer","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/verdict.json","required":true}},
		{"key":"fixer","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/handoff.md","required":true}}`,
		tpl("PLANNER"), tpl("CODER"), tpl("REVIEWER"), tpl("FIXER"))
	if withDistill {
		stages += fmt.Sprintf(`,
		{"key":"distill","harness":"fake","prompt_template":%q,
		 "artifact":{"path":"{run_dir}/lessons.md","required":true},
		 "gate_after":"lesson_review"}`, tpl("DISTILL")+"{{behavior_trace}}\n")
	}
	spec := fmt.Sprintf(`{"stages":[%s],
		"loop":{"from":"reviewer","to":"fixer","max_iters":2},
		"final_gate":"final_review"`, stages)
	if lessonsFlag != "" {
		spec += fmt.Sprintf(`,"lessons":%q`, lessonsFlag)
	}
	return spec + "}"
}

type lessonsFixture struct {
	db      *sql.DB
	sup     *supervisor.Supervisor
	machine *runsmachine.Machine
	runsSvc *usecase.Service
	runID   string
	runsDir string
	stages  stagesrep.RepositoryWithTX
	runs    runsrep.RepositoryWithTX
	gates   gatesrep.RepositoryWithTX
	events  eventsrep.RepositoryWithTX
	cancel  context.CancelFunc
	done    chan struct{}

	// deps для рестарта supervisor (M2: recovery терминального distill)
	scriptAbs string
	journal   *events.Journal
	projects  projectsrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
	artifacts artifactsrep.RepositoryWithTX
}

// newLessonsFixture — supervisor с реальной стейт-машиной; аугментер
// встроенного distill подключён всегда (как в main, T-30).
func newLessonsFixture(t *testing.T, script, specJSON string) *lessonsFixture {
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
		Name: "default", Version: 1, SpecJSON: specJSON,
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
	machine.SetSpecAugmenter(pipeline.BuiltinDistillAugmenter(
		pipeline.Triplets{Harness: "fake", Model: "fake", Effort: "low"}))
	runsSvc := usecase.New(machine, journal, journal,
		runsRepo, stagesRepo, notesRepo, projectsRepo, pipelinesRepo, gatesRepo)

	run, already, err := runsSvc.CreateRun(ctx, usecase.CreateRunParams{
		ProjectID:         projectID,
		PipelineVersionID: pipelineID,
		TaskText:          "контур уроков",
		BaseBranch:        "main",
		Depth:             1,
		IdempotencyKey:    uuid.New(),
	})
	require.NoError(t, err)
	require.False(t, already)

	f := &lessonsFixture{
		db: db, machine: machine, runsSvc: runsSvc,
		runID: run.ID, runsDir: runsDir,
		stages: stagesRepo, runs: runsRepo, gates: gatesRepo,
		events:    eventsrep.NewRepository(db),
		scriptAbs: scriptAbs, journal: journal,
		projects: projectsRepo, pipelines: pipelinesRepo,
		notes: notesRepo, artifacts: artifactsRepo,
	}
	f.start(t, defaultTestCfg(runsDir))
	return f
}

// defaultTestCfg — конфиг контура для тестов (короткие интервалы).
func defaultTestCfg(runsDir string) supervisor.Config {
	cfg := supervisor.DefaultConfig(runsDir)
	cfg.PollInterval = 50 * time.Millisecond
	cfg.StallTimeout = 5 * time.Second
	cfg.KillGrace = 200 * time.Millisecond
	cfg.StageTimeout = 30 * time.Second
	cfg.Backoff = []time.Duration{50 * time.Millisecond}
	cfg.MaxAutoResumes = 2
	cfg.MaxParallel = 2
	return cfg
}

// start поднимает supervisor на deps фикстуры (начальный запуск и рестарт).
func (f *lessonsFixture) start(t *testing.T, cfg supervisor.Config) {
	t.Helper()
	registry := harness.NewRegistry(nil)
	registry.Register(fakeAdapter{script: f.scriptAbs})

	promptBuilder := pipeline.NewPromptBuilder(t.TempDir())
	sup := supervisor.New(f.machine, registry, f.journal, cfg, promptBuilder.Build,
		f.runs, f.stages, f.projects, f.pipelines, f.notes, f.artifacts, f.gates)
	sup.SetOnStageSucceeded(pipeline.NewLoopHook(f.machine, f.runsDir).OnStageSucceeded)
	f.sup = sup

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = sup.Run(runCtx)
		close(done)
	}()
	f.cancel, f.done = cancel, done
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// stop останавливает supervisor (симуляция падения демона; M2).
func (f *lessonsFixture) stop() {
	f.cancel()
	<-f.done
}

// resolveOpenGateKind резолвит открытый гейт заданного вида.
func (f *lessonsFixture) resolveOpenGateKind(t *testing.T, kind dtorep.GateKind) dtorep.Gate {
	t.Helper()
	ctx := context.Background()
	var resolved dtorep.Gate
	require.Eventually(t, func() bool {
		gates, err := f.gates.ListOpenGates(ctx, f.runID)
		if err != nil || len(gates) != 1 || gates[0].Kind != kind {
			return false
		}
		g, _, err := f.runsSvc.ResolveGateAPI(ctx, gates[0].ID, usecase.GateActionApprove, nil, nil)
		if err != nil {
			return false
		}
		resolved = *g
		return true
	}, 30*time.Second, 100*time.Millisecond, "gate %s must open and resolve", kind)
	return resolved
}

func (f *lessonsFixture) runState(t *testing.T) dtorep.RunState {
	t.Helper()
	run, err := f.runs.GetRunByID(context.Background(), f.runID)
	require.NoError(t, err)
	return run.State
}

func (f *lessonsFixture) eventKinds(t *testing.T) []string {
	t.Helper()
	events, err := f.events.ReplayEvents(context.Background(), f.runID, 0, 10000)
	require.NoError(t, err)
	kinds := make([]string, 0, len(events))
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

// Гарантия distill (T-30): в спеке нет distill-этапа, lessons:on (дефолт) →
// встроенный distill дописывается машиной и выполняется: трейс собран
// (run_facts.json), гейт lesson_review открыт с origin=builtin.
func TestBuiltinDistillGuarantee(t *testing.T) {
	f := newLessonsFixture(t, "testdata/pipeline_distill.sh", lessonsSpecJSON(false, ""))
	ctx := context.Background()

	f.resolveOpenGateKind(t, dtorep.GateKindPlanApproval)

	// гейт lesson_review открывается (distill написал карточку)
	lessonGate := f.resolveOpenGateKind(t, dtorep.GateKindLessonReview)
	assert.Contains(t, lessonGate.ContextJSON, `"origin":"builtin"`,
		"встроенный distill помечен origin=builtin")

	f.resolveOpenGateKind(t, dtorep.GateKindFinalReview)

	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond)

	// distill-этап выполнен, трейс собран (чинит P0: вход distill подключён)
	distill, err := f.stages.GetLatestStage(ctx, f.runID, "distill")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateSucceeded, distill.State)

	facts, err := os.ReadFile(filepath.Join(f.runsDir, f.runID, "run_facts.json"))
	require.NoError(t, err, "run_facts.json — артефакт трейса")
	assert.Contains(t, string(facts), `"state"`)

	// промпт distill содержит отрендеренный трейс
	promptData, err := os.ReadFile(filepath.Join(f.runsDir, f.runID, "prompt-distill-1.md"))
	require.NoError(t, err)
	assert.Contains(t, string(promptData), "Исход рана", "{{behavior_trace}} отрендерен трейсом")
	assert.NotContains(t, string(promptData), "{{")
}

// Мастер-выключатель (T-30): lessons:off → distill-этап пропускается с
// событием skip, гейт lesson_review не открывается, даже если нода есть в YAML.
func TestLessonsOffSkipsDistill(t *testing.T) {
	f := newLessonsFixture(t, "testdata/pipeline_distill.sh", lessonsSpecJSON(true, "off"))
	ctx := context.Background()

	f.resolveOpenGateKind(t, dtorep.GateKindPlanApproval)
	f.resolveOpenGateKind(t, dtorep.GateKindFinalReview)

	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond)

	// distill пропущен (строка skipped + событие с причиной)
	distill, err := f.stages.GetLatestStage(ctx, f.runID, "distill")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateSkipped, distill.State)

	var skipEvent bool
	events, err := f.events.ReplayEvents(ctx, f.runID, 0, 10000)
	require.NoError(t, err)
	for _, ev := range events {
		if ev.Kind == "stage.skipped" {
			skipEvent = true
			assert.Contains(t, ev.PayloadJSON, "lessons_off")
		}
		assert.NotEqual(t, "gate.opened", ev.Kind+":"+string(dtorep.GateKindLessonReview))
	}
	assert.True(t, skipEvent, "событие skip distill-этапа в журнале")

	// гейт lesson_review не открывался
	gates, err := f.gates.ListGatesByRun(ctx, f.runID)
	require.NoError(t, err)
	for _, g := range gates {
		assert.NotEqual(t, dtorep.GateKindLessonReview, g.Kind)
	}

	// трейс не собирался
	_, err = os.Stat(filepath.Join(f.runsDir, f.runID, "run_facts.json"))
	assert.True(t, os.IsNotExist(err), "lessons:off — трейс не собирается")
}

// Терминальный distill (T-30): ран failed (coder без артефакта) → distill
// прогоняется out-of-band с полным трейсом (исход failed), гейт
// lesson_review открывается, не переводя ран в waiting_gate.
func TestTerminalDistillOnFailure(t *testing.T) {
	f := newLessonsFixture(t, "testdata/pipeline_fail_coder.sh", lessonsSpecJSON(false, ""))
	ctx := context.Background()

	f.resolveOpenGateKind(t, dtorep.GateKindPlanApproval)

	// coder падает → ран failed
	require.Eventually(t, func() bool {
		return f.runState(t) == dtorep.RunStateFailed
	}, 15*time.Second, 100*time.Millisecond)

	// терминальный distill (после grace): гейт lesson_review на failed-ране
	var lessonGate dtorep.Gate
	require.Eventually(t, func() bool {
		gates, err := f.gates.ListOpenGates(ctx, f.runID)
		if err != nil || len(gates) != 1 || gates[0].Kind != dtorep.GateKindLessonReview {
			return false
		}
		lessonGate = gates[0]
		return true
	}, 30*time.Second, 100*time.Millisecond, "out-of-band lesson_review gate on failed run")

	assert.Equal(t, dtorep.RunStateFailed, f.runState(t),
		"терминальный гейт не переводит ран в waiting_gate")

	// трейс с исходом провала
	facts, err := os.ReadFile(filepath.Join(f.runsDir, f.runID, "run_facts.json"))
	require.NoError(t, err)
	assert.Contains(t, string(facts), `"state": "failed"`)

	// событие терминального distill в журнале
	var terminalEvent bool
	for _, kind := range f.eventKinds(t) {
		if kind == "run.terminal_distill" {
			terminalEvent = true
		}
	}
	assert.True(t, terminalEvent)

	// резолв гейта работает на failed-ране; ран остаётся failed
	g, _, err := f.runsSvc.ResolveGateAPI(ctx, lessonGate.ID, usecase.GateActionApprove, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, dtorep.GateStateApproved, g.State)
	assert.Equal(t, dtorep.RunStateFailed, f.runState(t))

	// повторный терминальный distill не запускается (строка этапа = маркер)
	distill, err := f.stages.GetLatestStage(ctx, f.runID, "distill")
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateSucceeded, distill.State)
	assert.Equal(t, int64(1), distill.Iteration)
}
