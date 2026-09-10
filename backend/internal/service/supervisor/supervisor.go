// Package supervisor — процессный контур этапа (D-13/14/15/16, T-09):
// запуск harness-процессов, watchdog, классификация завершения,
// auto-resume, пул процессов (D-33). Состояние — только в БД (ADR-001);
// supervisor лишь двигает его через Machine и читает на каждом тике.
package supervisor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/harness"
	artifactsrep "github.com/fableFM/glamor/internal/repository/artifacts"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	runsmachine "github.com/fableFM/glamor/internal/service/runsmachine"
)

const componentName = "service/supervisor"

// Config — параметры контура (дефолты — DefaultConfig; из конфига демона, T-12).
type Config struct {
	StallTimeout       time.Duration // нет событий стрима при живом процессе → kill (дефолт 120s)
	KillGrace          time.Duration // SIGTERM → SIGKILL (дефолт 10s)
	StageTimeout       time.Duration // общий лимит на этап (дефолт 60m)
	MaxAutoResumes     int64         // лимит auto-resume (дефолт 3, D-14)
	Backoff            []time.Duration
	RetriableThreshold int           // K подряд retriable-ошибок стрима → раннее прерывание (дефолт 3)
	MaxParallel        int           // пул процессов (D-33, дефолт 4)
	PollInterval       time.Duration // тик контура (дефолт 1s)
	RunsDir            string        // каталог логов ранов (receipts, D-13)
}

func DefaultConfig(runsDir string) Config {
	return Config{
		StallTimeout:       120 * time.Second,
		KillGrace:          10 * time.Second,
		StageTimeout:       60 * time.Minute,
		MaxAutoResumes:     runsmachine.MaxResumeCount,
		Backoff:            []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute},
		RetriableThreshold: 3,
		MaxParallel:        4,
		PollInterval:       time.Second,
		RunsDir:            runsDir,
	}
}

// LaunchContext — контекст сборки промпта попытки этапа.
type LaunchContext struct {
	Run           *dtorep.Run
	Stage         *dtorep.Stage // новая попытка (pending)
	StageSpec     runsmachine.StageSpec
	PrevSessionID string   // session_id предыдущей попытки (resume, D-16)
	IsResume      bool     // auto-resume после interrupted
	SteerMessages []string // сообщения Interrupt&Steer / queue notes (T-11)
	// RunDir — каталог артефактов рана (~/.glamor/runs/<id>, T-17):
	// spec.md/questions.md/verdict.json/handoff.md/prompt-*.md.
	RunDir string
	// ProjectPath — чекаут проекта (vendor-память локальная, T-17).
	ProjectPath string
	// MaxIterations — лимит fix-петли из спеки ({{max_iterations}}, T-17).
	MaxIterations int64
	// LessonSignals — сигналы для distill-этапа (T-29): ответы на гейтах,
	// queue notes, комментарии ({{gate_answers}} — fallback-плейсхолдер
	// для пользовательских пайплайнов; основной вход distill — трейс).
	LessonSignals string
	// BehaviorTrace — трейс поведения рана для distill-этапа (T-30):
	// заполняется supervisor'ом перед distill ({{behavior_trace}}).
	BehaviorTrace string
}

// PromptBuilder собирает промпт этапа (полные промпты дефолтного пайплайна —
// T-17; здесь хук + простой дефолт).
type PromptBuilder func(ctx context.Context, lc LaunchContext) (string, error)

// PreStageHook — проверка перед стартом этапа (T-10: branch_mismatch).
// Возвращает ошибку → этап не стартует, событие run.branch_mismatch + гейт.
type PreStageHook func(ctx context.Context, run *dtorep.Run, projectPath string) error

// PostRunHook — подготовка после старта рана (T-10: PrepareBranch).
type PostRunHook func(ctx context.Context, run *dtorep.Run, projectPath string) error

// OnStageSucceeded — хук после успешного этапа (T-11: артефакт-гейты,
// T-17: fix-петля). nil — линейное продвижение. spec — спека пайплайна рана.
type OnStageSucceeded func(ctx context.Context, run *dtorep.Run, stage *dtorep.Stage, spec runsmachine.Spec) error

// Supervisor — контур этапов. Один экземпляр на демон.
type Supervisor struct {
	machine   *runsmachine.Machine
	registry  *harness.Registry
	journal   *events.Journal
	cfgMu     sync.RWMutex
	cfg       Config
	prompt    PromptBuilder
	preStage  PreStageHook
	postRun   PostRunHook
	onSuccess OnStageSucceeded

	runs      runsrep.RepositoryWithTX
	stages    stagesrep.RepositoryWithTX
	projects  projectsrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
	artifacts artifactsrep.RepositoryWithTX
	gates     gatesrep.RepositoryWithTX

	lessonsHooks LessonsHooks // контур уроков (T-30); nil — только трейс/distill

	startedAt time.Time // подъём демона: отсечка истории терминального distill (T-30)

	sem chan struct{} // пул процессов (D-33)

	mu                     sync.Mutex
	procs                  map[int64]*stageProc // stageID → живой процесс
	resumeAfter            map[int64]time.Time  // interrupted stageID → когда резюмить (backoff)
	queued                 map[int64]bool       // stage pending, ждёт семафор (событие stage.queued отправлено)
	terminalDistillHandled map[string]bool      // runID → терминальный distill уже запущен/не нужен (T-30)
	draining               bool                 // graceful shutdown (T-12): новые этапы не стартуем
	completions            chan stageResult     // завершившиеся процессы → классификация
	stopCh                 chan struct{}
	wg                     sync.WaitGroup
}

func New(machine *runsmachine.Machine, registry *harness.Registry, journal *events.Journal,
	cfg Config, prompt PromptBuilder,
	runs runsrep.RepositoryWithTX, stages stagesrep.RepositoryWithTX,
	projects projectsrep.RepositoryWithTX, pipelines pipelinesrep.RepositoryWithTX,
	notes notesrep.RepositoryWithTX, artifacts artifactsrep.RepositoryWithTX,
	gates gatesrep.RepositoryWithTX,
) *Supervisor {
	if prompt == nil {
		prompt = DefaultPromptBuilder
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 1
	}
	return &Supervisor{
		machine:                machine,
		registry:               registry,
		journal:                journal,
		cfg:                    cfg,
		prompt:                 prompt,
		runs:                   runs,
		stages:                 stages,
		projects:               projects,
		pipelines:              pipelines,
		notes:                  notes,
		artifacts:              artifacts,
		gates:                  gates,
		sem:                    make(chan struct{}, cfg.MaxParallel),
		procs:                  map[int64]*stageProc{},
		resumeAfter:            map[int64]time.Time{},
		queued:                 map[int64]bool{},
		terminalDistillHandled: map[string]bool{},
		startedAt:              time.Now(),
		completions:            make(chan stageResult, 64),
		stopCh:                 make(chan struct{}),
	}
}

// SetConfig — hot-apply настроек (экран настроек UI): подменяет конфиг
// контура на лету (watchdog/пул/backoff читаются на каждом тике).
func (s *Supervisor) SetConfig(cfg Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
}

// GetConfig — актуальный конфиг (RLock).
func (s *Supervisor) GetConfig() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Supervisor) SetPreStageHook(h PreStageHook)         { s.preStage = h }
func (s *Supervisor) SetPostRunHook(h PostRunHook)           { s.postRun = h }
func (s *Supervisor) SetOnStageSucceeded(h OnStageSucceeded) { s.onSuccess = h }

// Run запускает контур (блокирует до Stop/Drain или отмены ctx).
func (s *Supervisor) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.GetConfig().PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				// тик не должен умирать из-за одной ошибки — лог и дальше
				logError(ctx, "tick failed", err)
			}
		}
	}
}

// Drain — graceful shutdown (D-15, T-12): новые этапы не стартуем,
// живые процессы помечаются stop_requested_by=daemon (auto-resume при
// подъёме ДОЛЖЕН сработать) и убиваются с grace.
func (s *Supervisor) Drain(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	procs := make([]*stageProc, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()

	for _, p := range procs {
		if err := s.markStopRequested(p, "daemon"); err != nil {
			logError(ctx, "failed to mark stop_requested_by=daemon", err)
		}
		p.kill(s.GetConfig().KillGrace)
	}

	close(s.stopCh)
	s.wg.Wait()

	// финальная классификация: живые на момент drain стадии должны уйти в
	// interrupted (НЕ failed) до выхода — checkpoint (D-15)
	s.drainCompletions(ctx)
	return nil
}

// tick — одна итерация контура (event-driven tick, ADR-001).
func (s *Supervisor) tick(ctx context.Context) error {
	// 1. завершившиеся процессы → классификация
	s.drainCompletions(ctx)

	// 2. отложенные resume (backoff)
	if err := s.processDueResumes(ctx); err != nil {
		return err
	}

	// 2.5 Interrupt & Steer (D-22): interrupted со steer-заметкой → ре-вход
	if err := s.processSteers(ctx); err != nil {
		return err
	}

	// 3. reconcile: процесс жив, а стадия в БД уже не running (stop/steer
	// пользователя через API) → убить
	s.reconcileProcs(ctx)

	// 4. активные раны → NextAction → действие
	if s.isDraining() {
		return nil
	}
	// 4.5 терминальный distill (T-30): failed-раны проходят distill out-of-band
	if err := s.processTerminalDistills(ctx); err != nil {
		logError(ctx, "terminal distill processing failed", err)
	}
	return s.processActiveRuns(ctx)
}

func (s *Supervisor) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

func (s *Supervisor) processActiveRuns(ctx context.Context) error {
	activeRuns, err := s.runs.ListRuns(ctx, dtorep.ListRunsRequest{
		States: []dtorep.RunState{dtorep.RunStateDraft, dtorep.RunStateRunning},
	})
	if err != nil {
		return fmt.Errorf("failed to list active runs: %w", err)
	}

	for _, run := range activeRuns {
		run := run
		if err := s.processRun(ctx, &run); err != nil {
			logError(ctx, fmt.Sprintf("failed to process run %s", run.ID), err)
		}
	}
	return nil
}

func (s *Supervisor) processRun(ctx context.Context, run *dtorep.Run) error {
	action, err := s.machine.NextAction(ctx, run.ID)
	if err != nil {
		return err
	}

	switch action.Kind {
	case runsmachine.ActionNone, runsmachine.ActionWaitStage, runsmachine.ActionWaitGate:
		return nil

	case runsmachine.ActionStartRun:
		project, err := s.projects.GetProjectByID(ctx, run.ProjectID)
		if err != nil {
			return err
		}
		if s.postRun != nil {
			if err := s.postRun(ctx, run, project.Path); err != nil {
				return fmt.Errorf("post-run hook: %w", err)
			}
		}
		// T-30: версионная деградация vendor-уроков при старте рана
		s.checkVendorVersions(ctx, run, project.Path)
		return s.machine.TransitionRun(ctx, run.ID, dtorep.RunStateRunning)

	case runsmachine.ActionStartStage:
		// мастер-выключатель lessons:off (T-30): distill пропускается
		if s.skipDistillIfDisabled(ctx, run, action.StageKey) {
			return nil
		}
		stage := action.Stage
		if stage == nil {
			stage, err = s.machine.StartStage(ctx, run.ID, action.StageKey, action.Harness)
			if err != nil {
				return err
			}
		}
		return s.launchStage(ctx, run, stage)

	case runsmachine.ActionResumeStage:
		// если для этой стадии уже назначен backoff-resume — ждём его
		s.mu.Lock()
		_, scheduled := s.resumeAfter[action.Stage.ID]
		s.mu.Unlock()
		if scheduled {
			return nil
		}
		// recovery/interrupted без backoff (рестарт демона) — резюмим сразу
		return s.resumeStage(ctx, action.Stage)

	case runsmachine.ActionEscalate:
		_, err := s.machine.OpenGate(ctx, runsmachine.OpenGateRequest{
			RunID:       run.ID,
			StageID:     &action.Stage.ID,
			Kind:        dtorep.GateKindEscalation,
			Question:    fmt.Sprintf("Этап %q прерван %d раз подряд — auto-resume исчерпан. Что делать?", action.Stage.StageKey, action.Stage.ResumeCount),
			ContextJSON: fmt.Sprintf(`{"stage_key":%q,"resume_count":%d}`, action.Stage.StageKey, action.Stage.ResumeCount),
		})
		return err

	case runsmachine.ActionFinishRun:
		// финальный гейт перед succeeded (T-17, D-21)
		if action.RunState == dtorep.RunStateSucceeded && action.FinalGate != "" {
			opened, err := s.machine.EnsureFinalGate(ctx, run.ID)
			if err != nil {
				return err
			}
			if opened {
				return nil // ждём резолва финального гейта
			}
		}
		return s.machine.TransitionRun(ctx, run.ID, action.RunState)
	}
	return nil
}

// resumeStage — auto-resume прерванного этапа: новая попытка + запуск (D-16).
func (s *Supervisor) resumeStage(ctx context.Context, interrupted *dtorep.Stage) error {
	newStage, err := s.machine.ResumeStage(ctx, interrupted.ID)
	if err != nil {
		return err
	}
	if err := s.appendRunEvent(ctx, interrupted.RunID, &newStage.ID, "stage.resumed",
		fmt.Sprintf(`{"stage_key":%q,"iteration":%d,"resume_count":%d}`,
			newStage.StageKey, newStage.Iteration, newStage.ResumeCount)); err != nil {
		return err
	}

	run, err := s.runs.GetRunByID(ctx, newStage.RunID)
	if err != nil {
		return err
	}
	return s.launchStage(ctx, run, newStage)
}

// markStopRequested фиксирует stop_requested_by в БД ДО убийства процесса
// (D-14/15): "user" — auto-resume не сработает; "daemon" — сработает.
func (s *Supervisor) markStopRequested(p *stageProc, by string) error {
	return s.machine.UpdateRunningStage(context.Background(), p.stageID,
		dtorep.StageTransitionFields{StopRequestedBy: &by})
}
