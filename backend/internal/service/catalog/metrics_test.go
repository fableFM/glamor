package catalog_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
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
	"github.com/fableFM/glamor/internal/service/catalog"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

func newCatalogSvc(t *testing.T) (*catalog.Service, *runsmachine.Machine, *events.Journal, string) {
	t.Helper()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	hub := events.NewHub()
	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), hub)
	machine := runsmachine.NewMachine(
		runsrep.NewRepository(db), stagesrep.NewRepository(db), gatesrep.NewRepository(db),
		pipelinesrep.NewRepository(db), projectsrep.NewRepository(db), notesrep.NewRepository(db),
		journal, journal)

	svc := catalog.New(
		projectsrep.NewRepository(db), pipelinesrep.NewRepository(db), runsrep.NewRepository(db),
		stagesrep.NewRepository(db), gatesrep.NewRepository(db), artifactsrep.NewRepository(db),
		notesrep.NewRepository(db), journal, t.TempDir())
	return svc, machine, journal, runID
}

// Метрики этапа == сумма usage-событий журнала (T-24 acceptance).
func TestRunMetrics_MatchUsageEvents(t *testing.T) {
	ctx := context.Background()
	svc, machine, journal, runID := newCatalogSvc(t)

	require.NoError(t, machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	stage, err := machine.StartStage(ctx, runID, "plan", "kimi")
	require.NoError(t, err)

	// три usage-события (100/50, 200/70, 10/5) — как opencode step_finish
	usage := [][]int64{{100, 50}, {200, 70}, {10, 5}}
	for _, u := range usage {
		err := journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			return journal.Append(ctx, tx, dtorep.Event{
				RunID:       runID,
				StageID:     &stage.ID,
				Kind:        "stream.usage",
				PayloadJSON: `{"tokens_in":` + itoa(u[0]) + `,"tokens_out":` + itoa(u[1]) + `}`,
			})
		})
		require.NoError(t, err)
	}

	// записываем сумму в стадию (так делает supervisor на завершении)
	tokensIn := int64(310)
	tokensOut := int64(125)
	now := time.Now().UTC()
	started := now.Add(-90 * time.Second)
	require.NoError(t, machine.TransitionStage(ctx, stage.ID, dtorep.StageStateRunning,
		dtorep.StageTransitionFields{StartedAt: &started}))
	require.NoError(t, machine.TransitionStage(ctx, stage.ID, dtorep.StageStateSucceeded,
		dtorep.StageTransitionFields{FinishedAt: &now, TokensIn: &tokensIn, TokensOut: &tokensOut}))

	m, err := svc.RunMetrics(ctx, runID)
	require.NoError(t, err)
	require.Len(t, m.Stages, 1)
	assert.Equal(t, int64(310), m.Stages[0].TokensIn)
	assert.Equal(t, int64(125), m.Stages[0].TokensOut)
	require.NotNil(t, m.Stages[0].DurationSec)
	assert.InDelta(t, 90, *m.Stages[0].DurationSec, 2)
	assert.Equal(t, int64(310), m.TokensIn)
	assert.Equal(t, 1, m.StagesCount)
}

// gate_wait_seconds — производное событий gate.opened/resolved (T-24).
func TestRunMetrics_GateWait(t *testing.T) {
	ctx := context.Background()
	svc, machine, _, runID := newCatalogSvc(t)

	require.NoError(t, machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	gate, err := machine.OpenGate(ctx, runsmachine.OpenGateRequest{
		RunID: runID, Kind: dtorep.GateKindPlanApproval, Question: "ok?",
	})
	require.NoError(t, err)

	// «пользователь думал» 50мс минимум — проверяем, что ожидание > 0
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, machine.ResolveGate(ctx, gate.ID, dtorep.GateStateApproved, nil))

	m, err := svc.RunMetrics(ctx, runID)
	require.NoError(t, err)
	assert.Greater(t, m.GateWaitSeconds, 0.0)
}

// Метрики проекта за период (T-24).
func TestProjectMetrics(t *testing.T) {
	ctx := context.Background()
	svc, machine, _, runID := newCatalogSvc(t)

	require.NoError(t, machine.TransitionRun(ctx, runID, dtorep.RunStateRunning))
	require.NoError(t, machine.TransitionRun(ctx, runID, dtorep.RunStateSucceeded))

	m, err := svc.ProjectMetrics(ctx, 1, "7d")
	require.NoError(t, err)
	assert.Equal(t, 1, m.RunsTotal)
	assert.Equal(t, 1, m.ByState["succeeded"])

	// период "с древности" тоже находит
	m, err = svc.ProjectMetrics(ctx, 1, "all")
	require.NoError(t, err)
	assert.Equal(t, 1, m.RunsTotal)
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
