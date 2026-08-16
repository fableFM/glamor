package supervisor_test

import (
	"context"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/harness"
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

// newRestartFixture — общая БД для нескольких supervisor'ов (рестарт демона).
// Скрипт steer.sh: попытка 1 — долгая, попытка 2 — быстрый успех.
func newRestartFixture(t *testing.T, script string) *restartFixture {
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
		Name: "default", Version: 1, SpecJSON: specWithArtifact,
	})
	require.NoError(t, err)

	hub := events.NewHub()
	journal := events.NewJournal(db, hub)
	machine := usecase.NewMachine(db, journal)
	machine.SetTxExecutor(journal)

	run, already, err := machine.CreateRun(ctx, usecase.CreateRunParams{
		ProjectID:         projectID,
		PipelineVersionID: pipelineID,
		TaskText:          "restart test",
		BaseBranch:        "main",
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

	return &restartFixture{
		db: db, machine: machine, journal: journal,
		runID: run.ID, workDir: workDir, runsDir: runsDir,
		stages:   stagesrep.NewRepository(db),
		runs:     runsrep.NewRepository(db),
		cfg:      cfg,
		registry: registry,
	}
}

var (
	lastSupMu sync.Mutex
	lastSup   *supervisor.Supervisor
)

// startSupervisor поднимает supervisor на общей БД (доступен через
// lastSupervisor) и возвращает управление его жизненным циклом.
func (f *restartFixture) startSupervisor(t *testing.T) (context.CancelFunc, chan struct{}) {
	t.Helper()

	sup := supervisor.New(f.machine, f.registry, f.journal, f.cfg, nil,
		f.runs, f.stages,
		projectsrep.NewRepository(f.db), pipelinesrep.NewRepository(f.db),
		notesrep.NewRepository(f.db))

	lastSupMu.Lock()
	lastSup = sup
	lastSupMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = sup.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func (f *restartFixture) lastSupervisor() *supervisor.Supervisor {
	lastSupMu.Lock()
	defer lastSupMu.Unlock()
	return lastSup
}

// killProcessGroup — SIGKILL группе (симуляция смерти орфан-процесса).
func killProcessGroup(t *testing.T, pid int64) {
	t.Helper()
	pgid, err := syscall.Getpgid(int(pid))
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = syscall.Kill(int(pid), syscall.SIGKILL)
}

func (f *restartFixture) latestStage(t *testing.T) *dtorep.Stage {
	t.Helper()
	st, err := f.stages.GetLatestStage(context.Background(), f.runID, "plan")
	require.NoError(t, err)
	return st
}
