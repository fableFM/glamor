package kimi

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/harness"
)

// wireMessage — строка stream-json kimi (NDJSON, OpenAI-подобный формат;
// матрица, секция kimi). thinking/usage в stdout отсутствуют — не парсим.
type wireMessage struct {
	Role       string          `json:"role"`
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name"`         // tool-сообщение
	ToolCallID string          `json:"tool_call_id"` // tool-сообщение
	Message    string          `json:"message"`      // error-подобные сообщения
	ToolCalls  []wireToolCall  `json:"tool_calls"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// ParseStream мапит одну строку stdout в нормализованные события.
// Не-JSON строки (kimi мусорит stdout зеркалом вывода foreground-Bash —
// Kent production) и битые/обрезанные строки → EventRaw, НЕ ошибка и
// не паника (D-13). Пустые строки не порождают событий.
func (a *adapter) ParseStream(line []byte) []harness.Event {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}

	var msg wireMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return []harness.Event{rawEvent(line)}
	}

	switch {
	case msg.Role == "meta":
		return a.parseMeta(msg, line)
	case msg.Role == "assistant":
		return a.parseAssistant(msg, line)
	case msg.Role == "tool" || msg.Type == "tool_result":
		return a.parseToolResult(msg)
	case msg.Role == "error" || strings.Contains(msg.Type, "error"):
		return []harness.Event{errorEvent(msg)}
	default:
		// Валидный JSON неизвестной формы — не теряем строку.
		return []harness.Event{rawEvent(line)}
	}
}

// parseMeta: session.resume_hint — ключевое событие с resumable session id;
// system.version — версия стрима без id. Оба → session.init (матрица,
// «Рекомендации»: kimi session.init = system.version + resume_hint).
func (a *adapter) parseMeta(msg wireMessage, line []byte) []harness.Event {
	switch msg.Type {
	case "session.resume_hint", "system.version":
		return []harness.Event{{
			Kind:      harness.EventSessionInit,
			TS:        time.Now(),
			SessionID: msg.SessionID,
		}}
	case "":
		// meta без type — не наш формат.
		return []harness.Event{rawEvent(line)}
	default:
		if strings.Contains(msg.Type, "error") {
			return []harness.Event{errorEvent(msg)}
		}
		return []harness.Event{rawEvent(line)}
	}
}

// parseAssistant: content — полный текст (дельт у kimi нет); tool_calls →
// по событию tool_call.start на каждый вызов. Одно сообщение может нести
// и текст, и вызовы — события идут в порядке: текст, затем вызовы.
func (a *adapter) parseAssistant(msg wireMessage, line []byte) []harness.Event {
	var content string
	if len(msg.Content) > 0 {
		if err := json.Unmarshal(msg.Content, &content); err != nil {
			// content не строка (массив блоков и т.п.) — формат не наш.
			return []harness.Event{rawEvent(line)}
		}
	}

	var events []harness.Event
	if content != "" {
		events = append(events, harness.Event{
			Kind:      harness.EventAssistantText,
			TS:        time.Now(),
			Text:      content,
			SessionID: msg.SessionID,
		})
	}
	for _, tc := range msg.ToolCalls {
		events = append(events, harness.Event{
			Kind:      harness.EventToolCallStart,
			TS:        time.Now(),
			CallID:    tc.ID,
			Tool:      tc.Function.Name,
			ToolInput: toolInput(tc.Function.Arguments),
			SessionID: msg.SessionID,
		})
	}
	if len(events) == 0 {
		// Пустое assistant-сообщение (ни текста, ни вызовов) — шум.
		return nil
	}
	return events
}

// parseToolResult: tool-сообщение после tool_calls (OpenAI-подобный
// role:"tool" либо type:"tool_result" — по матрице). Вывод усечён через
// harness.TruncateToolOutput, полный — в логе рана (T-09).
func (a *adapter) parseToolResult(msg wireMessage) []harness.Event {
	content := msg.Message
	if len(msg.Content) > 0 {
		var s string
		if err := json.Unmarshal(msg.Content, &s); err == nil {
			content = s
		} else {
			content = string(msg.Content)
		}
	}
	return []harness.Event{{
		Kind:       harness.EventToolResult,
		TS:         time.Now(),
		CallID:     msg.ToolCallID,
		Tool:       msg.Name,
		ToolOutput: harness.TruncateToolOutput(content),
		SessionID:  msg.SessionID,
	}}
}

// toolInput нормализует arguments вызова: в OpenAI-подобном формате это
// JSON-строка — разворачиваем её в ToolInput как есть (уже валидный JSON).
// Не-JSON строку оборачиваем JSON-строкой, объект передаём без изменений.
func toolInput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		// arguments — объект, а не строка: оставляем как есть.
		return raw
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

// errorEvent — error-подобное сообщение стрима с классификацией
// retriable (сигнал для auto-resume, T-09).
func errorEvent(msg wireMessage) harness.Event {
	text := msg.Message
	if text == "" {
		text = msg.Type
	}
	return harness.Event{
		Kind:      harness.EventError,
		TS:        time.Now(),
		Text:      text,
		SessionID: msg.SessionID,
		Err: &harness.StreamError{
			Message:   text,
			Retriable: classifyRetriable(text),
		},
	}
}

func rawEvent(line []byte) harness.Event {
	return harness.Event{
		Kind: harness.EventRaw,
		TS:   time.Now(),
		Text: string(line),
	}
}

// retriableSubstrings — признаки сетевых/rate-limit/5xx ошибок провайдера
// (матрица: kimi шлёт ошибки в stderr/exit code, но error-сообщения в
// потоке классифицируем по тем же подстрокам).
var retriableSubstrings = []string{
	"rate limit",
	"timeout",
	"connection",
	"429",
	"500",
	"502",
	"503",
	"overloaded",
}

// classifyRetriable определяет, стоит ли ошибку ретраить (auto-resume).
func classifyRetriable(msg string) bool {
	m := strings.ToLower(msg)
	for _, sub := range retriableSubstrings {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}
