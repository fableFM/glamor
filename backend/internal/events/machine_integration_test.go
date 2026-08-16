package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/repository/testdb"
	machine "github.com/fableFM/glamor/internal/usecase/runs"
)

// Машина (T-03) через Journal (T-04): переход состояния публикует событие
// в Hub после коммита — живой WS-подписчик видит его сразу.
func TestMachineTransitions_PublishToHub(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)

	hub := events.NewHub()
	journal := events.NewJournal(db, hub)

	m := machine.NewMachine(db, journal)
	m.SetTxExecutor(journal)

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
