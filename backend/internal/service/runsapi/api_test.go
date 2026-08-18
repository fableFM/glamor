package runsapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/repository"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	gatesrep "github.com/fableFM/glamor/internal/repository/gates"
	notesrep "github.com/fableFM/glamor/internal/repository/notes"
	pipelinesrep "github.com/fableFM/glamor/internal/repository/pipelines"
	projectsrep "github.com/fableFM/glamor/internal/repository/projects"
	runsrep "github.com/fableFM/glamor/internal/repository/runs"
	stagesrep "github.com/fableFM/glamor/internal/repository/stages"
	"github.com/fableFM/glamor/internal/repository/testdb"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/runsmachine"
)

type fixture struct {
	svc     *runsapi.Service
	journal *events.Journal
	hub     *events.Hub
	projectID,
	pipelineID int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db := testdb.New(t)

	projects := projectsrep.NewRepository(db)
	projectID, err := projects.CreateProject(ctx, dtorep.CreateProjectRequest{
		Path: t.TempDir(), Name: "test", DefaultBranch: "main",
	})
	require.NoError(t, err)

	pipelines := pipelinesrep.NewRepository(db)
	pipelineID, err := pipelines.CreatePipeline(ctx, dtorep.CreatePipelineRequest{
		Name: "default", Version: 1,
		SpecJSON: `{"stages":[{"key":"plan","harness":"kimi"}]}`,
	})
	require.NoError(t, err)

	hub := events.NewHub()
	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), hub)
	machine := runsmachine.NewMachine(
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		gatesrep.NewRepository(db), pipelines, projects,
		notesrep.NewRepository(db), journal, journal)
	svc := runsapi.New(machine, journal, journal,
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		notesrep.NewRepository(db), projects, pipelines,
		gatesrep.NewRepository(db))

	return &fixture{svc: svc, journal: journal, hub: hub, projectID: projectID, pipelineID: pipelineID}
}

func (f *fixture) createRun(t *testing.T, key string) (*dtorep.Run, bool) {
	t.Helper()
	run, alreadyExisted, err := f.svc.CreateRun(context.Background(), runsapi.CreateRunParams{
		ProjectID:         f.projectID,
		PipelineVersionID: f.pipelineID,
		TaskText:          "test task",
		IdempotencyKey:    key,
	})
	require.NoError(t, err)
	return run, alreadyExisted
}

func createdEvents(t *testing.T, evs []dtorep.Event) []dtorep.Event {
	t.Helper()
	var out []dtorep.Event
	for _, ev := range evs {
		if ev.Kind == runsmachine.EventKindRunCreated {
			out = append(out, ev)
		}
	}
	return out
}

// TestCreateRunWritesRunCreatedEvent: создание рана пишет run.created в том же
// tx (D-11) — событие видно в replay и долетает live-подписчику Hub.
func TestCreateRunWritesRunCreatedEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	live, unsubscribe := f.hub.Subscribe(dtorep.RunIDAll)
	defer unsubscribe()

	run, alreadyExisted := f.createRun(t, "key-1")
	require.False(t, alreadyExisted)

	// live-доставка после коммита
	select {
	case ev := <-live:
		assert.Equal(t, runsmachine.EventKindRunCreated, ev.Kind)
		assert.Equal(t, run.ID, ev.RunID)
	case <-time.After(2 * time.Second):
		t.Fatal("run.created не долетел до live-подписчика")
	}

	// replay: ровно одно событие, payload — объект рана
	evs, err := f.journal.Replay(ctx, run.ID, 0, 100)
	require.NoError(t, err)
	created := createdEvents(t, evs)
	require.Len(t, created, 1)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(created[0].PayloadJSON), &payload))
	assert.Equal(t, run.ID, payload["id"])
	assert.Equal(t, float64(f.projectID), payload["project_id"])
	assert.Equal(t, float64(f.pipelineID), payload["pipeline_version_id"])
	assert.Equal(t, "test task", payload["task_text"])
	assert.Equal(t, run.Branch, payload["branch"])
	assert.Equal(t, string(dtorep.RunStateDraft), payload["state"])
	assert.NotEmpty(t, payload["created_at"])
}

// TestCreateRunIdempotentNoDuplicateEvent: повтор по Idempotency-Key (D-12)
// возвращает первый ран и НЕ пишет второе событие run.created.
func TestCreateRunIdempotentNoDuplicateEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first, alreadyExisted := f.createRun(t, "key-dup")
	require.False(t, alreadyExisted)
	second, alreadyExisted := f.createRun(t, "key-dup")
	require.True(t, alreadyExisted)
	assert.Equal(t, first.ID, second.ID)

	evs, err := f.journal.Replay(ctx, first.ID, 0, 100)
	require.NoError(t, err)
	assert.Len(t, createdEvents(t, evs), 1)
}
