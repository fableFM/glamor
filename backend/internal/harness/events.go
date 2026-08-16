package harness

import (
	"encoding/json"
	"time"
)

// Нормализованные типы событий (D-41; enum — из «Рекомендаций»
// capability-матрицы, доп. T-06 от 2026-08-14).
const (
	EventSessionInit          = "session.init"
	EventAssistantText        = "assistant.text"
	EventAssistantTextDelta   = "assistant.text_delta"
	EventAssistantThinking    = "assistant.thinking"
	EventAssistantThinkingDlt = "assistant.thinking_delta"
	EventToolCallStart        = "tool_call.start"
	EventToolCallEnd          = "tool_call.end"
	EventToolResult           = "tool_result"
	EventUsage                = "usage"
	EventResult               = "result" // терминальное: status + текст (+structured_output?)
	EventStructuredOutput     = "structured_output"
	EventError                = "error"
	EventErrorRetry           = "error.retry" // провайдер ретраит (claude api_retry и т.п.)
	EventRaw                  = "raw"         // нераспознанная строка stdout (D-13)
)

// Event — нормализованное событие harness-стрима.
type Event struct {
	Kind string    // одна из констант Event*
	TS   time.Time // время парсинга (не все CLI шлют ts)
	Text string    // assistant.text(_delta)/thinking(_delta)/raw/result

	// tool_call.*: имя тулзы и ввод/вывод
	CallID     string
	Tool       string
	ToolInput  json.RawMessage
	ToolOutput string // усечённый (ToolOutputLimit), полный — в логе рана (T-09)
	ToolIsErr  bool

	Usage  *Usage       // usage/result: токены/стоимость (nullable — не гарантирован)
	Result *Result      // result: терминальный статус
	Err    *StreamError // error/error.retry

	SessionID string // session.init / result: resumable session id
	Model     string // session.init / usage
}

// Usage — метрики токенов (null-семантика: не все harness'ы сообщают).
type Usage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	CostUSD    *float64
}

// Result — терминальное событие стрима.
type Result struct {
	Status           string // success | error
	StructuredOutput json.RawMessage
}

// StreamError — ошибка стрима с признаком retriable (сеть, rate limit,
// 5xx провайдера) — сигнал для auto-resume (T-09).
type StreamError struct {
	Message   string
	Retriable bool
}

// ToolOutputLimit — усечение вывода тулзы в событии (полный вывод — в логе рана).
const ToolOutputLimit = 8 * 1024

// TruncateToolOutput урезает вывод тулзы до ToolOutputLimit.
func TruncateToolOutput(s string) string {
	if len(s) <= ToolOutputLimit {
		return s
	}
	return s[:ToolOutputLimit] + "…[truncated]"
}

// JournalKind мапит нормализованное событие в kind журнала событий
// (api/openapi.yaml EventKind). Само нормализованное имя сохраняется
// в payload («normalized»).
func JournalKind(ev Event) string {
	switch ev.Kind {
	case EventAssistantThinking, EventAssistantThinkingDlt:
		return "stream.thinking"
	case EventAssistantText, EventAssistantTextDelta:
		return "stream.text"
	case EventToolCallStart:
		return "stream.tool_call"
	case EventToolCallEnd, EventToolResult:
		return "stream.tool_result"
	case EventUsage:
		return "stream.usage"
	case EventError, EventErrorRetry:
		return "stream.error"
	case EventRaw:
		return "stream.raw"
	default: // session.init, result, structured_output
		return "system"
	}
}
