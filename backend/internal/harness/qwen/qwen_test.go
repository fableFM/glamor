package qwen

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
// tasks/research/harness-capability-matrix.md (раздел «## 2. qwen»);
// формат соответствует qwen 0.21.3, реальный CLI НЕ запускался.

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

	// init
	init := eventsOfKind(events, harness.EventSessionInit)
	require.Len(t, init, 1)
	assert.Equal(t, "sess-abc-123", init[0].SessionID)
	assert.Equal(t, "qwen3-coder-plus", init[0].Model)

	// assistant text (user-эхо не должно давать assistant.text)
	texts := eventsOfKind(events, harness.EventAssistantText)
	require.Len(t, texts, 2)
	assert.Equal(t, "Прочитаю файл и внесу правку.", texts[0].Text)

	// tool_use → tool_call.start
	calls := eventsOfKind(events, harness.EventToolCallStart)
	require.Len(t, calls, 1)
	assert.Equal(t, "toolu_01", calls[0].CallID)
	assert.Equal(t, "read_file", calls[0].Tool)
	assert.JSONEq(t, `{"path":"main.go"}`, string(calls[0].ToolInput))

	// tool_result из user-сообщения
	results := eventsOfKind(events, harness.EventToolResult)
	require.Len(t, results, 1)
	assert.Equal(t, "toolu_01", results[0].CallID)
	assert.Contains(t, results[0].ToolOutput, "package main")
	assert.False(t, results[0].ToolIsErr)

	// терминальный result: текст, session_id, structured_result, usage
	res := eventsOfKind(events, harness.EventResult)
	require.Len(t, res, 1)
	assert.Equal(t, "success", res[0].Result.Status)
	assert.Equal(t, "Готово: правка внесена.", res[0].Text)
	assert.Equal(t, "sess-abc-123", res[0].SessionID)
	assert.JSONEq(t, `{"verdict":"approve","comments":[]}`, string(res[0].Result.StructuredOutput))
	require.NotNil(t, res[0].Usage)
	assert.Equal(t, int64(12345), res[0].Usage.Input)
	assert.Equal(t, int64(678), res[0].Usage.Output)
	assert.Equal(t, int64(1000), res[0].Usage.CacheRead)
	assert.Equal(t, int64(200), res[0].Usage.CacheWrite)
	require.NotNil(t, res[0].Usage.CostUSD)
	assert.InDelta(t, 0.0123, *res[0].Usage.CostUSD, 1e-9)

	assert.Empty(t, eventsOfKind(events, harness.EventError))
	assert.Empty(t, eventsOfKind(events, harness.EventRaw))
}

func TestParseStream_ErrorRun(t *testing.T) {
	events := parseFile(t, "testdata/error.ndjson")

	res := eventsOfKind(events, harness.EventResult)
	require.Len(t, res, 1)
	assert.Equal(t, "error", res[0].Result.Status)
	assert.Equal(t, "sess-err-1", res[0].SessionID)
	assert.Empty(t, res[0].Result.StructuredOutput)
	assert.Nil(t, res[0].Usage) // usage отсутствует — не выдумываем

	// is_error=true → result(error) + отдельное EventError
	errs := eventsOfKind(events, harness.EventError)
	require.Len(t, errs, 1)
	require.NotNil(t, errs[0].Err)
	assert.Contains(t, errs[0].Err.Message, "rate limit")
	assert.True(t, errs[0].Err.Retriable) // 429 → retriable
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
	h := New()
	assert.Empty(t, h.ParseStream(nil))
	assert.Empty(t, h.ParseStream([]byte("   \t ")))

	events := h.ParseStream([]byte("{not valid json"))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_UsageFromStatsFallback(t *testing.T) {
	// usage отсутствует, есть только stats.models.*.tokens (матрица).
	line := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok",` +
		`"stats":{"models":{"a":{"tokens":{"output":100,"total":500}},"b":{"tokens":{"output":50,"total":250}}}}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Usage)
	assert.Equal(t, int64(150), events[0].Usage.Output)
	assert.Equal(t, int64(600), events[0].Usage.Input) // total - output
	assert.Nil(t, events[0].Usage.CostUSD)
}

func TestParseStream_NoUsage(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok"}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Nil(t, events[0].Usage)
	assert.Nil(t, events[0].Result.StructuredOutput)
}

func TestParseStream_ToolResultContentBlocks(t *testing.T) {
	// tool_result.content массивом text-блоков (Anthropic-подобная форма).
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}],"is_error":true}]}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventToolResult, events[0].Kind)
	assert.Equal(t, "part1part2", events[0].ToolOutput)
	assert.True(t, events[0].ToolIsErr)
}

func TestParseStream_TruncatesToolOutput(t *testing.T) {
	big := strings.Repeat("x", harness.ToolOutputLimit+100)
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","content":` + strconvQuote(big) + `,"is_error":false}]}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Less(t, len(events[0].ToolOutput), harness.ToolOutputLimit+100)
	assert.Contains(t, events[0].ToolOutput, "[truncated]")
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestExtractSessionID(t *testing.T) {
	h := New()

	// result.session_id или init.session_id — первое подходящее событие
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
	assert.Contains(t, err.Error(), "/tmp")
}

func TestBuildCommand_Basic(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: "сделай задачу", WorkDir: "/tmp/proj"})
	require.NoError(t, err)

	assert.Equal(t, []string{"qwen", "-p", "сделай задачу", "-o", "stream-json", "--approval-mode", "yolo"}, spec.Argv)
	assert.Empty(t, spec.Stdin)
	// WorkDir НЕ в argv — supervisor задаёт cmd.Dir
	for _, arg := range spec.Argv {
		assert.NotContains(t, arg, "/tmp/proj")
	}
	assert.Contains(t, spec.Env, "QWEN_CODE_UNATTENDED_RETRY=1")
}

func TestBuildCommand_Model(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: "p", Model: "coder-model"})
	require.NoError(t, err)
	assert.Contains(t, spec.Argv, "-m")
	assert.Contains(t, spec.Argv, "coder-model")
}

func TestBuildCommand_ResumeRepassesAllFlags(t *testing.T) {
	// --json-schema — per-run флаг: передаём ЗАНОВО на каждый resume (матрица).
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:              "продолжи",
		SessionID:           "sess-1",
		Model:               "coder-model",
		SchemaForStructured: `{"type":"object"}`,
	})
	require.NoError(t, err)

	assert.Equal(t, "qwen", spec.Argv[0])
	assert.Equal(t, "-r", spec.Argv[1])
	assert.Equal(t, "sess-1", spec.Argv[2])
	assert.Contains(t, spec.Argv, "-p")
	assert.Contains(t, spec.Argv, "продолжи")
	assert.Contains(t, spec.Argv, "-m")
	assert.Contains(t, spec.Argv, "--json-schema")
	assert.Contains(t, spec.Argv, `{"type":"object"}`)
}

func TestBuildCommand_LongPromptViaStdin(t *testing.T) {
	long := strings.Repeat("а", promptStdinThreshold+1)
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: long})
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
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:   "p",
		ExtraEnv: map[string]string{"QWEN_HOME": "/tmp/qwen"},
	})
	require.NoError(t, err)
	assert.Contains(t, spec.Env, "QWEN_CODE_UNATTENDED_RETRY=1")
	assert.Contains(t, spec.Env, "QWEN_HOME=/tmp/qwen")
}

func TestBuildCommand_EmptyPrompt(t *testing.T) {
	_, err := New().BuildCommand(harness.LaunchSpec{})
	require.Error(t, err)
}

func TestCapabilities(t *testing.T) {
	caps := New().Capabilities()
	assert.True(t, caps.StreamJSON)
	assert.True(t, caps.Resume)
	assert.Nil(t, caps.EffortLevels) // CLI-флага effort нет (матрица)
	assert.True(t, caps.CostReporting)
	assert.True(t, caps.StructuredJSON)
	assert.True(t, caps.ACP)
	assert.False(t, caps.Thinking)
	assert.Equal(t, harness.UsageSourceStream, caps.UsageSource)
}

func TestIsRetriable(t *testing.T) {
	cases := []struct {
		msg      string
		expected bool
	}{
		{"429 Too Many Requests: rate limit exceeded", true},
		{"HTTP 529: provider overloaded", true},
		{"503 Service Unavailable", true},
		{"connection reset by peer", true},
		{"dial tcp: network is unreachable", true},
		{"request timed out", true},
		{"invalid API key", false},
		{"--json-schema validation failed", false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, isRetriable(tc.msg), tc.msg)
	}
}
