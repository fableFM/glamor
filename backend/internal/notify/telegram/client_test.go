package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testToken = "secret-token-123"

// newTestClient — HTTPClient поверх httptest-сервера с быстрым backoff.
func newTestClient(t *testing.T, handler http.HandlerFunc) BotClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := NewHTTPClient(testToken, srv.Client()).(*httpClient)
	c.baseURL = srv.URL
	c.backoffBase = time.Millisecond
	return c
}

func TestClientGetUpdatesAndSend(t *testing.T) {
	var lastPath atomic.Value
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		lastPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bot" + testToken + "/getUpdates":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": []map[string]any{{
					"update_id": 42,
					"message":   map[string]any{"message_id": 7, "chat": map[string]any{"id": 100}, "text": "/status"},
				}},
			})
		case "/bot" + testToken + "/sendMessage":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"message_id": 99},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		}
	})
	ctx := context.Background()

	updates, err := client.GetUpdates(ctx, 41, 1)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, int64(42), updates[0].UpdateID)
	assert.Equal(t, "/status", updates[0].Message.Text)
	assert.Equal(t, int64(100), updates[0].Message.Chat.ID)

	msgID, err := client.SendMessage(ctx, SendMessageRequest{
		ChatID: 100, Text: "hi",
		InlineKeyboard: [][]Button{{{Text: "Approve", CallbackData: "gate:g1:approve"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(99), msgID)

	require.NoError(t, client.AnswerCallbackQuery(ctx, "cb-1", "ok"))
	assert.Equal(t, "/bot"+testToken+"/answerCallbackQuery", lastPath.Load())
}

// Токен — только в пути запроса; в тексте ошибок его быть не должно.
func TestClientErrorDoesNotLeakToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Unauthorized"})
	})

	_, err := client.GetUpdates(context.Background(), 0, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unauthorized")
	assert.NotContains(t, err.Error(), testToken, "токен не должен попадать в ошибки")
}

// Сетевые ошибки ретраятся (обрыв соединения → повторная попытка).
func TestClientRetriesNetworkErrors(t *testing.T) {
	var attempts atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			// обрыв соединения без ответа
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hj.Hijack()
			require.NoError(t, err)
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
	})

	updates, err := client.GetUpdates(context.Background(), 0, 1)
	require.NoError(t, err)
	assert.Empty(t, updates)
	assert.Equal(t, int32(2), attempts.Load(), "первая попытка оборвалась → ретрай")

	// ошибки API (ok=false) НЕ ретраятся
	attempts.Store(0)
	apiFail := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Bad Request"})
	})
	_, err = apiFail.GetUpdates(context.Background(), 0, 1)
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "API-ошибки не ретраим")
}

// Отмена ctx останавливает long polling.
func TestClientRespectsContextCancel(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// долгий ответ (long poll): клиент уйдёт по таймауту раньше
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.GetUpdates(ctx, 0, 30)
	require.Error(t, err)
}
