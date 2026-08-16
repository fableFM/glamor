package qwen

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/harness"
)

// Схема NDJSON (`-o stream-json`) по матрице «## 2. qwen» — Claude-подобная:
//
//	{"type":"system","subtype":"init"|"session_start","session_id","model",...}
//	{"type":"assistant","message":{"content":[{"type":"text"|"tool_use",...}]},...}
//	{"type":"user","message":{"content":[{"type":"tool_result",...}]},...}
//	{"type":"result","subtype":"success","is_error":false,"session_id",
//	 "result","structured_result","usage","stats","duration_ms","error":{...}}

type wireEvent struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	SessionID        string          `json:"session_id"`
	Model            string          `json:"model"`
	IsError          bool            `json:"is_error"`
	Result           json.RawMessage `json:"result"`            // строка (может быть пустой)
	StructuredResult json.RawMessage `json:"structured_result"` // объект по --json-schema
	Usage            *wireUsage      `json:"usage"`
	Stats            *wireStats      `json:"stats"`
	Error            *wireError      `json:"error"`
	Message          json.RawMessage `json:"message"`
}

// wireUsage — usage в result. Точная схема полей qwen 0.21.3 по матрице не
// зафиксирована детально («result.usage»); принимаем Anthropic-подобные имена
// (qwen — форк с Claude-подобным stream-json). Поле nullable — usage может
// отсутствовать, не выдумываем (матрица, следствие «г»).
type wireUsage struct {
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	CacheRead    int64    `json:"cache_read_input_tokens"`
	CacheWrite   int64    `json:"cache_creation_input_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
}

// wireStats — stats.models.<m>.tokens.{input,output,total} (матрица,
// пример в headless.md) — фолбэк для usage.
type wireStats struct {
	Models map[string]struct {
		Tokens struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
			Total  int64 `json:"total"`
		} `json:"tokens"`
	} `json:"models"`
}

type wireError struct {
	Message string `json:"message"`
}

type wireMessage struct {
	Content json.RawMessage `json:"content"` // строка ИЛИ массив блоков
}

type wireContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result: строка ИЛИ блоки
	IsError   bool            `json:"is_error"`    // tool_result
}

// ParseStream парсит одну строку stdout. Не-JSON/битая строка → EventRaw,
// без паники (D-13; матрица, следствие «б»: парсер обязан пропускать
// не-JSON строки — обрезки при краше и т.п.).
func (adapter) ParseStream(line []byte) []harness.Event {
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
		if ev.Subtype == "init" || ev.Subtype == "session_start" {
			return []harness.Event{{
				Kind:      harness.EventSessionInit,
				TS:        now,
				SessionID: ev.SessionID,
				Model:     ev.Model,
			}}
		}
		// Прочие system-подтипы нам неизвестны — в raw для журнала (D-13).
		return []harness.Event{rawEvent(now, trimmed)}
	case "assistant", "user":
		return parseMessage(now, ev.Type, ev.Message)
	case "result":
		return parseResult(now, ev)
	default:
		// Нераспознанные типы (partial-события при --include-partial-messages
		// и т.п.) — мы их не запрашиваем, но если придут: raw, не ошибка.
		return []harness.Event{rawEvent(now, trimmed)}
	}
}

func rawEvent(now time.Time, line []byte) harness.Event {
	return harness.Event{Kind: harness.EventRaw, TS: now, Text: string(line)}
}

// parseMessage разбирает assistant/user-сообщение. Из user-сообщений
// (qwen tool-сообщения — матрица, «Рекомендации», tool_call.end/tool_result)
// берём ТОЛЬКО tool_result: текст user-сообщения — эхо промпта, а не ответ
// модели (расхождение с claude-семантикой учтено).
func parseMessage(now time.Time, msgType string, raw json.RawMessage) []harness.Event {
	var msg wireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}

	var out []harness.Event
	for _, block := range contentBlocks(msg.Content) {
		switch block.Type {
		case "text":
			if msgType == "assistant" && block.Text != "" {
				out = append(out, harness.Event{
					Kind: harness.EventAssistantText,
					TS:   now,
					Text: block.Text,
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

// parseResult — терминальное событие. Напоминание: при exit 53/55/130
// result-события может НЕ быть — эти коды обрабатывает supervisor (T-09),
// адаптеру они недоступны (матрица, «stream-json / json»).
func parseResult(now time.Time, ev wireEvent) []harness.Event {
	status := "success"
	if ev.IsError {
		status = "error"
	}

	res := &harness.Result{Status: status}
	if len(ev.StructuredResult) > 0 && string(ev.StructuredResult) != "null" {
		// qwen кладёт structured output в result.structured_result
		// (у claude — structured_output; матрица, «Structured output»).
		res.StructuredOutput = ev.StructuredResult
	}

	text := rawString(ev.Result)
	out := []harness.Event{{
		Kind:      harness.EventResult,
		TS:        now,
		Text:      text,
		SessionID: ev.SessionID,
		Result:    res,
		Usage:     buildUsage(ev.Usage, ev.Stats),
	}}

	if ev.IsError {
		msg := text
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		if msg == "" {
			msg = "qwen: result с is_error=true без сообщения об ошибке"
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

// buildUsage собирает Usage: приоритет — result.usage, фолбэк — агрегат
// stats.models.*.tokens (матрица: «qwen result.usage+stats»). Оба
// отсутствуют → nil (null-семантика, не выдумываем).
func buildUsage(u *wireUsage, s *wireStats) *harness.Usage {
	if u != nil {
		return &harness.Usage{
			Input:      u.InputTokens,
			Output:     u.OutputTokens,
			CacheRead:  u.CacheRead,
			CacheWrite: u.CacheWrite,
			CostUSD:    u.CostUSD,
		}
	}
	if s == nil || len(s.Models) == 0 {
		return nil
	}
	usage := &harness.Usage{}
	var total int64
	for _, m := range s.Models {
		usage.Input += m.Tokens.Input
		usage.Output += m.Tokens.Output
		total += m.Tokens.Total
	}
	// В stats может быть только tokens.total — докидываем остаток во вход
	// (лучше грубая оценка, чем потерянные токены; схема stats по матрице).
	if usage.Input == 0 && total > usage.Output {
		usage.Input = total - usage.Output
	}
	return usage
}

// retriableSubstrings — эвристика retriable-ошибок (rate limit / сеть /
// 5xx провайдера) — сигнал для auto-resume supervisor'а (T-09). Регистр
// не важен. QWEN_CODE_UNATTENDED_RETRY=1 ретраит 429/529 и сам (матрица),
// так что сюда доходят в основном исчерпанные/иные сбои.
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
