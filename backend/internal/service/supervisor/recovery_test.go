package supervisor_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/harness"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/internal/service/supervisor"
)

// restartFixture — два supervisor'а на одной БД (симуляция рестарта демона).
type restartFixture struct {
	db       *sql.DB
	machine  *runsmachine.Machine
	journal  *events.Journal
	runID    string
	workDir  string
	runsDir  string
	stages   stagesrep.RepositoryWithTX
	runs     runsrep.RepositoryWithTX
	cfg      supervisor.Config
	registry *harness.Registry
}

// Тест «рестарт посреди рана» (D-15, T-12): демон убит → старт → ран
// продолжается с interrupted-стадии, без дублей событий.
func TestDaemonRestartRecovery(t *testing.T) {
	f := newRestartFixture(t, "testdata/steer.sh")
	ctx := context.Background()

	// --- демон 1: этап запущен ---
	cancel1, done1 := f.startSupervisor(t)
	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(ctx, f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateRunning && st.PID != nil
	}, 10*time.Second, 50*time.Millisecond)

	// --- «SIGKILL демона»: тик остановлен, процесс-орфан умирает сам ---
	cancel1()
	<-done1
	stage := f.latestStage(t)
	killProcessGroup(t, *stage.PID)

	// --- демон 2: recovery + продолжение ---
	recovered, err := f.machine.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	assert.Equal(t, dtorep.StageStateInterrupted, recovered[0].State)

	cancel2, done2 := f.startSupervisor(t)
	defer func() { cancel2(); <-done2 }()

	require.Eventually(t, func() bool {
		run, err := f.runs.GetRunByID(ctx, f.runID)
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond, "run must continue after daemon restart")

	stage = f.latestStage(t)
	assert.Equal(t, int64(2), stage.Iteration)
	assert.Equal(t, int64(1), stage.ResumeCount)

	// без дублей событий: ровно один переход В running для попытки 1
	evs, err := f.journal.Replay(ctx, f.runID, 0, 10000)
	require.NoError(t, err)
	var runningTransitions int
	for _, ev := range evs {
		if ev.Kind == "stage.state_changed" && ev.StageID != nil && *ev.StageID == 1 &&
			strings.Contains(ev.PayloadJSON, `"to":"running"`) {
			runningTransitions++
		}
	}
	assert.Equal(t, 1, runningTransitions, "попытка 1 стартовала ровно один раз — дублей нет")
}

// Graceful shutdown (D-15): Drain помечает stop_requested_by=daemon,
// при подъёме флаг очищается и стадия auto-resume'ится.
func TestGracefulDrainAndRecovery(t *testing.T) {
	f := newRestartFixture(t, "testdata/steer.sh")
	ctx := context.Background()

	cancel1, done1 := f.startSupervisor(t)
	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(ctx, f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateRunning
	}, 10*time.Second, 50*time.Millisecond)

	// SIGTERM-эквивалент: Drain
	sup := f.lastSupervisor()
	require.NoError(t, sup.Drain(ctx))
	cancel1()
	<-done1

	stage := f.latestStage(t)
	require.Equal(t, dtorep.StageStateInterrupted, stage.State)
	require.NotNil(t, stage.StopRequestedBy)
	require.Equal(t, "daemon", *stage.StopRequestedBy)

	// подъём: recovery очищает daemon-флаг → auto-resume
	_, err := f.machine.RecoverInterrupted(ctx)
	require.NoError(t, err)

	stage = f.latestStage(t)
	require.NotNil(t, stage.StopRequestedBy)
	require.Equal(t, "", *stage.StopRequestedBy, "daemon flag must be cleared")

	cancel2, done2 := f.startSupervisor(t)
	defer func() { cancel2(); <-done2 }()

	require.Eventually(t, func() bool {
		run, err := f.runs.GetRunByID(ctx, f.runID)
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 15*time.Second, 100*time.Millisecond, "daemon-interrupted stage must auto-resume")
}
