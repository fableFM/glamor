package opencode

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/harness"
)

// Схема NDJSON (`opencode run --format json`, 1.2.27) по матрице
// «## 5. opencode» + takopi.dev cheatsheet. Конверт:
//
//	{"type","timestamp"(epoch ms),"sessionID","part"? | "error"?}
//
// Типы: step_start, text, reasoning, tool_use, step_finish, error.
// Модели в событиях НЕТ (баг #40544) — Event.Model не заполняется.
type wireEvent struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"` // epoch ms
	SessionID string          `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
	Error     *wireError      `json:"error"`
}

// wireError — error-конверт: {name, data:{message, statusCode,
// isRetryable, …}}. Классификация retriable — по подстрокам сообщения,
// как у соседних адаптеров (kimi/qwen), а не по data.isRetryable —
// единообразие эвристики supervisor'а (T-09).
type wireError struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
	} `json:"data"`
}

// wirePart — union part-полей всех типов (part.type: step-start | text |
// reasoning | tool | step-finish). Диспетчеризация — по верхнему
// envelope type, part.type носит справочный характер.
type wirePart struct {
	Type   string         `json:"type"`
	Text   string         `json:"text"`   // text/reasoning
	CallID string         `json:"callID"` // tool
	Tool   string         `json:"tool"`   // tool
	State  *wireToolState `json:"state"`  // tool
	Reason string         `json:"reason"` // step-finish: stop|tool-calls|…
	Cost   *float64       `json:"cost"`   // step-finish, USD
	Tokens *wireTokens    `json:"tokens"` // step-finish
}

// wireToolState — состояние tool-part'а. CLI эмитит tool_use ТОЛЬКО по
// завершении: status — "completed" или "error" (pending/running в
// JSON-вывод не попадают — матрица, takopi.dev).
type wireToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error"` // при status=="error"
	Title  string          `json:"title"`
}

// wireTokens — step-finish.tokens {input, output, reasoning,
// cache:{read, write}} (баг #40544: модель среди ключей отсутствует).
type wireTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

// ParseStream мапит одну строку stdout в нормализованные события.
// Не-JSON/битые строки (обрезки при краше) и валидный JSON неизвестной
// формы → EventRaw, НЕ ошибка и не паника (D-13; матрица, следствие «б»).
// Пустые строки не порождают событий.
func (adapter) ParseStream(line []byte) []harness.Event {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}

	var ev wireEvent
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return []harness.Event{rawEvent(time.Now(), trimmed)}
	}
	ts := eventTime(ev.Timestamp)

	switch ev.Type {
	case "step_start":
		// Первый step_start — носитель sessionID (матрица, «Рекомендации»:
		// opencode session.init = первый step_start). Повторные step_start
		// (каждый шаг) тоже мапим в session.init — SessionID тот же,
		// ExtractSessionID берёт первое подходящее событие.
		return []harness.Event{{
			Kind:      harness.EventSessionInit,
			TS:        ts,
			SessionID: ev.SessionID,
		}}
	case "text":
		return parseText(ts, ev, trimmed)
	case "reasoning":
		return parseReasoning(ts, ev, trimmed)
	case "tool_use":
		return parseToolUse(ts, ev, trimmed)
	case "step_finish":
		return parseStepFinish(ts, ev, trimmed)
	case "error":
		return []harness.Event{errorEvent(ts, ev)}
	default:
		// error-поле может прийти и с нестандартным type — не теряем.
		if ev.Error != nil {
			return []harness.Event{errorEvent(ts, ev)}
		}
		// Валидный JSON неизвестного типа (новые события CLI) — в raw.
		return []harness.Event{rawEvent(ts, trimmed)}
	}
}

// eventTime — timestamp конверта (epoch ms); при отсутствии — время
// парсинга (конвенция harness.Event.TS).
func eventTime(ts int64) time.Time {
	if ts <= 0 {
		return time.Now()
	}
	return time.UnixMilli(ts)
}

// parseText: text-событие → assistant.text_delta. Гранулярность частичных
// text-событий UNVERIFIED (матрица, «Рекомендации»: «частичные text-события —
// это скорее дельты») — выбран text_delta; supervisor склеивает дельты,
// полный текст блока не гарантирован.
func parseText(ts time.Time, ev wireEvent, line []byte) []harness.Event {
	part, ok := parsePart(ev)
	if !ok {
		return []harness.Event{rawEvent(ts, line)}
	}
	if part.Text == "" {
		return nil // пустой text-part — шум
	}
	return []harness.Event{{
		Kind:      harness.EventAssistantTextDelta,
		TS:        ts,
		Text:      part.Text,
		SessionID: ev.SessionID,
	}}
}

// parseReasoning: reasoning-событие → assistant.thinking (CLI шлёт при
// `--thinking`; матрица, «JSON»).
func parseReasoning(ts time.Time, ev wireEvent, line []byte) []harness.Event {
	part, ok := parsePart(ev)
	if !ok {
		return []harness.Event{rawEvent(ts, line)}
	}
	if part.Text == "" {
		return nil
	}
	return []harness.Event{{
		Kind:      harness.EventAssistantThinking,
		TS:        ts,
		Text:      part.Text,
		SessionID: ev.SessionID,
	}}
}

// parseToolUse: tool_use эмитится CLI ТОЛЬКО по завершении (status
// "completed"|"error"), события «tool started» нет (матрица, takopi.dev).
// Зафиксированное отступление от семантики tool_call.*: эмитим ПАРУ
// tool_call.start + tool_result с одинаковым TS и CallID (start=end) —
// у начала вызова известны и ввод, и вывод одновременно.
func parseToolUse(ts time.Time, ev wireEvent, line []byte) []harness.Event {
	part, ok := parsePart(ev)
	if !ok || part.State == nil {
		return []harness.Event{rawEvent(ts, line)}
	}

	start := harness.Event{
		Kind:      harness.EventToolCallStart,
		TS:        ts,
		CallID:    part.CallID,
		Tool:      part.Tool,
		ToolInput: part.State.Input,
		SessionID: ev.SessionID,
	}

	output := part.State.Output
	isErr := part.State.Status == "error"
	if isErr && part.State.Error != "" {
		output = part.State.Error
	}
	result := harness.Event{
		Kind:       harness.EventToolResult,
		TS:         ts,
		CallID:     part.CallID,
		Tool:       part.Tool,
		ToolOutput: harness.TruncateToolOutput(output),
		ToolIsErr:  isErr,
		SessionID:  ev.SessionID,
	}
	return []harness.Event{start, result}
}

// parseStepFinish: step_finish → usage (tokens + cost). ВАЖНО: финальный
// step_finish может НЕ прийти (баг #26855) — usage в стриме не
// гарантирован (null-семантика, матрица следствие «г»); post-hoc fallback —
// `opencode export <sessionID>` (см. Capabilities.CostReporting).
// Терминальный result адаптер НЕ синтезирует: ParseStream построчный и
// stateless, статус завершения — из exit code процесса (supervisor, T-09).
func parseStepFinish(ts time.Time, ev wireEvent, line []byte) []harness.Event {
	part, ok := parsePart(ev)
	if !ok {
		return []harness.Event{rawEvent(ts, line)}
	}
	if part.Tokens == nil && part.Cost == nil {
		// step-finish без метрик — не наш формат, строку не теряем.
		return []harness.Event{rawEvent(ts, line)}
	}

	usage := &harness.Usage{CostUSD: part.Cost}
	if part.Tokens != nil {
		usage.Input = part.Tokens.Input
		// reasoning-токены — часть output у провайдеров; отдельного поля
		// в harness.Usage нет → складываем в Output.
		usage.Output = part.Tokens.Output + part.Tokens.Reasoning
		usage.CacheRead = part.Tokens.Cache.Read
		usage.CacheWrite = part.Tokens.Cache.Write
	}
	return []harness.Event{{
		Kind:      harness.EventUsage,
		TS:        ts,
		SessionID: ev.SessionID,
		Usage:     usage,
	}}
}

// parsePart разбирает part-поле; отсутствующий/битый part — не ок.
func parsePart(ev wireEvent) (wirePart, bool) {
	var part wirePart
	if len(ev.Part) == 0 {
		return part, false
	}
	if err := json.Unmarshal(ev.Part, &part); err != nil {
		return part, false
	}
	return part, true
}

// errorEvent — error-конверт с классификацией retriable по подстрокам
// (сигнал для auto-resume supervisor'а, T-09), как у kimi/qwen.
func errorEvent(ts time.Time, ev wireEvent) harness.Event {
	var text string
	if ev.Error != nil {
		text = ev.Error.Data.Message
		if text == "" {
			text = ev.Error.Name
		}
	}
	if text == "" {
		text = "opencode: error-событие без сообщения"
	}
	return harness.Event{
		Kind:      harness.EventError,
		TS:        ts,
		Text:      text,
		SessionID: ev.SessionID,
		Err: &harness.StreamError{
			Message:   text,
			Retriable: isRetriable(text),
		},
	}
}

func rawEvent(ts time.Time, line []byte) harness.Event {
	return harness.Event{Kind: harness.EventRaw, TS: ts, Text: string(line)}
}

// retriableSubstrings — эвристика retriable-ошибок (rate limit / сеть /
// 5xx провайдера), унифицирована с qwen-адаптером. Регистр не важен.
var retriableSubstrings = []string{
	"rate limit", "rate_limit", "too many requests",
	"429", "529",
	"overloaded",
	"500", "502", "503", "504",
	"internal server error", "bad gateway", "service unavailable",
	"gateway timeout", "timeout", "timed out",
	"connection reset", "connection refused", "econnreset", "econnrefused",
	"network", "socket hang up", "temporarily unavailable",
}

func isRetriable(msg string) bool {
	m := strings.ToLower(msg)
	for _, sub := range retriableSubstrings {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}
