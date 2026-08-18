package codex

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/harness"
)

// Золотые файлы testdata/*.jsonl СИНТЕЗИРОВАНЫ по примерам из
// tasks/research/harness-capability-matrix.md (раздел «## 4. codex» —
// только официальные доки, exec.md); реальный CLI НЕ запускался
// (не установлен локально). Версия/поведение — UNVERIFIED до установки.

// parseFile построчно прогоняет JSONL-файл через ParseStream.
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
	events := parseFile(t, "testdata/normal.jsonl")

	// thread.started → session.init (thread_id = session id)
	init := eventsOfKind(events, harness.EventSessionInit)
	require.Len(t, init, 1)
	assert.Equal(t, "0199a213-81c0-7800-8aa1-bbab2a035a53", init[0].SessionID)

	// item.started command_execution → tool_call.start
	calls := eventsOfKind(events, harness.EventToolCallStart)
	require.Len(t, calls, 1)
	assert.Equal(t, "item_1", calls[0].CallID)
	assert.Equal(t, "command_execution", calls[0].Tool)
	assert.JSONEq(t, `{"command":"bash -lc ls"}`, string(calls[0].ToolInput))

	// item.completed command_execution → tool_result
	results := eventsOfKind(events, harness.EventToolResult)
	require.Len(t, results, 1)
	assert.Equal(t, "item_1", results[0].CallID)
	assert.Contains(t, results[0].ToolOutput, "file1.go")
	assert.False(t, results[0].ToolIsErr)

	// reasoning → assistant.thinking
	thinking := eventsOfKind(events, harness.EventAssistantThinking)
	require.Len(t, thinking, 1)
	assert.Equal(t, "Надо посмотреть файлы проекта.", thinking[0].Text)

	// agent_message → assistant.text ЦЕЛИКОМ (text-delta у codex нет)
	texts := eventsOfKind(events, harness.EventAssistantText)
	require.Len(t, texts, 1)
	assert.Equal(t, "Готово: файлы на месте.", texts[0].Text)
	assert.Empty(t, eventsOfKind(events, harness.EventAssistantTextDelta))

	// turn.completed → usage
	usage := eventsOfKind(events, harness.EventUsage)
	require.Len(t, usage, 1)
	require.NotNil(t, usage[0].Usage)
	assert.Equal(t, int64(24763), usage[0].Usage.Input)
	assert.Equal(t, int64(24448), usage[0].Usage.CacheRead)
	assert.Equal(t, int64(122), usage[0].Usage.Output)

	assert.Empty(t, eventsOfKind(events, harness.EventError))
	assert.Empty(t, eventsOfKind(events, harness.EventRaw))
}

func TestParseStream_ErrorRun(t *testing.T) {
	events := parseFile(t, "testdata/error.jsonl")

	// exit_code != 0 → tool_result с ToolIsErr
	results := eventsOfKind(events, harness.EventToolResult)
	require.Len(t, results, 1)
	assert.True(t, results[0].ToolIsErr)
	assert.Contains(t, results[0].ToolOutput, "compile error")

	// error (плоское message) и turn.failed (error.message) → EventError
	errs := eventsOfKind(events, harness.EventError)
	require.Len(t, errs, 2)
	require.NotNil(t, errs[0].Err)
	assert.Contains(t, errs[0].Err.Message, "rate limit")
	assert.True(t, errs[0].Err.Retriable) // 429 → retriable
	require.NotNil(t, errs[1].Err)
	assert.Contains(t, errs[1].Err.Message, "503")
	assert.True(t, errs[1].Err.Retriable)

	assert.Empty(t, eventsOfKind(events, harness.EventRaw))
}

func TestParseStream_BrokenLines(t *testing.T) {
	events := parseFile(t, "testdata/broken.jsonl")

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
}

func TestParseStream_KnownNoiseSkipped(t *testing.T) {
	// turn.started / item.updated / неизвестные item-типы — не raw и не
	// события: это штатные части протокола без полезной нагрузки.
	for _, line := range []string{
		`{"type":"turn.started"}`,
		`{"type":"item.updated","item":{"id":"i1","type":"command_execution","aggregated_output":"x"}}`,
		`{"type":"item.started","item":{"id":"i2","type":"agent_message"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"file_change","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i4","type":"todo_list"}}`,
	} {
		assert.Empty(t, New().ParseStream([]byte(line)), line)
	}
}

func TestParseStream_UnknownTypeRaw(t *testing.T) {
	events := New().ParseStream([]byte(`{"type":"some.future.event","x":1}`))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

func TestParseStream_TruncatesToolOutput(t *testing.T) {
	big := strings.Repeat("x", harness.ToolOutputLimit+100)
	line := `{"type":"item.completed","item":{"id":"i1","type":"command_execution",` +
		`"aggregated_output":"` + big + `","exit_code":0,"status":"completed"}}`
	events := New().ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Less(t, len(events[0].ToolOutput), harness.ToolOutputLimit+100)
	assert.Contains(t, events[0].ToolOutput, "[truncated]")
}

func TestParseStream_TurnCompletedNoUsage(t *testing.T) {
	events := New().ParseStream([]byte(`{"type":"turn.completed"}`))
	assert.Empty(t, events) // usage не гарантирован — не выдумываем
}

func TestExtractSessionID(t *testing.T) {
	h := New()

	id, err := h.ExtractSessionID([]harness.Event{
		{Kind: harness.EventAssistantText, Text: "hi"},
		{Kind: harness.EventSessionInit, SessionID: "thread-1"},
	}, "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "thread-1", id)

	_, err = h.ExtractSessionID(nil, "/tmp")
	require.Error(t, err)
	assert.ErrorIs(t, err, errNoSessionID)
}

func TestBuildCommand_Basic(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: "сделай задачу", WorkDir: "/tmp/proj"})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"codex", "exec", "сделай задачу",
		"--json",
		"--sandbox", "workspace-write",
		"--skip-git-repo-check",
	}, spec.Argv)
	assert.Empty(t, spec.Stdin)
	// WorkDir НЕ в argv — supervisor задаёт cmd.Dir
	for _, arg := range spec.Argv {
		assert.NotContains(t, arg, "/tmp/proj")
	}
}

func TestBuildCommand_ModelAndEffort(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt: "p",
		Model:  "gpt-5-codex",
		Effort: "high",
	})
	require.NoError(t, err)
	assert.Contains(t, spec.Argv, "-m")
	assert.Contains(t, spec.Argv, "gpt-5-codex")
	// UNVERIFIED enum — пробрасываем как -c override (матрица)
	assert.Contains(t, spec.Argv, "-c")
	assert.Contains(t, spec.Argv, "model_reasoning_effort=high")
}

func TestBuildCommand_ResumeRepassesAllFlags(t *testing.T) {
	// Доки: resume сохраняет ТОЛЬКО контекст диалога — все флаги заново.
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:    "продолжи",
		SessionID: "thread-1",
		Model:     "gpt-5-codex",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"codex", "exec", "resume", "thread-1", "продолжи"}, spec.Argv[:5])
	assert.Contains(t, spec.Argv, "--json")
	assert.Contains(t, spec.Argv, "--sandbox")
	assert.Contains(t, spec.Argv, "workspace-write")
	assert.Contains(t, spec.Argv, "--skip-git-repo-check")
	assert.Contains(t, spec.Argv, "-m")
	assert.Contains(t, spec.Argv, "gpt-5-codex")
}

func TestBuildCommand_StructuredOutput(t *testing.T) {
	// UNVERIFIED (только доки): --output-schema — ПУТЬ к файлу схемы + -o.
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:              "вердиkt",
		SchemaForStructured: `{"type":"object"}`,
	})
	require.NoError(t, err)

	schemaIdx, outIdx := -1, -1
	for i, arg := range spec.Argv {
		switch arg {
		case "--output-schema":
			schemaIdx = i
		case "-o":
			outIdx = i
		}
	}
	require.NotEqual(t, -1, schemaIdx)
	require.NotEqual(t, -1, outIdx)
	schemaPath := spec.Argv[schemaIdx+1]
	defer func() { _ = os.RemoveAll(filepath.Dir(schemaPath)) }()

	content, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(content))
	assert.Equal(t, filepath.Dir(schemaPath), filepath.Dir(spec.Argv[outIdx+1]))
}

func TestBuildCommand_LongPromptViaStdin(t *testing.T) {
	long := strings.Repeat("а", promptStdinThreshold+1)
	spec, err := New().BuildCommand(harness.LaunchSpec{Prompt: long})
	require.NoError(t, err)

	assert.Equal(t, long, spec.Stdin)
	// `codex exec -` — весь промпт из stdin (матрица)
	assert.Equal(t, "-", spec.Argv[2])
}

func TestBuildCommand_ExtraEnv(t *testing.T) {
	spec, err := New().BuildCommand(harness.LaunchSpec{
		Prompt:   "p",
		ExtraEnv: map[string]string{"CODEX_API_KEY": "k"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"CODEX_API_KEY=k"}, spec.Env)
}

func TestBuildCommand_EmptyPrompt(t *testing.T) {
	_, err := New().BuildCommand(harness.LaunchSpec{})
	require.Error(t, err)
}

func TestCapabilities(t *testing.T) {
	caps := New().Capabilities()
	assert.True(t, caps.StreamJSON)
	assert.True(t, caps.Resume)
	assert.Nil(t, caps.EffortLevels) // enum UNVERIFIED — ручка disabled (матрица)
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
		{"429 Too Many Requests: rate limit exceeded", true},
		{"HTTP 529: provider overloaded", true},
		{"503 Service Unavailable", true},
		{"connection reset by peer", true},
		{"dial tcp: network is unreachable", true},
		{"request timed out", true},
		{"invalid API key", false},
		{"sandbox permission denied", false},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, isRetriable(tc.msg), tc.msg)
	}
}
