package events_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	eventsrep "github.com/fableFM/glamor/internal/repository/events"
	"github.com/fableFM/glamor/internal/repository/testdb"
)

func newJournal(t *testing.T) (*events.Journal, *events.Hub, string) {
	t.Helper()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	hub := events.NewHub()
	return events.NewJournal(db, hub), hub, runID
}

// Публикация — ПОСЛЕ коммита, не до; откат → события не рассылаются (D-11).
func TestJournal_PublishAfterCommit(t *testing.T) {
	ctx := context.Background()
	journal, hub, runID := newJournal(t)

	sub, unsubscribe := hub.Subscribe(runID)
	defer unsubscribe()

	errBoom := fmt.Errorf("boom")

	// откат: событие не публикуется и не сохраняется
	err := journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		require.NoError(t, journal.Append(ctx, tx, dtorep.Event{
			RunID: runID, Kind: "stream.text", PayloadJSON: `{"text":"rolled back"}`,
		}))
		return errBoom
	})
	require.ErrorIs(t, err, errBoom)

	select {
	case ev := <-sub:
		t.Fatalf("event from rolled back tx must not be published: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	evs, err := journal.Replay(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, evs)

	// коммит: событие публикуется после коммита и видно в Replay
	require.NoError(t, journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return journal.Append(ctx, tx, dtorep.Event{
			RunID: runID, Kind: "stream.text", PayloadJSON: `{"text":"committed"}`,
		})
	}))

	select {
	case ev := <-sub:
		assert.Equal(t, "stream.text", ev.Kind)
		assert.Positive(t, ev.ID)
	case <-time.After(time.Second):
		t.Fatal("committed event must be published")
	}

	evs, err = journal.Replay(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 1)
}

// Replay: догон по afterID без дублей и пропусков, страницами.
func TestJournal_ReplayPages(t *testing.T) {
	ctx := context.Background()
	journal, _, runID := newJournal(t)

	const total = 1200
	for i := 0; i < total; i += 100 {
		end := min(i+100, total)
		require.NoError(t, journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			for n := i; n < end; n++ {
				if err := journal.Append(ctx, tx, dtorep.Event{
					RunID: runID, Kind: "stream.text",
					PayloadJSON: fmt.Sprintf(`{"n":%d}`, n),
				}); err != nil {
					return err
				}
			}
			return nil
		}))
	}

	var seen []int64
	var afterID int64
	for {
		page, err := journal.Replay(ctx, runID, afterID, 500)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		for _, ev := range page {
			seen = append(seen, ev.ID)
			afterID = ev.ID
		}
	}
	require.Len(t, seen, total)
	for i := 1; i < len(seen); i++ {
		assert.Greater(t, seen[i], seen[i-1], "ids must be strictly increasing, no dups")
	}
}

// Медленный подписчик отключается, шина не тормозит (T-04 backpressure).
func TestHub_SlowSubscriberDropped(t *testing.T) {
	hub := events.NewHub()

	slow, unsubSlow := hub.Subscribe("run-1")
	defer unsubSlow()
	fast, unsubFast := hub.Subscribe("run-1")
	defer unsubFast()

	// fast читается конкурентно — модель живого WS-клиента
	const total = events.SubscriberBuffer + 10
	fastGot := make(chan int64, total)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range fast {
			fastGot <- ev.ID
		}
	}()

	// slow не читается: буфер (256) переполняется → drop
	for i := 1; i <= total; i++ {
		hub.Publish(dtorep.Event{ID: int64(i), RunID: "run-1", Kind: "stream.text", PayloadJSON: `{}`})
	}

	// fast получил всё
	for i := 1; i <= total; i++ {
		select {
		case id := <-fastGot:
			assert.Equal(t, int64(i), id)
		case <-time.After(time.Second):
			t.Fatal("fast subscriber must receive all events")
		}
	}

	// slow отключён: канал закрыт
	for {
		_, ok := <-slow
		if !ok {
			break
		}
	}
	assert.Equal(t, 1, hub.SubscriberCount())

	// шина жива: новые события доходят до fast
	hub.Publish(dtorep.Event{ID: 9999, RunID: "run-1", Kind: "stream.text", PayloadJSON: `{}`})
	select {
	case id := <-fastGot:
		assert.Equal(t, int64(9999), id)
	case <-time.After(time.Second):
		t.Fatal("bus must stay alive after dropping slow subscriber")
	}
	unsubFast()
	<-done
}

// Подписка на конкретный ран фильтрует чужие события; "*" — получает все.
func TestHub_RunFilter(t *testing.T) {
	hub := events.NewHub()

	subA, unsubA := hub.Subscribe("run-a")
	defer unsubA()
	subAll, unsubAll := hub.Subscribe(eventsrep.RunIDAll)
	defer unsubAll()

	hub.Publish(dtorep.Event{ID: 1, RunID: "run-b", Kind: "stream.text", PayloadJSON: `{}`})

	select {
	case ev := <-subA:
		t.Fatalf("run-a subscriber must not receive run-b events: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case ev := <-subAll:
		assert.Equal(t, "run-b", ev.RunID)
	case <-time.After(time.Second):
		t.Fatal("wildcard subscriber must receive all events")
	}
}

// StreamBatcher: запись батчами — одна транзакция на сброс; порог байт
// инициирует внеочередной сброс; Close сбрасывает остаток.
func TestStreamBatcher(t *testing.T) {
	ctx := context.Background()
	journal, _, runID := newJournal(t)

	batcher := events.NewStreamBatcher(journal, runID, nil)

	// маленькие события копятся до Flush
	for range 10 {
		batcher.Append("stream.text", []byte(`{"text":"chunk"}`))
	}
	evs, err := journal.Replay(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, evs, "no flush yet — batching works")

	require.NoError(t, batcher.Flush(ctx))
	evs, err = journal.Replay(ctx, runID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, evs, 10)

	// большой объём — сброс по таймеру/порогу через Start
	batcher.Start(ctx)
	defer func() { require.NoError(t, batcher.Close(ctx)) }()

	for range 5 {
		batcher.Append("stream.thinking", []byte(`{"text":"tick"}`))
	}
	require.Eventually(t, func() bool {
		evs, err := journal.Replay(ctx, runID, 0, 0)
		require.NoError(t, err)
		return len(evs) == 15
	}, 2*time.Second, 20*time.Millisecond, "interval flush must persist batched events")
}

// 100k событий: Replay страницами работает (T-04 acceptance).
func TestJournal_Replay100k(t *testing.T) {
	if testing.Short() {
		t.Skip("100k events test")
	}
	ctx := context.Background()
	journal, _, runID := newJournal(t)

	const total = 100_000
	const batch = 1000
	for i := 0; i < total; i += batch {
		require.NoError(t, journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			for n := i; n < i+batch; n++ {
				if err := journal.Append(ctx, tx, dtorep.Event{
					RunID: runID, Kind: "stream.text",
					PayloadJSON: fmt.Sprintf(`{"n":%d}`, n),
				}); err != nil {
					return err
				}
			}
			return nil
		}))
	}

	var count int
	var afterID, prevID int64
	for {
		page, err := journal.Replay(ctx, runID, afterID, 5000)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		for _, ev := range page {
			require.Greater(t, ev.ID, prevID, "strictly increasing ids")
			prevID = ev.ID
			afterID = ev.ID
			count++
		}
	}
	assert.Equal(t, total, count)

	latest, err := journal.LatestID(ctx)
	require.NoError(t, err)
	assert.Equal(t, prevID, latest)
}
