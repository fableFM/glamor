package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/cstmerrors"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/repository"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	"github.com/fableFM/glamor/internal/repository/runs"
	"github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/pkg/uuid"
)

// Миграции: повторный накат — no-op, схема на месте.
func TestMigrations_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)

	require.NoError(t, repository.Migrate(ctx, db))

	for _, table := range []string{
		"projects", "pipelines", "runs", "run_stages", "events", "gates", "artifacts", "vendor_index",
	} {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name)
		require.NoError(t, err, "table %s must exist", table)
	}
}

// CAS-переход не применяется дважды.
func TestTransitionStage_NotAppliedTwice(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	repo := stages.NewRepository(db)

	stageID, err := repo.CreateStage(ctx, dtorep.CreateStageRequest{
		RunID: runID, StageKey: "plan", Iteration: 1, Harness: "kimi",
	})
	require.NoError(t, err)

	ok, err := repo.TransitionStageState(ctx, stageID, dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = repo.TransitionStageState(ctx, stageID, dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
	require.NoError(t, err)
	assert.False(t, ok, "second CAS from the same state must not apply")

	stage, err := repo.GetStageByID(ctx, stageID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateRunning, stage.State)
}

// Параллельные CAS-переходы одной стадии: применяется ровно один.
func TestTransitionStage_ParallelCAS(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	repo := stages.NewRepository(db)

	stageID, err := repo.CreateStage(ctx, dtorep.CreateStageRequest{
		RunID: runID, StageKey: "code", Iteration: 1, Harness: "kimi",
	})
	require.NoError(t, err)

	const workers = 8
	var wg sync.WaitGroup
	results := make([]bool, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.TransitionStageState(ctx, stageID,
				dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
			assert.NoError(t, err)
			results[i] = ok
		}()
	}
	wg.Wait()

	var applied int
	for _, ok := range results {
		if ok {
			applied++
		}
	}
	assert.Equal(t, 1, applied, "exactly one CAS transition must win")
}

// Параллельные вставки одного (run_id, stage_key, iteration): UNIQUE не даёт
// двух стадий одного ключа.
func TestCreateStage_UniqueKeyUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	repo := stages.NewRepository(db)

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = repo.CreateStage(ctx, dtorep.CreateStageRequest{
				RunID: runID, StageKey: "plan", Iteration: 1, Harness: "kimi",
			})
		}()
	}
	wg.Wait()

	var successes, duplicates int
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, cstmerrors.ErrDuplicate):
			duplicates++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, workers-1, duplicates)

	list, err := repo.ListStagesByRun(ctx, runID)
	require.NoError(t, err)
	assert.Len(t, list, 1, "UNIQUE(run_id, stage_key, iteration) must hold")
}

// Переход состояния + событие журнала атомарны (D-11): ошибка в tx откатывает
// и переход, и событие.
func TestTransitionAndEvent_Atomic(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	stagesRepo := stages.NewRepository(db)
	eventsRepo := eventsrep.NewRepository(db)
	txm := repository.NewTxManager(db)

	stageID, err := stagesRepo.CreateStage(ctx, dtorep.CreateStageRequest{
		RunID: runID, StageKey: "plan", Iteration: 1, Harness: "kimi",
	})
	require.NoError(t, err)

	boom := errors.New("boom")
	err = txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		ok, err := stages.NewTx(tx).TransitionStageState(ctx, stageID,
			dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
		require.NoError(t, err)
		require.True(t, ok)

		_, err = eventsrep.NewTx(tx).AppendEvent(ctx, dtorep.Event{
			RunID:       runID,
			StageID:     &stageID,
			Kind:        "stage.state_changed",
			PayloadJSON: `{"from":"pending","to":"running"}`,
		})
		require.NoError(t, err)

		return boom // симуляция падения после записи события
	})
	require.ErrorIs(t, err, boom)

	stage, err := stagesRepo.GetStageByID(ctx, stageID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStatePending, stage.State, "transition must be rolled back")

	evs, err := eventsRepo.ReplayEvents(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, evs, "event must be rolled back in the same tx")

	// Успешный случай: переход + событие коммитятся вместе.
	err = txm.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		ok, err := stages.NewTx(tx).TransitionStageState(ctx, stageID,
			dtorep.StageStatePending, dtorep.StageStateRunning, dtorep.StageTransitionFields{})
		require.NoError(t, err)
		require.True(t, ok)

		_, err = eventsrep.NewTx(tx).AppendEvent(ctx, dtorep.Event{
			RunID:       runID,
			StageID:     &stageID,
			Kind:        "stage.state_changed",
			PayloadJSON: `{"from":"pending","to":"running"}`,
		})
		return err
	})
	require.NoError(t, err)

	evs, err = eventsRepo.ReplayEvents(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 1)
	assert.Positive(t, evs[0].ID)
}

// Идемпотентность (D-12): повторный idempotency_key → ErrDuplicate.
func TestCreateRun_IdempotencyKeyUnique(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	existing, err := runs.NewRepository(db).GetRunByID(ctx, runID)
	require.NoError(t, err)

	err = runs.NewRepository(db).CreateRun(ctx, dtorep.CreateRunRequest{
		ID:                uuid.New(),
		ProjectID:         existing.ProjectID,
		PipelineVersionID: existing.PipelineVersionID,
		TaskText:          "retry",
		BaseBranch:        "main",
		Branch:            "glamor/retry",
		State:             dtorep.RunStateDraft,
		IdempotencyKey:    existing.IdempotencyKey, // дубликат
	})
	require.ErrorIs(t, err, cstmerrors.ErrDuplicate)
}

// CAS перехода рана + поля перехода стадии.
func TestTransitionRunAndStageFields(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	runsRepo := runs.NewRepository(db)
	stagesRepo := stages.NewRepository(db)

	ok, err := runsRepo.TransitionRunState(ctx, runID, dtorep.RunStateDraft, dtorep.RunStateRunning, nil)
	require.NoError(t, err)
	require.True(t, ok)

	finishedAt := time.Now().UTC()
	ok, err = runsRepo.TransitionRunState(ctx, runID, dtorep.RunStateRunning, dtorep.RunStateStopped, &finishedAt)
	require.NoError(t, err)
	require.True(t, ok)

	run, err := runsRepo.GetRunByID(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateStopped, run.State)
	require.NotNil(t, run.FinishedAt)

	stageID, err := stagesRepo.CreateStage(ctx, dtorep.CreateStageRequest{
		RunID: runID, StageKey: "code", Iteration: 1, Harness: "qwen",
	})
	require.NoError(t, err)

	pid := int64(4242)
	sessionID := "sess-1"
	ok, err = stagesRepo.TransitionStageState(ctx, stageID,
		dtorep.StageStatePending, dtorep.StageStateRunning,
		dtorep.StageTransitionFields{PID: &pid, SessionID: &sessionID, StartedAt: &finishedAt})
	require.NoError(t, err)
	require.True(t, ok)

	stage, err := stagesRepo.GetStageByID(ctx, stageID)
	require.NoError(t, err)
	assert.Equal(t, dtorep.StageStateRunning, stage.State)
	require.NotNil(t, stage.PID)
	assert.Equal(t, pid, *stage.PID)
	require.NotNil(t, stage.SessionID)
	assert.Equal(t, sessionID, *stage.SessionID)
}
