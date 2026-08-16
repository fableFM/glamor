package events_test

import (
	"context"
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
	machine "github.com/fableFM/glamor/internal/service/runsmachine"
)

// Машина (T-03) через Journal (T-04): переход состояния публикует событие
// в Hub после коммита — живой WS-подписчик видит его сразу.
func TestMachineTransitions_PublishToHub(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	hub := events.NewHub()
	journal := events.NewJournal(eventsrep.NewRepository(db), repository.NewTxManager(db), hub)

	m := machine.NewMachine(
		runsrep.NewRepository(db), stagesrep.NewRepository(db),
		gatesrep.NewRepository(db), pipelinesrep.NewRepository(db),
		projectsrep.NewRepository(db), notesrep.NewRepository(db),
		journal, journal)

	sub, unsubscribe := hub.Subscribe(runID)
	defer unsubscribe()

	require.NoError(t, m.TransitionRun(ctx, runID, dtorep.RunStateRunning))

	select {
	case ev := <-sub:
		assert.Equal(t, machine.EventKindRunStateChanged, ev.Kind)
		assert.JSONEq(t, `{"from":"draft","to":"running"}`, ev.PayloadJSON)
		assert.Positive(t, ev.ID)
	case <-time.After(time.Second):
		t.Fatal("machine transition must publish event to hub after commit")
	}

	// событие также в журнале (Replay)
	evs, err := journal.Replay(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 1)
}
