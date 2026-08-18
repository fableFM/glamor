package opencode

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
// tasks/research/harness-capability-matrix.md (раздел «## 5. opencode») и
// takopi.dev stream-json cheatsheet; формат соответствует opencode 1.2.27,
// реальный CLI НЕ запускался (это стоит API-вызовы; живой прогон — e2e
// под build tag e2e).

// parseFile построчно прогоняет NDJSON-файл через ParseStream.
func parseFile(t *testing.T, path string) []harness.Event {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var events []harness.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		events = append(events, New().ParseStream(sc.Bytes())...)
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

	// session.init из step_start (их два — по одному на шаг, SessionID один)
	init := eventsOfKind(events, harness.EventSessionInit)
	require.Len(t, init, 2)
	assert.Equal(t, "ses_abc123def456", init[0].SessionID)
	assert.Empty(t, init[0].Model) // модели в событиях нет (баг #40544)
	// timestamp конверта (epoch ms) → TS события
	assert.Equal(t, int64(1767036059338), init[0].TS.UnixMilli())

	// text → assistant.text_delta (гранулярность UNVERIFIED, см. stream.go)
	texts := eventsOfKind(events, harness.EventAssistantTextDelta)
	require.Len(t, texts, 2)
	assert.Equal(t, "Прочитаю файл и внесу правку.", texts[0].Text)
	assert.Equal(t, "ses_abc123def456", texts[0].SessionID)

	// reasoning → assistant.thinking
	thinking := eventsOfKind(events, harness.EventAssistantThinking)
	require.Len(t, thinking, 1)
	assert.Contains(t, thinking[0].Text, "прочитать main.go")

	// tool_use (только completed!) → ПАРА tool_call.start + tool_result,
	// start=end: одинаковые CallID и TS (зафиксированное отступление).
	calls := eventsOfKind(events, harness.EventToolCallStart)
	require.Len(t, calls, 1)
	assert.Equal(t, "call_read_01", calls[0].CallID)
	assert.Equal(t, "read", calls[0].Tool)
	assert.JSONEq(t, `{"filePath":"main.go"}`, string(calls[0].ToolInput))

	results := eventsOfKind(events, harness.EventToolResult)
	require.Len(t, results, 1)
	assert.Equal(t, calls[0].CallID, results[0].CallID)
	assert.Equal(t, calls[0].Tool, results[0].Tool)
	assert.True(t, calls[0].TS.Equal(results[0].TS), "start=end: TS совпадают")
	assert.Contains(t, results[0].ToolOutput, "package main")
	assert.False(t, results[0].ToolIsErr)

	// step_finish → usage (tokens + cost); их два (tool-calls и финальный)
	usages := eventsOfKind(events, harness.EventUsage)
	require.Len(t, usages, 2)

	// промежуточный (reason=tool-calls): cost=0, без cache
	require.NotNil(t, usages[0].Usage)
	assert.Equal(t, int64(21772), usages[0].Usage.Input)
	assert.Equal(t, int64(110), usages[0].Usage.Output)
	require.NotNil(t, usages[0].Usage.CostUSD)
	assert.InDelta(t, 0.0, *usages[0].Usage.CostUSD, 1e-9)

	// финальный (reason=stop): reasoning складывается в Output, cache, cost
	require.NotNil(t, usages[1].Usage)
	assert.Equal(t, int64(671), usages[1].Usage.Input)
	assert.Equal(t, int64(12), usages[1].Usage.Output) // 8 output + 4 reasoning
	assert.Equal(t, int64(21415), usages[1].Usage.CacheRead)
	assert.Equal(t, int64(100), usages[1].Usage.CacheWrite)
	require.NotNil(t, usages[1].Usage.CostUSD)
	assert.InDelta(t, 0.0123, *usages[1].Usage.CostUSD, 1e-9)

	// терминального result адаптер НЕ синтезирует (stateless; exit code —
	// supervisor); #26855: финальный step_finish может не прийти.
	assert.Empty(t, eventsOfKind(events, harness.EventResult))
	assert.Empty(t, eventsOfKind(events, harness.EventError))
	assert.Empty(t, eventsOfKind(events, harness.EventRaw))
}

func TestParseStream_ErrorRun(t *testing.T) {
	events := parseFile(t, "testdata/error.ndjson")

	require.Len(t, eventsOfKind(events, harness.EventSessionInit), 1)

	errs := eventsOfKind(events, harness.EventError)
	require.Len(t, errs, 1)
	require.NotNil(t, errs[0].Err)
	assert.Contains(t, errs[0].Err.Message, "Rate limit exceeded")
	assert.True(t, errs[0].Err.Retriable) // 429 → retriable
	assert.Equal(t, "ses_err_001", errs[0].SessionID)
}

func TestParseStream_BrokenLines(t *testing.T) {
	events := parseFile(t, "testdata/broken.ndjson")

	raws := eventsOfKind(events, harness.EventRaw)
	require.Len(t, raws, 2) // обрезанный JSON + не-JSON строка
	assert.Contains(t, raws[0].Text, "обрезанный")
	assert.Contains(t, raws[1].Text, "not json at all")

	// валидные строки вокруг мусора парсятся нормально
	require.Len(t, eventsOfKind(events, harness.EventSessionInit), 1)
	require.Len(t, eventsOfKind(events, harness.EventUsage), 1)
}

func TestParseStream_EmptyAndGarbage(t *testing.T) {
	h := New()
	assert.Empty(t, h.ParseStream(nil))
	assert.Empty(t, h.ParseStream([]byte("   \t ")))

	events := h.ParseStream([]byte("{not valid json"))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)

	// валидный JSON неизвестного типа → raw, не ошибка
	events = h.ParseStream([]byte(`{"type":"future_event","sessionID":"s1"}`))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_ToolUseError(t *testing.T) {
	// tool_use со status=="error" → пара start+result, ToolIsErr=true,
	// вывод — state.error.
	line := `{"type":"tool_use","timestamp":1767036061199,"sessionID":"s1","part":{"id":"p1","type":"tool",` +
		`"callID":"c1","tool":"bash","state":{"status":"error","input":{"command":"false"},` +
		`"error":"exit status 1","title":"Run false","time":{"start":1,"end":2}}}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 2)
	assert.Equal(t, harness.EventToolCallStart, events[0].Kind)
	assert.Equal(t, harness.EventToolResult, events[1].Kind)
	assert.True(t, events[1].ToolIsErr)
	assert.Equal(t, "exit status 1", events[1].ToolOutput)
	assert.Equal(t, events[0].CallID, events[1].CallID)
}

func TestParseStream_StepFinishWithoutMetrics(t *testing.T) {
	// step-finish без tokens/cost — не наш формат → raw (null-семантика,
	// не выдумываем usage).
	line := `{"type":"step_finish","timestamp":1,"sessionID":"s1","part":{"id":"p1","type":"step-finish","reason":"stop"}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_MissingPart(t *testing.T) {
	// part отсутствует у типа, который его требует → raw, без паники.
	events := New().ParseStream([]byte(`{"type":"text","timestamp":1,"sessionID":"s1"}`))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_TruncatesToolOutput(t *testing.T) {
	big := strings.Repeat("x", harness.ToolOutputLimit+100)
	quoted, err := json.Marshal(big)
	require.NoError(t, err)
	line := `{"type":"tool_use","timestamp":1,"sessionID":"s1","part":{"id":"p1","type":"tool",` +
		`"callID":"c1","tool":"bash","state":{"status":"completed","input":{},` +
		`"output":` + string(quoted) + `,"time":{"start":1,"end":2}}}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 2)
	assert.Less(t, len(events[1].ToolOutput), harness.ToolOutputLimit+100)
	assert.Contains(t, events[1].ToolOutput, "[truncated]")
}

func TestExtractSessionID(t *testing.T) {
	h := New()

	// sessionID из первого события с непустым полем (не фиксированный тип)
	id, err := h.ExtractSessionID([]harness.Event{
		{Kind: harness.EventAssistantTextDelta, Text: "hi"},
		{Kind: harness.EventSessionInit, SessionID: "ses_from_init"},
		{Kind: harness.EventUsage, SessionID: "ses_from_usage"},
	}, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "ses_from_init", id)

	// у opencode sessionID есть в КАЖДОМ событии конверта — любое подходит
	id, err = h.ExtractSessionID([]harness.Event{
		{Kind: harness.EventUsage, SessionID: "ses_from_usage"},
	}, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "ses_from_usage", id)

	_, err = h.ExtractSessionID(nil, "/tmp")
	require.Error(t, err)
	require.ErrorIs(t, err, errNoSessionID)
}

func TestBuildCommand_Basic(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: "сделай задачу", WorkDir: "/tmp/proj"})
	require.NoError(t, err)

	assert.Equal(t, []string{"opencode", "run", "сделай задачу", "--format", "json", "--auto"}, spec.Argv)
	assert.Empty(t, spec.Stdin)
	// WorkDir НЕ в argv — supervisor задаёт cmd.Dir
	for _, arg := range spec.Argv {
		assert.NotContains(t, arg, "/tmp/proj")
	}
	// --dangerously-skip-permissions отсутствует в 1.2.27 — не добавляем
	for _, arg := range spec.Argv {
		assert.NotContains(t, arg, "dangerously")
	}
}

func TestBuildCommand_ModelAndEffort(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt: "p",
		Model:  "anthropic/claude-sonnet-4-5",
		Effort: "high",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"opencode", "run",
		"-m", "anthropic/claude-sonnet-4-5",
		"--variant", "high",
		"p", "--format", "json", "--auto",
	}, spec.Argv)
}

func TestBuildCommand_Resume(t *testing.T) {
	// Resume: `opencode run -s <id> "<новое>"` (end-to-end UNVERIFIED).
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:    "продолжи",
		SessionID: "ses_abc123",
		Model:     "openai/gpt-5",
	})
	require.NoError(t, err)

	assert.Equal(t, "opencode", spec.Argv[0])
	assert.Equal(t, "run", spec.Argv[1])
	assert.Equal(t, "-s", spec.Argv[2])
	assert.Equal(t, "ses_abc123", spec.Argv[3])
	assert.Contains(t, spec.Argv, "продолжи")
	assert.Contains(t, spec.Argv, "-m")
	assert.Contains(t, spec.Argv, "--auto")
}

func TestBuildCommand_LongPromptViaFile(t *testing.T) {
	long := strings.Repeat("а", maxArgvPromptLen+1)
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: long})
	require.NoError(t, err)

	// в argv — короткая инструкция со ссылкой на временный файл
	var prompt string
	for i, arg := range spec.Argv {
		if arg == "run" {
			prompt = spec.Argv[i+1]
		}
	}
	require.NotEmpty(t, prompt)
	assert.Less(t, len(prompt), 1024)
	assert.Contains(t, prompt, "Прочитай файл ")

	path := strings.TrimPrefix(prompt, "Прочитай файл ")
	path = strings.TrimSuffix(path, " и выполни инструкции из него.")
	defer func() { _ = os.RemoveAll(path[:strings.LastIndex(path, "/")]) }()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, long, string(content))
}

func TestBuildCommand_ExtraEnv(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:   "p",
		ExtraEnv: map[string]string{"OPENCODE_CONFIG": "/tmp/oc.json"},
	})
	require.NoError(t, err)
	assert.Contains(t, spec.Env, "OPENCODE_CONFIG=/tmp/oc.json")
}

func TestBuildCommand_EmptyPrompt(t *testing.T) {
	_, err := New().BuildCommand(harness.LaunchSpec{})
	require.Error(t, err)
}

func TestCapabilities(t *testing.T) {
	caps := New().Capabilities()
	assert.True(t, caps.StreamJSON)
	assert.True(t, caps.Resume) // end-to-end UNVERIFIED (матрица, риск 7)
	assert.Nil(t, caps.EffortLevels)
	assert.True(t, caps.CostReporting) // с оговоркой бага #26855
	assert.False(t, caps.StructuredJSON)
	assert.True(t, caps.ACP)
	assert.True(t, caps.Thinking)
	assert.Equal(t, harness.UsageSourceStream, caps.UsageSource)
}

func TestIsRetriable(t *testing.T) {
	cases := []struct {
		msg      string
		expected bool
	}{
		{"Rate limit exceeded: 429 Too Many Requests", true},
		{"HTTP 529: provider overloaded", true},
		{"503 Service Unavailable", true},
		{"connection reset by peer", true},
		{"request timed out", true},
		{"ProviderAuthError: invalid API key", false},
		{"MessageAbortedError", false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, isRetriable(tc.msg), tc.msg)
	}
}
