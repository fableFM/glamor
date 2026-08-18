package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/harness"
)

// Схема NDJSON (`--output-format stream-json --verbose`) по матрице
// «## 3. claude» (живой прогон 2.1.81 + доки):
//
//	{"type":"system","subtype":"init","session_id","cwd","model",
//	 "permissionMode","tools","claude_code_version",...}
//	{"type":"system","subtype":"api_retry","attempt","max_retries",
//	 "retry_delay_ms","error_status","error"}
//	{"type":"assistant","message":{"content":[{"type":"text"|"thinking"|
//	 "tool_use",...}]},...}
//	{"type":"user","message":{"content":[{"type":"tool_result",...}]},...}
//	{"type":"stream_event","event":{"type":"content_block_delta",
//	 "delta":{"type":"text_delta","text":...}}}   (--include-partial-messages)
//	{"type":"result","subtype":"success","is_error","result","session_id",
//	 "total_cost_usd","usage",...}

type wireEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	IsError   bool            `json:"is_error"`
	Result    json.RawMessage `json:"result"` // строка (может быть пустой)
	// StructuredOutput — результат --json-schema. В доках описан для
	// `--output-format json`; что поле приходит и в stream-json result —
	// UNVERIFIED, парсим best-effort (матрица, «Structured output»).
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	Usage            *wireUsage      `json:"usage"`
	// api_retry (живой образец из матрицы).
	Attempt      int             `json:"attempt"`
	MaxRetries   int             `json:"max_retries"`
	RetryDelayMs int64           `json:"retry_delay_ms"`
	ErrorStatus  int             `json:"error_status"`
	Error        json.RawMessage `json:"error"` // строка (категория) ИЛИ объект

	Message json.RawMessage `json:"message"` // assistant/user
	Event   json.RawMessage `json:"event"`   // stream_event
}

// wireUsage — usage в result (Anthropic-подобные поля, как у qwen-форка;
// матрица, «usage в потоке»).
type wireUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CacheRead    int64 `json:"cache_read_input_tokens"`
	CacheWrite   int64 `json:"cache_creation_input_tokens"`
}

type wireMessage struct {
	Content json.RawMessage `json:"content"` // строка ИЛИ массив блоков
}

type wireContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`    // thinking-блок
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result: строка ИЛИ блоки
	IsError   bool            `json:"is_error"`    // tool_result
}

// wireStreamEvent — событие --include-partial-messages (Anthropic
// SSE-подобные; матрица, «stream-json»).
type wireStreamEvent struct {
	Type  string `json:"type"` // content_block_delta, message_start, ...
	Delta struct {
		Type     string `json:"type"` // text_delta, thinking_delta, ...
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// ParseStream парсит одну строку stdout. Не-JSON/битая строка → EventRaw,
// без паники (D-13; матрица, следствие «б»: парсер обязан пропускать
// не-JSON строки — обрезки при краше и т.п.).
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
	case "system":
		return parseSystem(now, trimmed, ev)
	case "assistant":
		return parseMessage(now, true, ev.Message)
	case "user":
		return parseMessage(now, false, ev.Message)
	case "stream_event":
		return parseStreamEvent(now, ev.Event)
	case "result":
		return parseResult(now, ev)
	default:
		// Нераспознанные типы — в raw для журнала (D-13), не ошибка.
		return []harness.Event{rawEvent(now, trimmed)}
	}
}

func rawEvent(now time.Time, line []byte) harness.Event {
	return harness.Event{Kind: harness.EventRaw, TS: now, Text: string(line)}
}

// parseSystem: init → session.init; api_retry → error.retry (Retriable=true:
// это «признак жизни» — процесс живёт в retry-лупе минутами, stall-watchdog
// supervisor'а (T-09) обновляет по нему last-activity; верхний лимит лупы —
// конфиг supervisor'а, дополнение T-25).
func parseSystem(now time.Time, line []byte, ev wireEvent) []harness.Event {
	switch ev.Subtype {
	case "init":
		return []harness.Event{{
			Kind:      harness.EventSessionInit,
			TS:        now,
			SessionID: ev.SessionID,
			Model:     ev.Model,
		}}
	case "api_retry":
		msg := fmt.Sprintf("api_retry: попытка %d/%d (status %d, delay %dms): %s",
			ev.Attempt, ev.MaxRetries, ev.ErrorStatus, ev.RetryDelayMs, rawString(ev.Error))
		return []harness.Event{{
			Kind:      harness.EventErrorRetry,
			TS:        now,
			Text:      msg,
			SessionID: ev.SessionID,
			Err:       &harness.StreamError{Message: msg, Retriable: true},
		}}
	default:
		// hook_started/hook_response/plugin_install и прочие — в --bare не
		// приходят; если придут — журналируем как raw (D-13).
		return []harness.Event{rawEvent(now, line)}
	}
}

// parseMessage разбирает assistant/user-сообщение. Из user-сообщений берём
// ТОЛЬКО tool_result: текст user-сообщения — эхо промпта, а не ответ модели
// (как в qwen-адаптере).
func parseMessage(now time.Time, assistant bool, raw json.RawMessage) []harness.Event {
	var msg wireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}

	var out []harness.Event
	for _, block := range contentBlocks(msg.Content) {
		switch block.Type {
		case "text":
			if assistant && block.Text != "" {
				out = append(out, harness.Event{
					Kind: harness.EventAssistantText,
					TS:   now,
					Text: block.Text,
				})
			}
		case "thinking":
			if assistant && block.Thinking != "" {
				out = append(out, harness.Event{
					Kind: harness.EventAssistantThinking,
					TS:   now,
					Text: block.Thinking,
				})
			}
		case "tool_use":
			out = append(out, harness.Event{
				Kind:      harness.EventToolCallStart,
				TS:        now,
				CallID:    block.ID,
				Tool:      block.Name,
				ToolInput: block.Input,
			})
		case "tool_result":
			out = append(out, harness.Event{
				Kind:       harness.EventToolResult,
				TS:         now,
				CallID:     block.ToolUseID,
				ToolOutput: harness.TruncateToolOutput(toolResultText(block.Content)),
				ToolIsErr:  block.IsError,
			})
		}
	}
	return out
}

// parseStreamEvent — partial-события --include-partial-messages. Мапим
// только дельты текста/thinking; служебные (message_start,
// content_block_start/stop, signature_delta, ...) — пропускаем (мы сами
// их запросили флагом, это не мусор → не raw).
func parseStreamEvent(now time.Time, raw json.RawMessage) []harness.Event {
	var ev wireStreamEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	if ev.Type != "content_block_delta" {
		return nil
	}
	switch ev.Delta.Type {
	case "text_delta":
		if ev.Delta.Text == "" {
			return nil
		}
		return []harness.Event{{
			Kind: harness.EventAssistantTextDelta,
			TS:   now,
			Text: ev.Delta.Text,
		}}
	case "thinking_delta":
		if ev.Delta.Thinking == "" {
			return nil
		}
		return []harness.Event{{
			Kind: harness.EventAssistantThinkingDlt,
			TS:   now,
			Text: ev.Delta.Thinking,
		}}
	}
	return nil
}

// contentBlocks нормализует message.content: массив блоков либо голая строка.
func contentBlocks(raw json.RawMessage) []wireContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []wireContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []wireContentBlock{{Type: "text", Text: s}}
	}
	return nil
}

// toolResultText извлекает текст из tool_result.content (строка либо массив
// text-блоков, как в Anthropic-схеме).
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var b strings.Builder
	for _, block := range contentBlocks(raw) {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	return string(raw)
}

// parseResult — терминальное событие: result text, session_id, usage,
// total_cost_usd, is_error → status error (матрица, «stream-json»).
// Напоминание: при SIGTERM (exit 143) result-события может НЕ быть —
// exit code обрабатывает supervisor (T-09), адаптеру он недоступен.
func parseResult(now time.Time, ev wireEvent) []harness.Event {
	status := "success"
	if ev.IsError {
		status = "error"
	}

	res := &harness.Result{Status: status}
	if len(ev.StructuredOutput) > 0 && string(ev.StructuredOutput) != "null" {
		res.StructuredOutput = ev.StructuredOutput
	}

	text := rawString(ev.Result)
	out := []harness.Event{{
		Kind:      harness.EventResult,
		TS:        now,
		Text:      text,
		SessionID: ev.SessionID,
		Result:    res,
		Usage:     buildUsage(ev),
	}}

	if ev.IsError {
		msg := text
		if s := rawString(ev.Error); s != "" {
			msg = s
		}
		if msg == "" {
			msg = "claude: result с is_error=true без сообщения об ошибке"
		}
		out = append(out, harness.Event{
			Kind:      harness.EventError,
			TS:        now,
			Text:      msg,
			SessionID: ev.SessionID,
			Err:       &harness.StreamError{Message: msg, Retriable: isRetriable(msg)},
		})
	}
	return out
}

// rawString извлекает строку из RawMessage; не строка — компактный raw.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(bytes.TrimSpace(raw))
}

// buildUsage собирает Usage из result.usage + total_cost_usd (матрица:
// «result (usage, total_cost_usd, per-model)»). usage отсутствует → nil
// (null-семантика, не выдумываем — матрица, следствие «г»).
func buildUsage(ev wireEvent) *harness.Usage {
	if ev.Usage == nil && ev.TotalCostUSD == nil {
		return nil
	}
	usage := &harness.Usage{CostUSD: ev.TotalCostUSD}
	if ev.Usage != nil {
		usage.Input = ev.Usage.InputTokens
		usage.Output = ev.Usage.OutputTokens
		usage.CacheRead = ev.Usage.CacheRead
		usage.CacheWrite = ev.Usage.CacheWrite
	}
	return usage
}

// retriableSubstrings — эвристика retriable-ошибок (rate limit / сеть /
// 5xx провайдера) — сигнал для auto-resume supervisor'а (T-09). Регистр
// не важен. Категории api_retry из матрицы: rate_limit, overloaded,
// server_error. Список — как у qwen-адаптера.
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
