package runsmachine_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/repository"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	machine "github.com/fableFM/glamor/internal/service/runsmachine"
	"github.com/fableFM/glamor/pkg/uuid"
)

// newMachineWithSpec — машина + ран с кастомной спекой пайплайна.
func newMachineWithSpec(t *testing.T, specJSON string) (*machine.Machine, string) {
	t.Helper()
	db := testdb.New(t)
	ctx := context.Background()

	projectID, err := projectsrep.NewRepository(db).CreateProject(ctx, dtorep.CreateProjectRequest{
		Path: "/tmp/" + uuid.New(), Name: "test", DefaultBranch: "main",
	})
	require.NoError(t, err)
	pipelineID, err := pipelinesrep.NewRepository(db).CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name: "test", Version: 1, SpecJSON: specJSON,
	})
	require.NoError(t, err)
	runID := uuid.New()
	require.NoError(t, runsrep.NewRepository(db).CreateRun(ctx, dtorep.CreateRunRequest{
		ID: runID, ProjectID: projectID, PipelineVersionID: pipelineID,
		TaskText: "t", BaseBranch: "main", Branch: "glamor/t",
		State: dtorep.RunStateDraft, IdempotencyKey: uuid.New(),
	}))

	m := machine.NewMachine(
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		gatesrep.NewRepository(db), pipelinesrep.NewRepository(db),
		projectsrep.NewRepository(db), notesrep.NewRepository(db),
		repository.NewTxManager(db), nil)
	return m, runID
}

const parallelSpec = `{"stages":[
	{"key":"plan","harness":"kimi"},
	{"key":"research-a","harness":"kimi","parallel_group":"fan","read_only":true},
	{"key":"research-b","harness":"kimi","parallel_group":"fan","read_only":true},
	{"key":"join","harness":"kimi"}
],"parallel_groups":[{"name":"fan","on_failure":"fail_fast"}]}`

// Fan-out (T-28): обе ветки стартуют параллельно, join ждёт всех.
func TestParallelGroupProgression(t *testing.T) {
	ctx := context.Background()
	m, runID := newMachineWithSpec(t, parallelSpec)
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	// plan
	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, machine.ActionStartStage, action.Kind)
	plan, err := m.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)
	require.NoError(t, m.TransitionStage(ctx, plan.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))
	require.NoError(t, m.TransitionStage(ctx, plan.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))

	// старт ветки A
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, machine.ActionStartStage, action.Kind)
	assert.Equal(t, "research-a", action.StageKey)
	ra, err := m.StartStage(ctx, runID, "research-a", "kimi")
	require.NoError(t, err)
	require.NoError(t, m.TransitionStage(ctx, ra.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))

	// ветка A running → стартует ветка B (НЕ wait!)
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, machine.ActionStartStage, action.Kind)
	assert.Equal(t, "research-b", action.StageKey)
	rb, err := m.StartStage(ctx, runID, "research-b", "kimi")
	require.NoError(t, err)
	require.NoError(t, m.TransitionStage(ctx, rb.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{}))

	// обе running → wait (join не пропускает)
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionWaitStage, action.Kind)

	// A succeeded, B ещё running → всё ещё wait
	require.NoError(t, m.TransitionStage(ctx, ra.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionWaitStage, action.Kind)

	// B succeeded → join: стартует join-этап
	require.NoError(t, m.TransitionStage(ctx, rb.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, machine.ActionStartStage, action.Kind)
	assert.Equal(t, "join", action.StageKey)
}

// fail_fast: падение ветки → ран failed сразу.
func TestParallelGroupFailFast(t *testing.T) {
	ctx := context.Background()
	m, runID := newMachineWithSpec(t, parallelSpec)
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	plan, _ := m.StartStage(ctx, runID, "plan", "kimi")
	_ = m.TransitionStage(ctx, plan.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
	_ = m.TransitionStage(ctx, plan.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{})

	ra, _ := m.StartStage(ctx, runID, "research-a", "kimi")
	_ = m.TransitionStage(ctx, ra.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
	action, _ := m.NextAction(ctx, runID)
	rb, _ := m.StartStage(ctx, runID, action.StageKey, "kimi")
	_ = m.TransitionStage(ctx, rb.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{})

	// B падает, A ещё работает → fail_fast → finish failed
	require.NoError(t, m.TransitionStage(ctx, rb.ID, dtorep.StageStateFailed, dtorep.StageTransitionFields{}))
	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionFinishRun, action.Kind)
	assert.Equal(t, dtorep.RunStateFailed, action.RunState)
}

// wait_all: падение ветки → ждём остальных, потом failed.
func TestParallelGroupWaitAll(t *testing.T) {
	ctx := context.Background()
	spec := `{"stages":[
		{"key":"a","harness":"kimi","parallel_group":"fan","read_only":true},
		{"key":"b","harness":"kimi","parallel_group":"fan","read_only":true}
	],"parallel_groups":[{"name":"fan","on_failure":"wait_all"}]}`
	m, runID := newMachineWithSpec(t, spec)
	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	ra, _ := m.StartStage(ctx, runID, "a", "kimi")
	_ = m.TransitionStage(ctx, ra.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
	action, _ := m.NextAction(ctx, runID)
	rb, _ := m.StartStage(ctx, runID, action.StageKey, "kimi")
	_ = m.TransitionStage(ctx, rb.ID, dtorep.StageStateRunning, dtorep.StageTransitionFields{})

	// B failed, A running → ждём (не finish!)
	require.NoError(t, m.TransitionStage(ctx, rb.ID, dtorep.StageStateFailed, dtorep.StageTransitionFields{}))
	action, err := m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionWaitStage, action.Kind)

	// A succeeded → finish failed
	require.NoError(t, m.TransitionStage(ctx, ra.ID, dtorep.StageStateSucceeded, dtorep.StageTransitionFields{}))
	action, err = m.NextAction(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, machine.ActionFinishRun, action.Kind)
	assert.Equal(t, dtorep.RunStateFailed, action.RunState)
}
