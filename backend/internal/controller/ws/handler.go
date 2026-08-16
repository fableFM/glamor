// Package ws — WebSocket-контроллер журнала событий (D-04, T-04):
// endpoint /ws с догоняющей синхронизацией (Replay → synced → live).
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/events"
)

const componentName = "controller/ws"

const (
	replayPageSize = 500
	writeTimeout   = 10 * time.Second
	pingInterval   = 30 * time.Second
	maxMessageSize = 1 << 20 // защитный лимит на входящие (клиент ничего не шлёт)
)

// Handler — http.Handler endpoint'а /ws.
//
// Параметры: run_id (по умолчанию "*" — все раны), last_event_id (догон,
// по умолчанию 0), token (если настроен токен демона, D-08).
//
// Протокол: сначала события-догон (id > last_event_id), затем сообщение
// {"type":"synced","last_event_id":N} — граница replay/live, затем
// live-поток. Переполнение буфера клиента → закрытие со статусом
// "resync": клиент переподключается с последним полученным last_event_id.
type Handler struct {
	journal *events.Journal
	hub     *events.Hub
	token   string
}

func NewHandler(journal *events.Journal, hub *events.Hub, token string) *Handler {
	return &Handler{journal: journal, hub: hub, token: token}
}

// eventMessage — wire-формат события (соответствует components.Event в
// api/openapi.yaml).
type eventMessage struct {
	ID      int64           `json:"id"`
	RunID   string          `json:"run_id"`
	StageID *int64          `json:"stage_id"`
	TS      time.Time       `json:"ts"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type syncedMessage struct {
	Type        string `json:"type"` // "synced"
	LastEventID int64  `json:"last_event_id"`
}

func mapEventToMessage(ev dtorep.Event) eventMessage {
	return eventMessage{
		ID:      ev.ID,
		RunID:   ev.RunID,
		StageID: ev.StageID,
		TS:      ev.TS,
		Kind:    ev.Kind,
		Payload: json.RawMessage(ev.PayloadJSON),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.token != "" && r.URL.Query().Get("token") != h.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		runID = dtorep.RunIDAll
	}
	lastEventID, err := strconv.ParseInt(r.URL.Query().Get("last_event_id"), 10, 64)
	if err != nil {
		lastEventID = 0
	}

	// localhost-демон: Origin не проверяем (клиенты — UI из vite/tauri, CLI).
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		slog.WarnContext(r.Context(), "ws accept failed",
			slog.String("component", componentName), slog.String("error", err.Error()))
		return
	}
	conn.SetReadLimit(maxMessageSize)

	h.serve(r.Context(), conn, runID, lastEventID)
}

func (h *Handler) serve(ctx context.Context, conn *websocket.Conn, runID string, afterID int64) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "bye") }()

	// read pump: клиент ничего не шлёт, но читать нужно для control frames
	// (pong/close); ошибка чтения = конец соединения.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	// 1. догон из журнала
	var err error
	afterID, err = h.replay(ctx, conn, runID, afterID)
	if err != nil {
		h.closeWithError(conn, "replay failed", err)
		return
	}

	// 2. граница replay/live
	if err := h.write(ctx, conn, syncedMessage{Type: "synced", LastEventID: afterID}); err != nil {
		return
	}

	// 3. live-поток
	sub, unsubscribe := h.hub.Subscribe(runID)
	defer unsubscribe()

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				// подписку дропнули (медленный клиент) — клиент пересинхронизируется
				_ = conn.Close(websocket.StatusGoingAway, "resync required")
				return
			}
			if err := h.write(ctx, conn, mapEventToMessage(ev)); err != nil {
				return
			}
		case <-ping.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		}
	}
}

// replay отправляет все события с id > afterID страницами и возвращает
// id последнего отправленного события.
func (h *Handler) replay(ctx context.Context, conn *websocket.Conn, runID string, afterID int64) (int64, error) {
	for {
		page, err := h.journal.Replay(ctx, runID, afterID, replayPageSize)
		if err != nil {
			return afterID, err
		}
		for _, ev := range page {
			if err := h.write(ctx, conn, mapEventToMessage(ev)); err != nil {
				return afterID, err
			}
			afterID = ev.ID
		}
		if len(page) < replayPageSize {
			return afterID, nil
		}
	}
}

func (h *Handler) write(ctx context.Context, conn *websocket.Conn, v any) error {
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := wsjson.Write(writeCtx, conn, v); err != nil {
		return fmt.Errorf("ws write failed: %w", err)
	}
	return nil
}

func (h *Handler) closeWithError(conn *websocket.Conn, msg string, err error) {
	slog.Warn(msg, slog.String("component", componentName), slog.String("error", err.Error()))
	_ = conn.Close(websocket.StatusInternalError, msg)
}

// IsNormalClose — ошибка чтения/записи является штатным закрытием соединения.
func IsNormalClose(err error) bool {
	var closeErr websocket.CloseError
	return errors.As(err, &closeErr) &&
		(closeErr.Code == websocket.StatusNormalClosure || closeErr.Code == websocket.StatusGoingAway)
}
