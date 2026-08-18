package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/harness"
)

// Золотые файлы testdata/*.ndjson СИНТЕЗИРОВАНЫ по примерам из
// tasks/research/harness-capability-matrix.md (раздел «## 3. claude»,
// живой прогон 2.1.81 + доки); реальный CLI НЕ запускался (LLM-вызовы).
// Версия зафиксирована: claude 2.1.81.

// parseFile построчно прогоняет NDJSON-файл через ParseStream.
func parseFile(t *testing.T, path string) []harness.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var events []harness.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		events = append(events, New("").ParseStream(sc.Bytes())...)
	}
	require.NoError(t, sc.Err())
	return events
}

func eventsOfKind(events []harness.Event, kind string) []harness.Event {
	var out []harness.Event
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestParseStream_NormalRun(t *testing.T) {
	events := parseFile(t, "testdata/normal.ndjson")

	// init
	init := eventsOfKind(events, harness.EventSessionInit)
	require.Len(t, init, 1)
	assert.Equal(t, "sess-claude-1", init[0].SessionID)
	assert.Equal(t, "claude-sonnet-4-5", init[0].Model)

	// text_delta из stream_event (--include-partial-messages)
	deltas := eventsOfKind(events, harness.EventAssistantTextDelta)
	require.Len(t, deltas, 2)
	assert.Equal(t, "Прочитаю", deltas[0].Text)
	assert.Equal(t, " файл.", deltas[1].Text)

	// thinking-блок
	thinking := eventsOfKind(events, harness.EventAssistantThinking)
	require.Len(t, thinking, 1)
	assert.Equal(t, "Сначала посмотрю main.go.", thinking[0].Text)

	// assistant text (user-эхо не должно давать assistant.text)
	texts := eventsOfKind(events, harness.EventAssistantText)
	require.Len(t, texts, 2)
	assert.Equal(t, "Прочитаю файл и внесу правку.", texts[0].Text)
	assert.Equal(t, "Готово: правка внесена.", texts[1].Text)

	// tool_use → tool_call.start
	calls := eventsOfKind(events, harness.EventToolCallStart)
	require.Len(t, calls, 1)
	assert.Equal(t, "toolu_01", calls[0].CallID)
	assert.Equal(t, "Read", calls[0].Tool)
	assert.JSONEq(t, `{"file_path":"main.go"}`, string(calls[0].ToolInput))

	// tool_result из user-сообщения
	results := eventsOfKind(events, harness.EventToolResult)
	require.Len(t, results, 1)
	assert.Equal(t, "toolu_01", results[0].CallID)
	assert.Contains(t, results[0].ToolOutput, "package main")
	assert.False(t, results[0].ToolIsErr)

	// терминальный result: текст, session_id, structured_output, usage+cost
	res := eventsOfKind(events, harness.EventResult)
	require.Len(t, res, 1)
	assert.Equal(t, "success", res[0].Result.Status)
	assert.Equal(t, "Готово: правка внесена.", res[0].Text)
	assert.Equal(t, "sess-claude-1", res[0].SessionID)
	assert.JSONEq(t, `{"verdict":"approve","comments":[]}`, string(res[0].Result.StructuredOutput))
	require.NotNil(t, res[0].Usage)
	assert.Equal(t, int64(12345), res[0].Usage.Input)
	assert.Equal(t, int64(678), res[0].Usage.Output)
	assert.Equal(t, int64(1000), res[0].Usage.CacheRead)
	assert.Equal(t, int64(200), res[0].Usage.CacheWrite)
	require.NotNil(t, res[0].Usage.CostUSD)
	assert.InDelta(t, 0.042, *res[0].Usage.CostUSD, 1e-9)

	assert.Empty(t, eventsOfKind(events, harness.EventError))
	assert.Empty(t, eventsOfKind(events, harness.EventRaw))
}

func TestParseStream_RetryRun(t *testing.T) {
	events := parseFile(t, "testdata/retry.ndjson")

	// api_retry → error.retry, Retriable=true («признак жизни» для
	// stall-watchdog supervisor'а — дополнение T-25)
	retries := eventsOfKind(events, harness.EventErrorRetry)
	require.Len(t, retries, 3)
	require.NotNil(t, retries[0].Err)
	assert.True(t, retries[0].Err.Retriable)
	assert.Contains(t, retries[0].Text, "попытка 1/10")
	assert.Contains(t, retries[0].Text, "529")
	// session_id в api_retry не приходит (живой образец из матрицы: только
	// attempt/max_retries/retry_delay_ms/error_status/error)
	assert.Empty(t, retries[0].SessionID)

	// is_error=true → result(error) + отдельное EventError (retriable: 529)
	res := eventsOfKind(events, harness.EventResult)
	require.Len(t, res, 1)
	assert.Equal(t, "error", res[0].Result.Status)
	require.NotNil(t, res[0].Usage) // есть total_cost_usd=0 → usage не nil
	require.NotNil(t, res[0].Usage.CostUSD)
	assert.InDelta(t, 0.0, *res[0].Usage.CostUSD, 1e-9)

	errs := eventsOfKind(events, harness.EventError)
	require.Len(t, errs, 1)
	require.NotNil(t, errs[0].Err)
	assert.Contains(t, errs[0].Err.Message, "overloaded")
	assert.True(t, errs[0].Err.Retriable)

	assert.Empty(t, eventsOfKind(events, harness.EventRaw))
}

func TestParseStream_BrokenLines(t *testing.T) {
	events := parseFile(t, "testdata/broken.ndjson")

	raws := eventsOfKind(events, harness.EventRaw)
	require.Len(t, raws, 2) // обрезанный JSON + не-JSON строка
	assert.Contains(t, raws[0].Text, "обрезанный")
	assert.Contains(t, raws[1].Text, "not json at all")

	// валидные строки вокруг мусора парсятся нормально
	require.Len(t, eventsOfKind(events, harness.EventSessionInit), 1)
	require.Len(t, eventsOfKind(events, harness.EventResult), 1)
}

func TestParseStream_EmptyAndGarbage(t *testing.T) {
	h := New("")
	assert.Empty(t, h.ParseStream(nil))
	assert.Empty(t, h.ParseStream([]byte("   \t ")))

	events := h.ParseStream([]byte("{not valid json"))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_UnknownSystemSubtype(t *testing.T) {
	// hook_started и т.п. — в --bare не приходят, но если придут: raw (D-13).
	line := `{"type":"system","subtype":"hook_started","hook":"pretool"}`
	events := New("").ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_StreamEventNonDeltaSkipped(t *testing.T) {
	// Служебные partial-события (мы сами их запросили флагом) — не raw.
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}}`,
	} {
		assert.Empty(t, New("").ParseStream([]byte(line)), line)
	}
}

func TestParseStream_ThinkingDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"думаю"}}}`
	events := New("").ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventAssistantThinkingDlt, events[0].Kind)
	assert.Equal(t, "думаю", events[0].Text)
}

func TestParseStream_ToolResultContentBlocks(t *testing.T) {
	// tool_result.content массивом text-блоков (Anthropic-схема).
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}],"is_error":true}]}}`
	events := New("").ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventToolResult, events[0].Kind)
	assert.Equal(t, "part1part2", events[0].ToolOutput)
	assert.True(t, events[0].ToolIsErr)
}

func TestParseStream_TruncatesToolOutput(t *testing.T) {
	big := strings.Repeat("x", harness.ToolOutputLimit+100)
	quoted, err := json.Marshal(big)
	require.NoError(t, err)
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":` + string(quoted) + `,"is_error":false}]}}`
	events := New("").ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Less(t, len(events[0].ToolOutput), harness.ToolOutputLimit+100)
	assert.Contains(t, events[0].ToolOutput, "[truncated]")
}

func TestParseStream_NoUsage(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok"}`
	events := New("").ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Nil(t, events[0].Usage) // usage не гарантирован — не выдумываем
	assert.Nil(t, events[0].Result.StructuredOutput)
}

func TestExtractSessionID(t *testing.T) {
	h := New("")

	// init.session_id или result.session_id — первое подходящее событие
	id, err := h.ExtractSessionID([]harness.Event{
		{Kind: harness.EventAssistantText, Text: "hi"},
		{Kind: harness.EventSessionInit, SessionID: "from-init"},
		{Kind: harness.EventResult, SessionID: "from-result"},
	}, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "from-init", id)

	id, err = h.ExtractSessionID([]harness.Event{
		{Kind: harness.EventResult, SessionID: "from-result"},
	}, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "from-result", id)

	_, err = h.ExtractSessionID(nil, "/tmp")
	require.Error(t, err)
	assert.ErrorIs(t, err, errNoSessionID)
}

func TestBuildCommand_Basic(t *testing.T) {
	spec, err := New("").BuildCommand(harness.LaunchSpec{Prompt: "сделай задачу", WorkDir: "/tmp/proj"})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"claude", "-p", "сделай задачу",
		"--output-format", "stream-json",
		"--verbose", // обязателен для stream-json
		"--include-partial-messages",
		"--bare",
		"--permission-mode", "bypassPermissions", // дефолт при пустом аргументе New
	}, spec.Argv)
	assert.Empty(t, spec.Stdin)
	// WorkDir НЕ в argv — supervisor задаёт cmd.Dir
	for _, arg := range spec.Argv {
		assert.NotContains(t, arg, "/tmp/proj")
	}
}

func TestBuildCommand_CustomPermissionMode(t *testing.T) {
	spec, err := New("acceptEdits").BuildCommand(harness.LaunchSpec{Prompt: "p"})
	require.NoError(t, err)
	assert.Contains(t, spec.Argv, "--permission-mode")
	assert.Contains(t, spec.Argv, "acceptEdits")
	assert.NotContains(t, spec.Argv, "bypassPermissions")
}

func TestBuildCommand_ModelAndEffort(t *testing.T) {
	spec, err := New("").BuildCommand(harness.LaunchSpec{
		Prompt: "p",
		Model:  "claude-opus-4-1",
		Effort: "high",
	})
	require.NoError(t, err)
	assert.Contains(t, spec.Argv, "--model")
	assert.Contains(t, spec.Argv, "claude-opus-4-1")
	assert.Contains(t, spec.Argv, "--effort") // нативный флаг (матрица)
	assert.Contains(t, spec.Argv, "high")
}

func TestBuildCommand_ResumeRepassesAllFlags(t *testing.T) {
	// --json-schema передаём ЗАНОВО на каждый resume (UNVERIFIED у claude,
	// обязательно у qwen — матрица; повторная передача безвредна).
	spec, err := New("").BuildCommand(harness.LaunchSpec{
		Prompt:              "продолжи",
		SessionID:           "sess-1",
		Model:               "claude-opus-4-1",
		Effort:              "max",
		SchemaForStructured: `{"type":"object"}`,
	})
	require.NoError(t, err)

	assert.Equal(t, "claude", spec.Argv[0])
	assert.Equal(t, "-r", spec.Argv[1])
	assert.Equal(t, "sess-1", spec.Argv[2])
	assert.Contains(t, spec.Argv, "-p")
	assert.Contains(t, spec.Argv, "продолжи")
	assert.Contains(t, spec.Argv, "--verbose")
	assert.Contains(t, spec.Argv, "--bare")
	assert.Contains(t, spec.Argv, "--model")
	assert.Contains(t, spec.Argv, "--effort")
	assert.Contains(t, spec.Argv, "--json-schema")
	assert.Contains(t, spec.Argv, `{"type":"object"}`)
}

func TestBuildCommand_LongPromptViaStdin(t *testing.T) {
	long := strings.Repeat("а", promptStdinThreshold+1)
	spec, err := New("").BuildCommand(harness.LaunchSpec{Prompt: long})
	require.NoError(t, err)

	assert.Equal(t, long, spec.Stdin)
	// в argv — короткий stub, полного промпта нет
	promptIdx := -1
	for i, arg := range spec.Argv {
		if arg == "-p" {
			promptIdx = i
		}
	}
	require.NotEqual(t, -1, promptIdx)
	assert.Equal(t, stdinPromptStub, spec.Argv[promptIdx+1])
	assert.Less(t, len(spec.Argv[promptIdx+1]), 1024)
}

func TestBuildCommand_ExtraEnv(t *testing.T) {
	spec, err := New("").BuildCommand(harness.LaunchSpec{
		Prompt:   "p",
		ExtraEnv: map[string]string{"ANTHROPIC_API_KEY": "k", "CLAUDE_CODE_X": "y"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY=k", "CLAUDE_CODE_X=y"}, spec.Env)
}

func TestBuildCommand_EmptyPrompt(t *testing.T) {
	_, err := New("").BuildCommand(harness.LaunchSpec{})
	require.Error(t, err)
}

func TestCapabilities(t *testing.T) {
	caps := New("").Capabilities()
	assert.True(t, caps.StreamJSON)
	assert.True(t, caps.Resume)
	assert.Equal(t, []string{"low", "medium", "high", "max"}, caps.EffortLevels)
	assert.True(t, caps.CostReporting)
	assert.True(t, caps.StructuredJSON)
	assert.False(t, caps.ACP) // нативно нет — только внешний адаптер (матрица)
	assert.True(t, caps.Thinking)
	assert.Equal(t, harness.UsageSourceStream, caps.UsageSource)
}

func TestIsRetriable(t *testing.T) {
	cases := []struct {
		msg      string
		expected bool
	}{
		{"API Error: 429 Too Many Requests: rate limit exceeded", true},
		{"API Error: 529 provider overloaded", true},
		{"503 Service Unavailable", true},
		{"connection reset by peer", true},
		{"dial tcp: network is unreachable", true},
		{"request timed out", true},
		{"server_error", false}, // категория api_retry — не терминальная ошибка
		{"invalid API key", false},
		{"--json-schema validation failed", false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, isRetriable(tc.msg), tc.msg)
	}
}
