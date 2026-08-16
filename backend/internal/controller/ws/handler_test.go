package ws_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/controller/ws"
	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/repository/testdb"
)

type fixture struct {
	journal *events.Journal
	hub     *events.Hub
	server  *httptest.Server
	runID   string
}

func newFixture(t *testing.T, token string) *fixture {
	t.Helper()
	db := testdb.New(t)
	runID := testdb.SeedRun(t, db)
	hub := events.NewHub()
	journal := events.NewJournal(db, hub)
	server := httptest.NewServer(ws.NewHandler(journal, hub, token))
	t.Cleanup(server.Close)
	return &fixture{journal: journal, hub: hub, server: server, runID: runID}
}

func (f *fixture) appendEvents(t *testing.T, n int, kindPrefix string) []int64 {
	t.Helper()
	ctx := context.Background()
	var ids []int64
	err := f.journal.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i := range n {
			ev := dtorep.Event{
				RunID:       f.runID,
				Kind:        fmt.Sprintf("%s.%d", kindPrefix, i),
				PayloadJSON: fmt.Sprintf(`{"i":%d}`, i),
			}
			if err := f.journal.Append(ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	evs, err := f.journal.Replay(ctx, f.runID, 0, 0)
	require.NoError(t, err)
	for _, ev := range evs {
		ids = append(ids, ev.ID)
	}
	return ids
}

func (f *fixture) dial(t *testing.T, query string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+f.server.URL[len("http"):]+"/ws?"+query, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

type eventMessage struct {
	ID      int64           `json:"id"`
	RunID   string          `json:"run_id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type syncedMessage struct {
	Type        string `json:"type"`
	LastEventID int64  `json:"last_event_id"`
}

// readRaw читает одно сообщение и определяет его тип (event | synced).
func readRaw(t *testing.T, conn *websocket.Conn) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg json.RawMessage
	require.NoError(t, wsjson.Read(ctx, conn, &msg))
	return msg
}

func isSynced(msg json.RawMessage) (syncedMessage, bool) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &probe); err != nil || probe.Type != "synced" {
		return syncedMessage{}, false
	}
	var sm syncedMessage
	_ = json.Unmarshal(msg, &sm)
	return sm, true
}

// Клиент с last_event_id=N после реконнекта получает ровно события >N,
// без дублей и пропусков (T-04 acceptance).
func TestWs_ReplayAfterReconnect(t *testing.T) {
	f := newFixture(t, "")
	ids := f.appendEvents(t, 30, "stream.text")
	require.Len(t, ids, 30)

	// «первое подключение»: клиент получил 10 событий и отвалился
	cut := ids[9]

	conn := f.dial(t, fmt.Sprintf("run_id=%s&last_event_id=%d", f.runID, cut))

	var got []eventMessage
	var synced syncedMessage
	for range 21 { // 20 событий-догона + synced
		msg := readRaw(t, conn)
		if sm, ok := isSynced(msg); ok {
			synced = sm
			continue
		}
		var ev eventMessage
		require.NoError(t, json.Unmarshal(msg, &ev))
		got = append(got, ev)
	}

	require.Len(t, got, 20, "ровно события > last_event_id")
	for i, ev := range got {
		assert.Equal(t, ids[10+i], ev.ID, "порядок и состав без дублей/пропусков")
	}
	assert.Equal(t, ids[29], synced.LastEventID, "synced — граница replay/live")
}

// Live-поток после synced: новые события доходят подписчику.
func TestWs_LiveAfterSync(t *testing.T) {
	f := newFixture(t, "")
	f.appendEvents(t, 5, "stream.text")

	conn := f.dial(t, "run_id="+f.runID)

	// дочитываем догон + synced
	for {
		msg := readRaw(t, conn)
		if _, ok := isSynced(msg); ok {
			break
		}
	}

	// live-событие
	f.appendEvents(t, 1, "run.state_changed")

	msg := readRaw(t, conn)
	var ev eventMessage
	require.NoError(t, json.Unmarshal(msg, &ev))
	assert.Equal(t, "run.state_changed.0", ev.Kind)
}

// Подписка run_id=* получает события всех ранов.
func TestWs_WildcardSubscription(t *testing.T) {
	f := newFixture(t, "")
	conn := f.dial(t, "run_id=*")

	msg := readRaw(t, conn)
	_, ok := isSynced(msg)
	require.True(t, ok, "пустой журнал → сразу synced")

	f.appendEvents(t, 2, "stream.text")
	for range 2 {
		msg := readRaw(t, conn)
		var ev eventMessage
		require.NoError(t, json.Unmarshal(msg, &ev))
		assert.Equal(t, f.runID, ev.RunID)
	}
}

// Неверный token → 401 (D-08).
func TestWs_TokenAuth(t *testing.T) {
	f := newFixture(t, "secret")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+f.server.URL[len("http"):]+"/ws?token=wrong", nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 401, resp.StatusCode)

	// правильный токен — коннектится
	conn := f.dial(t, "token=secret")
	msg := readRaw(t, conn)
	_, ok := isSynced(msg)
	require.True(t, ok)
}

// Нагрузочный мини-тест: 10 WS-клиентов на активном ране — все получают
// live-поток, демон не деградирует (T-04 acceptance).
func TestWs_TenClients(t *testing.T) {
	f := newFixture(t, "")

	const clients = 10
	conns := make([]*websocket.Conn, 0, clients)
	for range clients {
		conn := f.dial(t, "run_id="+f.runID)
		msg := readRaw(t, conn)
		_, ok := isSynced(msg)
		require.True(t, ok)
		conns = append(conns, conn)
	}
	assert.Equal(t, clients, f.hub.SubscriberCount())

	const eventsN = 200
	f.appendEvents(t, eventsN, "stream.text")

	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			for range eventsN {
				msg := readRaw(t, c)
				var ev eventMessage
				require.NoError(t, json.Unmarshal(msg, &ev))
			}
		}(conn)
	}
	wg.Wait()
	assert.Equal(t, clients, f.hub.SubscriberCount(), "все клиенты живы")
}
