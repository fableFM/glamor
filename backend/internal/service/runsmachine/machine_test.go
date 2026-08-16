package runsmachine_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/repository"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	machine "github.com/fableFM/glamor/internal/service/runsmachine"
)

func newMachine(t *testing.T) (*machine.Machine, string, *sql.DB) {
	t.Helper()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db) // пайплайн: plan → code
	m := machine.NewMachine(
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		gatesrep.NewRepository(db), pipelinesrep.NewRepository(db),
		projectsrep.NewRepository(db), notesrep.NewRepository(db),
		repository.NewTxManager(db), nil)
	return m, runID, db
}

// Разрешённые переходы рана + запрещённые = ErrInvalidTransition.
func TestRunTransitions(t *testing.T) {
	ctx := context.Background()
	m, runID, db := newMachine(t)

	// draft → succeeded запрещён
	err := m.TransitionRun(ctx, runID, dtorep.RunStateSucceeded)
	require.ErrorIs(t, err, cstmerrors.ErrInvalidTransition)

	// draft → running → waiting_gate → running → succeeded
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateWaitingGate))
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateSucceeded))

	// succeeded — терминал
	err = m.TransitionRun(ctx, runID, dtorep.RunStateRunning)
	require.ErrorIs(t, err, cstmerrors.ErrInvalidTransition)

	// каждый переход оставил событие
	evs, err := eventsrep.NewRepository(db).ReplayEvents(ctx, runID, 0, 0)
	require.NoError(t, err)
	var runEvents int
	for _, ev := range evs {
		if ev.Kind == machine.EventKindRunStateChanged {
			runEvents++
		}
	}
	assert.Equal(t, 4, runEvents)
}

// Разрешённые/запрещённые переходы стадии.
func TestStageTransitions(t *testing.T) {
	ctx := context.Background()
	m, runID, _ := newMachine(t)

	stage, err := m.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)
	require.Equal(t, dtorep.StageStatePending, stage.State)

	// pending → succeeded запрещён
	require.ErrorIs(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}),
		cstmerrors.ErrInvalidTransition)

	require.NoError(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))
	require.NoError(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))

	// succeeded → running запрещён
	require.ErrorIs(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}),
		cstmerrors.ErrInvalidTransition)
}

// «Убить демон посреди рана»: running-стадия после рестарта → interrupted,
// движок предлагает resume, resume создаёт новую попытку (ADR-001).
func TestKillDaemonRecovery(t *testing.T) {
	ctx := context.Background()
	m, runID, _ := newMachine(t)

	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	stage, err := m.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)
	require.NoError(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))

	// --- «демон убит»: running-стадия осталась сиротой. Рестарт. ---
	recovered, err := m.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	assert.Equal(t, dtorep.StageStateInterrupted, recovered[0].State)

	// движок видит interrupted и предлагает resume
	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, machine.ActionResumeStage, action.Kind)
	require.Equal(t, stage.ID, action.Stage.ID)

	// resume = НОВАЯ строка iteration+1, resume_count+1 (аудит, ADR-001)
	resumed, err := m.ResumeStage(ctx, stage.ID)
	require.NoError(t, err)
	assert.NotEqual(t, stage.ID, resumed.ID)
	assert.Equal(t, int64(2), resumed.Iteration)
	assert.Equal(t, int64(1), resumed.ResumeCount)
	assert.Equal(t, dtorep.StageStatePending, resumed.State)

	// resume не от последней попытки цепочки запрещён
	_, err = m.ResumeStage(ctx, stage.ID)
	require.ErrorIs(t, err, cstmerrors.ErrInvalidTransition)
}

// Resume из не-interrupted состояния запрещён; лимит auto-resume → эскалация.
func TestResumeLimits(t *testing.T) {
	ctx := context.Background()
	m, runID, _ := newMachine(t)

	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	stage, err := m.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)

	// resume из pending запрещён
	_, err = m.ResumeStage(ctx, stage.ID)
	require.ErrorIs(t, err, cstmerrors.ErrInvalidTransition)

	// выжигаем лимит resume: MaxResumeCount циклов run→interrupt→resume
	current := stage
	for i := range machine.MaxResumeCount {
		require.NoError(t, m.TransitionStage(ctx, current.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}), "iter %d", i)
		require.NoError(t, m.TransitionStage(ctx, current.ID, dtorep.StageStateInterrupted, dtorep.StageTransitionFields{}), "iter %d", i)
		current, err = m.ResumeStage(ctx, current.ID)
		require.NoError(t, err, "iter %d", i)
	}
	assert.Equal(t, int64(machine.MaxResumeCount), current.ResumeCount)

	// лимит исчерпан: NextAction → эскалация
	require.NoError(t, m.TransitionStage(ctx, current.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))
	require.NoError(t, m.TransitionStage(ctx, current.ID, dtorep.StageStateInterrupted, dtorep.StageTransitionFields{}))
	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionEscalate, action.Kind)

	_, err = m.ResumeStage(ctx, current.ID)
	require.ErrorIs(t, err, cstmerrors.ErrInvalidTransition)
}

// Гейты: open → run waiting_gate; resolve → run running; reject → run failed.
func TestGateLifecycle(t *testing.T) {
	ctx := context.Background()
	m, runID, db := newMachine(t)

	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	// открыть гейт нельзя в draft
	m2, draftRunID, _ := newMachine(t)
	_, err := m2.OpenGate(ctx, machine.OpenGateRequest{RunID: draftRunID, Kind: dtorep.GateKindPlanApproval, Question: "ok?"})
	require.ErrorIs(t, err, cstmerrors.ErrInvalidTransition)

	gate, err := m.OpenGate(ctx, machine.OpenGateRequest{
		RunID:          runID,
		Kind:           dtorep.GateKindPlanApproval,
		Question:       "План ок?",
		IdempotencyKey: "gate-1",
	})
	require.NoError(t, err)

	run, err := runsrep.NewRepository(db).GetRunByID(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateWaitingGate, run.State)

	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionWaitGate, action.Kind)

	// идемпотентность: повторный OpenGate с тем же ключом — тот же гейт
	dup, err := m.OpenGate(ctx, machine.OpenGateRequest{
		RunID:          runID,
		Kind:           dtorep.GateKindPlanApproval,
		Question:       "План ок?",
		IdempotencyKey: "gate-1",
	})
	require.NoError(t, err)
	assert.Equal(t, gate.ID, dup.ID)

	// второй открытый гейт на том же (run, stage=nil) — запрещён
	_, err = m.OpenGate(ctx, machine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindQuestion,
		Question: "ещё вопрос",
	})
	require.ErrorIs(t, err, cstmerrors.ErrDuplicate)

	// approve → ран возвращается в running
	require.NoError(t, m.ResolveGate(ctx, gate.ID, dtorep.GateStateApproved, nil))
	run, err = runsrep.NewRepository(db).GetRunByID(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateRunning, run.State)

	// повторный resolve тем же резолюшном — идемпотентный no-op
	require.NoError(t, m.ResolveGate(ctx, gate.ID, dtorep.GateStateApproved, nil))
	// resolve в ДРУГОЕ состояние — запрещён (approved — терминал гейта)
	require.ErrorIs(t, m.ResolveGate(ctx, gate.ID, dtorep.GateStateRejected, nil),
		cstmerrors.ErrInvalidTransition)

	// reject → ран failed
	gate2, err := m.OpenGate(ctx, machine.OpenGateRequest{
		RunID:    runID,
		Kind:     dtorep.GateKindFinalReview,
		Question: "Результат ок?",
	})
	require.NoError(t, err)
	require.NoError(t, m.ResolveGate(ctx, gate2.ID, dtorep.GateStateRejected, nil))
	run, err = runsrep.NewRepository(db).GetRunByID(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateFailed, run.State)
	require.NotNil(t, run.FinishedAt)
}

// Параллельные переходы через машину: недопустимое состояние не возникает.
func TestMachineParallelTransitions(t *testing.T) {
	ctx := context.Background()
	m, runID, db := newMachine(t)

	stage, err := m.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			require.ErrorIs(t, err, cstmerrors.ErrConcurrentModification)
		}
	}

	st, err := stagesrep.NewRepository(db).GetStageByID(ctx, stage.ID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateRunning, st.State)

	// ровно одно событие running
	evs, err := eventsrep.NewRepository(db).ReplayEvents(ctx, runID, 0, 0)
	require.NoError(t, err)
	var runningEvents int
	for _, ev := range evs {
		if ev.Kind == machine.EventKindStageStateChanged && ev.StageID != nil && *ev.StageID == stage.ID {
			runningEvents++
		}
	}
	assert.Equal(t, 2, runningEvents, "события: pending(создание) + running")
}

// Линейное продвижение по спеке plan→code через NextAction.
func TestNextActionLinearProgression(t *testing.T) {
	ctx := context.Background()
	m, runID, _ := newMachine(t)

	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionStartRun, action.Kind)

	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	for _, key := range []string{"plan", "code"} {
		action, err = m.NextAction(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, machine.ActionStartStage, action.Kind)
		require.Equal(t, key, action.StageKey)

		stage, err := m.StartStage(ctx, runID, action.StageKey, action.Harness)
		require.NoError(t, err)

		action, err = m.NextAction(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, machine.ActionStartStage, action.Kind)
		require.NotNil(t, action.Stage, "pending-строка уже есть — запускаем её")

		require.NoError(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))
		action, err = m.NextAction(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, machine.ActionWaitStage, action.Kind)

		require.NoError(t, m.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))
	}

	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, machine.ActionFinishRun, action.Kind)
	assert.Equal(t, dtorep.RunStateSucceeded, action.RunState)

	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateSucceeded))
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionNone, action.Kind)
}

// Property-тест: случайные последовательности команд не приводят к
// недопустимым состояниям (ADR-001).
func TestPropertyRandomCommands(t *testing.T) {
	ctx := context.Background()
	m, runID, db := newMachine(t)
	runsRepo := runsrep.NewRepository(db)
	stagesRepo := stagesrep.NewRepository(db)
	gatesRepo := gatesrep.NewRepository(db)

	rng := rand.New(rand.NewPCG(42, 0))

	stageTargets := []dtorep.StageState{
		dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageStateSucceeded,
		dtorep.StageStateFailed, dtorep.StageStateInterrupted, dtorep.StageStateSkipped,
	}
	gateResolutions := []dtorep.GateState{
		dtorep.GateStateAnswered, dtorep.GateStateApproved,
		dtorep.GateStateRejected, dtorep.GateStateExpired,
	}

	for step := range 500 {
		action, err := m.NextAction(ctx, runID)
		require.NoError(t, err, "step %d", step)

		switch rng.IntN(8) {
		case 0: // старт рана
			_ = m.TransitionRun(ctx, runID, dtorep.RunStateRunning)
		case 1: // старт следующего этапа по движку
			if action.Kind == machine.ActionStartStage && action.Stage == nil {
				_, _ = m.StartStage(ctx, runID, action.StageKey, action.Harness)
			}
		case 2: // случайный переход случайной стадии (валидный или нет)
			stages := allStages(t, stagesRepo, runID)
			if len(stages) > 0 {
				st := stages[rng.IntN(len(stages))]
				_ = m.TransitionStage(ctx, st.ID, stageTargets[rng.IntN(len(stageTargets))], dtorep.StageTransitionFields{})
			}
		case 3: // открыть гейт
			_, _ = m.OpenGate(ctx, machine.OpenGateRequest{
				RunID:    runID,
				Kind:     dtorep.GateKindQuestion,
				Question: fmt.Sprintf("q%d", step),
			})
		case 4: // резолв случайного открытого гейта
			open, err := gatesRepo.ListOpenGates(ctx, runID)
			require.NoError(t, err)
			if len(open) > 0 {
				g := open[rng.IntN(len(open))]
				_ = m.ResolveGate(ctx, g.ID, gateResolutions[rng.IntN(len(gateResolutions))], nil)
			}
		case 5: // recovery (как при рестарте демона)
			_, _ = m.RecoverInterrupted(ctx)
		case 6: // resume случайной interrupted-стадии
			stages := allStages(t, stagesRepo, runID)
			for _, st := range stages {
				if st.State == dtorep.StageStateInterrupted {
					_, _ = m.ResumeStage(ctx, st.ID)
					break
				}
			}
		case 7: // стоп рана пользователем
			_ = m.TransitionRun(ctx, runID, dtorep.RunStateStopped)
		}

		assertInvariants(t, ctx, runsRepo, stagesRepo, gatesRepo, runID, step)
	}
}

func allStages(t *testing.T, repo stagesrep.RepositoryWithTX, runID string) []dtorep.Stage {
	t.Helper()
	stages, err := repo.ListStagesByRun(context.Background(), runID)
	require.NoError(t, err)
	return stages
}

// Инварианты ADR-001, проверяемые после каждого шага property-теста.
func assertInvariants(t *testing.T, ctx context.Context,
	runsRepo runsrep.RepositoryWithTX,
	stagesRepo stagesrep.RepositoryWithTX,
	gatesRepo gatesrep.RepositoryWithTX,
	runID string, step int,
) {
	t.Helper()

	run, err := runsRepo.GetRunByID(ctx, runID)
	require.NoError(t, err, "step %d", step)

	// состояние рана — из enum
	switch run.State {
	case dtorep.RunStateDraft, dtorep.RunStateRunning, dtorep.RunStateWaitingGate,
		dtorep.RunStateSucceeded, dtorep.RunStateFailed, dtorep.RunStateStopped:
	default:
		t.Fatalf("step %d: invalid run state %q", step, run.State)
	}

	// не более одной running-стадии на (run_id, stage_key)
	stages := allStages(t, stagesRepo, runID)
	running := map[string]int{}
	for _, st := range stages {
		switch st.State {
		case dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageStateSucceeded,
			dtorep.StageStateFailed, dtorep.StageStateInterrupted, dtorep.StageStateSkipped:
		default:
			t.Fatalf("step %d: invalid stage state %q", step, st.State)
		}
		if st.State == dtorep.StageStateRunning {
			running[st.StageKey]++
		}
	}
	for key, n := range running {
		require.LessOrEqual(t, n, 1, "step %d: stage %q has %d running rows", step, key, n)
	}

	// не более одного открытого гейта на (run_id, stage_id)
	open, err := gatesRepo.ListOpenGates(ctx, runID)
	require.NoError(t, err, "step %d", step)
	seen := map[string]struct{}{}
	for _, g := range open {
		key := fmt.Sprintf("%v", g.StageID)
		if _, dup := seen[key]; dup {
			t.Fatalf("step %d: two open gates for stage %v", step, g.StageID)
		}
		seen[key] = struct{}{}
	}

	// waiting_gate <=> есть открытые гейты (пока ран не терминальный)
	if run.State == dtorep.RunStateWaitingGate {
		require.NotEmpty(t, open, "step %d: waiting_gate without open gates", step)
	}
}
