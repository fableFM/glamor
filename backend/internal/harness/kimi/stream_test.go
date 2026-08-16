package kimi_test

// Golden-файлы в testdata синтезированы по примерам из
// tasks/research/harness-capability-matrix.md (секция «## 1. kimi»,
// kimi 0.36.0). Ральный CLI в тестах НЕ запускается — это стоит
// API-вызовы; e2e с живым kimi — отдельно под build tag `e2e` (T-07).

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/harness"
	"github.com/fableFM/glamor/internal/harness/kimi"
)

// parseFile прогоняет NDJSON-файл через ParseStream построчно.
func parseFile(t *testing.T, path string) []harness.Event {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	h := kimi.New()
	var events []harness.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		events = append(events, h.ParseStream(sc.Bytes())...)
	}
	require.NoError(t, sc.Err())
	return events
}

func kinds(events []harness.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

// Нормальный ран: resume_hint, текст, tool_call + tool_result.
func TestParseStream_NormalRun(t *testing.T) {
	events := parseFile(t, "testdata/normal_run.ndjson")

	assert.Equal(t, []string{
		harness.EventSessionInit,
		harness.EventSessionInit,
		harness.EventAssistantText,
		harness.EventToolCallStart,
		harness.EventToolResult,
		harness.EventAssistantText,
	}, kinds(events))

	// system.version — session.init без id, resume_hint — с id.
	assert.Empty(t, events[0].SessionID)
	assert.Equal(t, "session_11111111-2222-3333-4444-555555555555", events[1].SessionID)

	assert.Equal(t, "Сейчас создам файл hello.txt.", events[2].Text)

	assert.Equal(t, "call_abc123", events[3].CallID)
	assert.Equal(t, "Bash", events[3].Tool)
	assert.JSONEq(t, `{"command":"printf 'hello' > hello.txt"}`, string(events[3].ToolInput))

	assert.Equal(t, "call_abc123", events[4].CallID)
	assert.Equal(t, "Bash", events[4].Tool)
	assert.False(t, events[4].ToolIsErr)

	assert.Equal(t, "Готово: файл hello.txt создан.", events[5].Text)
}

// Не-JSON мусор (зеркало вывода foreground-Bash, Kent production) → raw,
// парсинг соседних строк не ломается.
func TestParseStream_NoisyRun(t *testing.T) {
	events := parseFile(t, "testdata/noisy_run.ndjson")

	assert.Equal(t, []string{
		harness.EventSessionInit,
		harness.EventAssistantText,
		harness.EventToolCallStart,
		harness.EventRaw,
		harness.EventRaw,
		harness.EventToolResult,
		harness.EventAssistantText,
	}, kinds(events))

	assert.Equal(t, `+ go build ./...`, events[3].Text)
	assert.Contains(t, events[4].Text, `imported and not used`)
	assert.Equal(t, "exit status 1", events[5].ToolOutput)
}

// Обрезанная последняя строка (краш процесса) → raw, без паники.
func TestParseStream_TruncatedTail(t *testing.T) {
	events := parseFile(t, "testdata/truncated_tail.ndjson")

	assert.Equal(t, []string{
		harness.EventSessionInit,
		harness.EventAssistantText,
		harness.EventRaw,
	}, kinds(events))
	assert.Contains(t, events[2].Text, "обрезанная строка")
}

// Битые строки не паникуют и дают raw; пустые строки — без событий.
func TestParseStream_BrokenLines(t *testing.T) {
	h := kimi.New()

	for _, line := range []string{
		`{"role":"assistant","content":`, // обрезок
		`{invalid json`,
		`"just a string"`,
		`12345`,
	} {
		events := h.ParseStream([]byte(line))
		require.Len(t, events, 1, "line %q", line)
		assert.Equal(t, harness.EventRaw, events[0].Kind)
	}

	assert.Empty(t, h.ParseStream(nil))
	assert.Empty(t, h.ParseStream([]byte("   ")))
}

// Валидный JSON неизвестной формы → raw (не теряем строку).
func TestParseStream_UnknownShape(t *testing.T) {
	h := kimi.New()

	events := h.ParseStream([]byte(`{"role":"user","content":"echo"}`))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)

	events = h.ParseStream([]byte(`{"role":"meta","type":"something.new"}`))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

// Assistant-сообщение с content не строкой (массив блоков) → raw.
func TestParseStream_AssistantNonStringContent(t *testing.T) {
	h := kimi.New()
	events := h.ParseStream([]byte(`{"role":"assistant","content":[{"type":"text"}]}`))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventRaw, events[0].Kind)
}

// Пустое assistant-сообщение (ни текста, ни tool_calls) — шум, без событий.
func TestParseStream_EmptyAssistant(t *testing.T) {
	h := kimi.New()
	assert.Empty(t, h.ParseStream([]byte(`{"role":"assistant","content":""}`)))
}

// Одно сообщение с текстом и tool_calls → два события: текст, затем вызов.
func TestParseStream_TextAndToolCall(t *testing.T) {
	h := kimi.New()
	line := `{"role":"assistant","content":"Читаю файл.","tool_calls":[{"id":"c1","function":{"name":"Read","arguments":{"path":"a.go"}}}]}`
	events := h.ParseStream([]byte(line))

	require.Len(t, events, 2)
	assert.Equal(t, harness.EventAssistantText, events[0].Kind)
	assert.Equal(t, "Читаю файл.", events[0].Text)
	assert.Equal(t, harness.EventToolCallStart, events[1].Kind)
	assert.Equal(t, "c1", events[1].CallID)
	assert.Equal(t, "Read", events[1].Tool)
	assert.JSONEq(t, `{"path":"a.go"}`, string(events[1].ToolInput))
}

// Вывод тулзы усекается до harness.ToolOutputLimit.
func TestParseStream_ToolOutputTruncated(t *testing.T) {
	h := kimi.New()
	big := strings.Repeat("x", harness.ToolOutputLimit+100)
	line := `{"role":"tool","tool_call_id":"c1","name":"Bash","content":"` + big + `"}`

	events := h.ParseStream([]byte(line))
	require.Len(t, events, 1)
	assert.Equal(t, harness.EventToolResult, events[0].Kind)
	assert.True(t, strings.HasSuffix(events[0].ToolOutput, "…[truncated]"))
	assert.LessOrEqual(t, len(events[0].ToolOutput), harness.ToolOutputLimit+len("…[truncated]"))
}

// Error-подобные сообщения → EventError с классификацией retriable.
func TestParseStream_ErrorEvents(t *testing.T) {
	h := kimi.New()

	tests := []struct {
		name      string
		line      string
		retriable bool
	}{
		{"rate limit", `{"role":"meta","type":"error","message":"HTTP 429: rate limit exceeded"}`, true},
		{"timeout", `{"role":"error","message":"connection timeout to api.moonshot.cn"}`, true},
		{"5xx", `{"role":"meta","type":"provider.error","message":"503 overloaded"}`, true},
		{"auth — не retriable", `{"role":"meta","type":"error","message":"invalid api key"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := h.ParseStream([]byte(tt.line))
			require.Len(t, events, 1)
			assert.Equal(t, harness.EventError, events[0].Kind)
			require.NotNil(t, events[0].Err)
			assert.Equal(t, tt.retriable, events[0].Err.Retriable)
		})
	}
}

// Usage в stdout у kimi отсутствует — парсер его не изобретает.
func TestParseStream_NoUsage(t *testing.T) {
	events := parseFile(t, "testdata/normal_run.ndjson")
	for _, ev := range events {
		assert.Nil(t, ev.Usage)
		assert.NotEqual(t, harness.EventUsage, ev.Kind)
	}
}
