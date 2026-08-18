package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/harness"
)

// Схема JSONL (`codex exec --json`) по матрице «## 4. codex» (ТОЛЬКО доки,
// CLI локально не установлен — UNVERIFIED детали полей за пределами
// примеров exec.md):
//
//	{"type":"thread.started","thread_id":"…"}   — session id для resume
//	{"type":"turn.started"}
//	{"type":"item.started","item":{"id","type":"command_execution",
//	 "command","status":"in_progress"}}
//	{"type":"item.updated","item":{…}}          — частичный вывод (не мапим)
//	{"type":"item.completed","item":{"id","type":"command_execution",
//	 "aggregated_output","exit_code","status":"completed"}}
//	{"type":"item.completed","item":{"id","type":"agent_message","text"}}
//	{"type":"item.completed","item":{"id","type":"reasoning","text"}}
//	{"type":"turn.completed","usage":{"input_tokens","cached_input_tokens",
//	 "output_tokens","reasoning_output_tokens"}}
//	{"type":"turn.failed","error":{"message"}} / {"type":"error","message"}
//
// Text-delta стрима НЕТ: agent_message приходит целиком в item.completed
// (матрица, «JSON») → assistant.text_delta адаптер не эмитит.

type wireEvent struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"` // thread.started
	Item     *wireItem  `json:"item"`      // item.*
	Usage    *wireUsage `json:"usage"`     // turn.completed
	Message  string     `json:"message"`   // error
	Error    *wireError `json:"error"`     // turn.failed (форма UNVERIFIED)
}

type wireItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`              // command_execution, agent_message, reasoning, …
	Command          string `json:"command"`           // command_execution
	AggregatedOutput string `json:"aggregated_output"` // command_execution (completed)
	ExitCode         *int   `json:"exit_code"`         // command_execution (completed)
	Status           string `json:"status"`
	Text             string `json:"text"` // agent_message / reasoning
}

type wireUsage struct {
	InputTokens     int64 `json:"input_tokens"`
	CacheReadTokens int64 `json:"cached_input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	// ReasoningTokens — UNVERIFIED, входит ли в output_tokens (по семантике
	// OpenAI usage — обычно да); отдельно НЕ суммируем, чтобы не
	// задвоить учёт.
	ReasoningTokens int64 `json:"reasoning_output_tokens"`
}

type wireError struct {
	Message string `json:"message"`
}

// ParseStream парсит одну строку stdout. Не-JSON/битая строка → EventRaw,
// без паники (D-13; матрица, следствие «б»). Известные служебные события
// без полезной нагрузки (turn.started, item.updated) пропускаем молча —
// это не мусор.
func (a *adapter) ParseStream(line []byte) []harness.Event {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	now := time.Now()

	var ev wireEvent
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return []harness.Event{rawEvent(now, trimmed)}
	}

	switch ev.Type {
	case "thread.started":
		// thread_id — это и есть session id для resume (матрица).
		return []harness.Event{{
			Kind:      harness.EventSessionInit,
			TS:        now,
			SessionID: ev.ThreadID,
		}}
	case "turn.started", "item.updated":
		return nil
	case "item.started":
		return parseItemStarted(now, ev.Item)
	case "item.completed":
		return parseItemCompleted(now, ev.Item)
	case "turn.completed":
		return parseTurnCompleted(now, ev.Usage)
	case "turn.failed", "error":
		return []harness.Event{errorEvent(now, ev)}
	default:
		// Нераспознанные типы — в raw для журнала (D-13), не ошибка.
		return []harness.Event{rawEvent(now, trimmed)}
	}
}

func rawEvent(now time.Time, line []byte) harness.Event {
	return harness.Event{Kind: harness.EventRaw, TS: now, Text: string(line)}
}

// parseItemStarted: command_execution → tool_call.start (матрица,
// «Рекомендации»: codex tool_call.start = item.started). Прочие item-типы
// на started ничего полезного не несут (agent_message/reasoning приходят
// целиком на completed) — пропускаем.
func parseItemStarted(now time.Time, item *wireItem) []harness.Event {
	if item == nil {
		return nil
	}
	if item.Type != "command_execution" {
		return nil
	}
	input, err := json.Marshal(map[string]string{"command": item.Command})
	if err != nil {
		input = nil
	}
	return []harness.Event{{
		Kind:      harness.EventToolCallStart,
		TS:        now,
		CallID:    item.ID,
		Tool:      item.Type,
		ToolInput: input,
	}}
}

// parseItemCompleted: command_execution → tool_result (exit_code +
// aggregated_output, усечённый); agent_message → assistant.text ЦЕЛИКОМ
// (deltas нет — матрица); reasoning → assistant.thinking. Прочие типы
// (file_change, mcp_tool_call, web_search, todo_list — матрица) пока не
// мапим: их семантика по докам UNVERIFIED — пропускаем.
func parseItemCompleted(now time.Time, item *wireItem) []harness.Event {
	if item == nil {
		return nil
	}
	switch item.Type {
	case "command_execution":
		return []harness.Event{{
			Kind:       harness.EventToolResult,
			TS:         now,
			CallID:     item.ID,
			Tool:       item.Type,
			ToolOutput: harness.TruncateToolOutput(item.AggregatedOutput),
			ToolIsErr:  item.ExitCode != nil && *item.ExitCode != 0,
		}}
	case "agent_message":
		if item.Text == "" {
			return nil
		}
		return []harness.Event{{
			Kind: harness.EventAssistantText,
			TS:   now,
			Text: item.Text,
		}}
	case "reasoning":
		if item.Text == "" {
			return nil
		}
		return []harness.Event{{
			Kind: harness.EventAssistantThinking,
			TS:   now,
			Text: item.Text,
		}}
	}
	return nil
}

// parseTurnCompleted → usage (матрица: «turn.completed.usage»). usage
// отсутствует → события нет (null-семантика, не выдумываем — матрица,
// следствие «г»).
func parseTurnCompleted(now time.Time, u *wireUsage) []harness.Event {
	if u == nil {
		return nil
	}
	return []harness.Event{{
		Kind: harness.EventUsage,
		TS:   now,
		Usage: &harness.Usage{
			Input:     u.InputTokens,
			Output:    u.OutputTokens,
			CacheRead: u.CacheReadTokens,
		},
	}}
}

// errorEvent — turn.failed / error с классификацией retriable (сигнал для
// auto-resume, T-09). Форма поля ошибки UNVERIFIED: принимаем и
// {"error":{"message"}}, и плоское {"message"}.
func errorEvent(now time.Time, ev wireEvent) harness.Event {
	msg := ev.Message
	if ev.Error != nil && ev.Error.Message != "" {
		msg = ev.Error.Message
	}
	if msg == "" {
		msg = "codex: " + ev.Type + " без сообщения об ошибке"
	}
	return harness.Event{
		Kind: harness.EventError,
		TS:   now,
		Text: msg,
		Err:  &harness.StreamError{Message: msg, Retriable: isRetriable(msg)},
	}
}

// retriableSubstrings — эвристика retriable-ошибок (rate limit / сеть /
// 5xx провайдера) — как у соседних адаптеров (qwen/claude). Регистр
// не важен.
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
