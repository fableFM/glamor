// Package telegram — TG-адаптер (T-19, D-70..73): облегчённый пульт в
// Telegram поверх in-process шины событий и API-сценариев ядра.
// client.go — минимальный HTTP-клиент Bot API (long polling, без webhook).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultAPIBase — базовый URL Bot API. Токен подставляется в путь запроса
// и НИКОГДА не попадает в логи/ошибки (в ошибках — только имя метода).
const defaultAPIBase = "https://api.telegram.org"

// BotClient — граница Bot API (интерфейс у потребителя: адаптер работает
// только с ним; в тестах — in-memory мок).
type BotClient interface {
	// GetUpdates — long polling: offset = last_update_id+1, timeoutSec —
	// серверный long-poll таймаут.
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error)
	// SendMessage отправляет сообщение; возвращает id отправленного сообщения.
	SendMessage(ctx context.Context, req SendMessageRequest) (messageID int64, err error)
	// AnswerCallbackQuery — ответ на inline-кнопку (снимает «часики»).
	AnswerCallbackQuery(ctx context.Context, id, text string) error
}

// Update — update Telegram (минимально нужные поля).
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message — сообщение Telegram (минимально нужные поля).
type Message struct {
	MessageID      int64    `json:"message_id"`
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text"`
	ReplyToMessage *Message `json:"reply_to_message,omitempty"`
}

// Chat — чат Telegram.
type Chat struct {
	ID int64 `json:"id"`
}

// CallbackQuery — нажатие inline-кнопки.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message,omitempty"`
}

// Button — inline-кнопка с callback_data.
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// SendMessageRequest — параметры sendMessage.
type SendMessageRequest struct {
	ChatID           int64
	Text             string
	ReplyToMessageID int64 // 0 — не reply
	InlineKeyboard   [][]Button
}

// httpClient — BotClient поверх net/http.
type httpClient struct {
	baseURL string
	token   string
	http    *http.Client
	// maxRetries/backoffBase — ретрай при сетевых ошибках (D-72: TG может
	// быть недоступен — poll/send не должны ронять адаптер).
	maxRetries  int
	backoffBase time.Duration
}

// NewHTTPClient собирает HTTP-клиент Bot API. hc == nil → дефолтный
// (таймаут чуть больше long-poll таймаута).
func NewHTTPClient(token string, hc *http.Client) BotClient {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &httpClient{
		baseURL:     defaultAPIBase,
		token:       token,
		http:        hc,
		maxRetries:  3,
		backoffBase: 500 * time.Millisecond,
	}
}

// apiResponse — конверт ответа Bot API.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

func (c *httpClient) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	body := map[string]any{
		"offset":  offset,
		"timeout": timeoutSec,
	}
	var updates []Update
	if err := c.call(ctx, "getUpdates", body, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *httpClient) SendMessage(ctx context.Context, req SendMessageRequest) (int64, error) {
	body := map[string]any{
		"chat_id": req.ChatID,
		"text":    req.Text,
	}
	if req.ReplyToMessageID != 0 {
		body["reply_to_message_id"] = req.ReplyToMessageID
	}
	if len(req.InlineKeyboard) > 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": req.InlineKeyboard}
	}

	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.call(ctx, "sendMessage", body, &result); err != nil {
		return 0, err
	}
	return result.MessageID, nil
}

func (c *httpClient) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	body := map[string]any{"callback_query_id": id}
	if text != "" {
		body["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", body, nil)
}

// call выполняет POST на /bot<token>/<method> с ретраями при сетевых
// ошибках (backoff 0.5s, 1s, 2s). Токен в текст ошибки не включается.
func (c *httpClient) call(ctx context.Context, method string, body map[string]any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("telegram %s: failed to marshal request: %w", method, err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.backoffBase << (attempt - 1)):
			}
		}

		err := c.do(ctx, method, payload, out)
		if err == nil {
			return nil
		}
		lastErr = err
		// ошибки API (4xx/5xx с ответом) не ретраим — только транспортные
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			break
		}
	}
	return lastErr
}

// apiError — Bot API ответило ok=false (не транспортная ошибка).
type apiError struct {
	description string
}

func (e *apiError) Error() string { return e.description }

func (c *httpClient) do(ctx context.Context, method string, payload []byte, out any) error {
	// токен — только в URL запроса; в ошибки/логи URL не включаем
	url := c.baseURL + "/bot" + c.token + "/" + method

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram %s: failed to build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: network error: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: failed to read response: %w", method, err)
	}

	var envelope apiResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("telegram %s: failed to decode response (status %d): %w",
			method, resp.StatusCode, err)
	}
	if !envelope.OK {
		return fmt.Errorf("telegram %s: api error: %w", method, &apiError{description: envelope.Description})
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram %s: failed to decode result: %w", method, err)
		}
	}
	return nil
}
