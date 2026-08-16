package supervisor_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/harness"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/service/supervisor"
	usecase "github.com/fableFM/glamor/internal/usecase/runs"
	"github.com/fableFM/glamor/pkg/uuid"
)

// --- fake harness ---------------------------------------------------------

// fakeAdapter запускает bash-скрипт из testdata; ParseStream понимает
// {"kind":..., ...} NDJSON, остальное — raw.
type fakeAdapter struct{ script string }

func (f fakeAdapter) Name() string       { return "fake" }
func (f fakeAdapter) BinaryName() string { return "bash" }

func (f fakeAdapter) Capabilities() harness.Capabilities {
	return harness.Capabilities{StreamJSON: true, Resume: true, UsageSource: harness.UsageSourceStream}
}

func (f fakeAdapter) BuildCommand(spec harness.LaunchSpec) (harness.CommandSpec, error) {
	// $1 = workdir, $2 = prompt (для ассертов содержимого промпта)
	return harness.CommandSpec{Argv: []string{"bash", f.script, spec.WorkDir, spec.Prompt}}, nil
}

func (f fakeAdapter) ParseStream(line []byte) []harness.Event {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return []harness.Event{{Kind: harness.EventRaw, Text: string(line)}}
	}
	ev := harness.Event{Kind: harness.EventRaw, Text: string(line)}
	if kind, ok := m["kind"].(string); ok {
		ev.Kind = kind
		ev.Text, _ = m["text"].(string)
		ev.SessionID, _ = m["session_id"].(string)
		if u, ok := m["usage"].(map[string]any); ok {
			ev.Usage = &harness.Usage{
				Input:  int64(u["input"].(float64)),
				Output: int64(u["output"].(float64)),
			}
		}
		if e, ok := m["error"].(map[string]any); ok {
			ev.Err = &harness.StreamError{
				Message:   e["message"].(string),
				Retriable: e["retriable"].(bool),
			}
		}
	}
	return []harness.Event{ev}
}

func (f fakeAdapter) ExtractSessionID(evs []harness.Event, _ string) (string, error) {
	for _, ev := range evs {
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", errors.New("no session id")
}

// --- fixture ---------------------------------------------------------------

type fixture struct {
	sup     *supervisor.Supervisor
	machine *usecase.Machine
	journal *events.Journal
	runID   string
	workDir string
	runsDir string
	stages  stagesrep.RepositoryWithTX
	runs    runsrep.RepositoryWithTX
	gates   gatesrep.RepositoryWithTX
	notes   notesrep.RepositoryWithTX
}

func newFixture(t *testing.T, script, specJSON string, mutateCfg func(*supervisor.Config)) *fixture {
	t.Helper()

	scriptAbs, err := filepath.Abs(script)
	require.NoError(t, err)
	script = scriptAbs

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
	journal := events.NewJournal(db, hub)
	machine := usecase.NewMachine(db, journal)
	machine.SetTxExecutor(journal)

	run, already, err := machine.CreateRun(ctx, usecase.CreateRunParams{
		ProjectID:         projectID,
		PipelineVersionID: pipelineID,
		TaskText:          "test task",
		BaseBranch:        "main",
		IdempotencyKey:    uuid.New(),
	})
	require.NoError(t, err)
	require.False(t, already)

	registry := harness.NewRegistry(nil)
	registry.Register(fakeAdapter{script: script})

	cfg := supervisor.DefaultConfig(runsDir)
	cfg.PollInterval = 50 * time.Millisecond
	cfg.StallTimeout = time.Second
	cfg.KillGrace = 200 * time.Millisecond
	cfg.StageTimeout = 30 * time.Second
	cfg.Backoff = []time.Duration{50 * time.Millisecond}
	cfg.MaxAutoResumes = 2
	cfg.MaxParallel = 2
	if mutateCfg != nil {
		mutateCfg(&cfg)
	}

	sup := supervisor.New(machine, registry, journal, cfg, nil,
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		projectsrep.NewRepository(db), pipelinesrep.NewRepository(db),
		notesrep.NewRepository(db))

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

	return &fixture{
		sup: sup, machine: machine, journal: journal,
		runID: run.ID, workDir: workDir, runsDir: runsDir,
		stages: stagesrep.NewRepository(db),
		runs:   runsrep.NewRepository(db),
		gates:  gatesrep.NewRepository(db),
		notes:  notesrep.NewRepository(db),
	}
}

func (f *fixture) latestStage(t *testing.T) *dtorep.Stage {
	t.Helper()
	st, err := f.stages.GetLatestStage(context.Background(), f.runID, "plan")
	require.NoError(t, err)
	return st
}

func (f *fixture) events(t *testing.T) []dtorep.Event {
	t.Helper()
	evs, err := f.journal.Replay(context.Background(), f.runID, 0, 10000)
	require.NoError(t, err)
	return evs
}

func (f *fixture) hasEventKind(t *testing.T, kind string) bool {
	t.Helper()
	for _, ev := range f.events(t) {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

const specWithArtifact = `{"stages":[{"key":"plan","harness":"fake","artifact":{"path":"artifact.txt","required":true}}]}`

// --- тесты -------------------------------------------------------------------

// Успешный этап: exit 0 + артефакт → succeeded, ран → succeeded,
// session_id сохранён, лог попытки на месте (D-13).
func TestStageSuccess(t *testing.T) {
	f := newFixture(t, "testdata/success.sh", specWithArtifact, nil)

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 10*time.Second, 50*time.Millisecond)

	stage := f.latestStage(t)
	assert.Equal(t, dtorep.StageStateSucceeded, stage.State)
	require.NotNil(t, stage.SessionID)
	assert.Equal(t, "fake-session-1", *stage.SessionID)
	assert.Equal(t, int64(100), stage.TokensIn)
	assert.Equal(t, int64(50), stage.TokensOut)

	// лог попытки содержит сырой выхлоп (receipts)
	logPath := filepath.Join(f.runsDir, f.runID, "stage-plan-1.log")
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "fake-session-1")

	// события: переходы + стрим
	assert.True(t, f.hasEventKind(t, "stage.state_changed"))
	assert.True(t, f.hasEventKind(t, "run.state_changed"))
	assert.True(t, f.hasEventKind(t, "stream.text"))
}

// Падение первой попытки (exit 1, нет артефакта) → interrupted →
// auto-resume → вторая попытка успешна (D-14/16).
func TestAutoResume(t *testing.T) {
	f := newFixture(t, "testdata/fail_once.sh", specWithArtifact, nil)

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 10*time.Second, 50*time.Millisecond)

	stage := f.latestStage(t)
	assert.Equal(t, dtorep.StageStateSucceeded, stage.State)
	assert.Equal(t, int64(2), stage.Iteration)
	assert.Equal(t, int64(1), stage.ResumeCount)

	// resume унаследовал session id предыдущей попытки
	require.NotNil(t, stage.SessionID)
	assert.Equal(t, "fake-session-1", *stage.SessionID)

	assert.True(t, f.hasEventKind(t, "stage.interrupted"))
	assert.True(t, f.hasEventKind(t, "stage.resumed"))
}

// Stall: процесс молчит → watchdog убивает → interrupted → auto-resume;
// исчерпание лимита → эскалация-гейт (T-09/T-11).
func TestStallWatchdog(t *testing.T) {
	f := newFixture(t, "testdata/stall.sh", specWithArtifact, nil)

	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(context.Background(), f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateInterrupted
	}, 15*time.Second, 100*time.Millisecond, "watchdog must interrupt stalling stage")

	// исчерпание auto-resume → эскалация-гейт и ран в waiting_gate
	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		if err != nil || run.State != dtorep.RunStateWaitingGate {
			return false
		}
		gates, err := f.gatesList(run.ID)
		if err != nil {
			return false
		}
		for _, g := range gates {
			if g.Kind == dtorep.GateKindEscalation {
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond)
}

// Stop пользователем: процесс убит, auto-resume НЕ срабатывает (D-14).
func TestUserStop(t *testing.T) {
	f := newFixture(t, "testdata/long_running.sh", specWithArtifact, nil)

	ctx := context.Background()
	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(ctx, f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateRunning
	}, 10*time.Second, 50*time.Millisecond)

	_, err := f.machine.StopRun(ctx, f.runID)
	require.NoError(t, err)

	// процесс добит reconcile'ом, стадия interrupted с stop_requested_by=user
	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(ctx, f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateInterrupted &&
			st.StopRequestedBy != nil && *st.StopRequestedBy == "user"
	}, 10*time.Second, 50*time.Millisecond)

	// auto-resume не срабатывает: новой попытки нет даже через время
	time.Sleep(500 * time.Millisecond)
	stages, err := f.stages.ListStagesByRun(ctx, f.runID)
	require.NoError(t, err)
	for _, st := range stages {
		assert.Equal(t, int64(1), st.Iteration, "no resume attempt after user stop")
	}

	run, err := f.machineRun()
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateStopped, run.State)
}

// exit 0 без обязательного артефакта → failed (не interrupted), ран → failed.
func TestExitZeroWithoutArtifact(t *testing.T) {
	f := newFixture(t, "testdata/no_artifact.sh", specWithArtifact, nil)

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateFailed
	}, 10*time.Second, 50*time.Millisecond)

	stage := f.latestStage(t)
	assert.Equal(t, dtorep.StageStateFailed, stage.State)
}

// Пул процессов (D-33): MaxParallel=1 — второй этап ждёт (stage.queued).
func TestProcessPool(t *testing.T) {
	spec := `{"stages":[
		{"key":"one","harness":"fake","artifact":{"path":"a1.txt","required":true}},
		{"key":"two","harness":"fake","artifact":{"path":"a2.txt","required":true}}
	]}`
	f := newFixture(t, "testdata/success.sh", spec, func(cfg *supervisor.Config) {
		cfg.MaxParallel = 1
	})

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 15*time.Second, 50*time.Millisecond)

	assert.True(t, f.hasEventKind(t, "stage.queued"))
}

// --- helpers -----------------------------------------------------------------

func (f *fixture) machineRun() (*dtorep.Run, error) {
	return f.runs.GetRunByID(context.Background(), f.runID)
}

func (f *fixture) gatesList(runID string) ([]dtorep.Gate, error) {
	return f.gates.ListOpenGates(context.Background(), runID)
}
