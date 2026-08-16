package kimi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/harness"
	"github.com/fableFM/glamor/internal/harness/kimi"
)

func TestCapabilities(t *testing.T) {
	caps := kimi.New().Capabilities()

	assert.True(t, caps.StreamJSON)
	assert.True(t, caps.Resume)
	assert.Equal(t, []string{"low", "high", "max"}, caps.EffortLevels)
	assert.False(t, caps.CostReporting)
	assert.False(t, caps.StructuredJSON)
	assert.True(t, caps.ACP)
	assert.False(t, caps.Thinking) // thinking — в stderr, не в stdout
	assert.Equal(t, harness.UsageSourceWirefile, caps.UsageSource)
}

// Базовая сборка: kimi -p <prompt> --output-format stream-json, без yolo/auto.
func TestBuildCommand_Basic(t *testing.T) {
	spec, err := kimi.New().BuildCommand(harness.LaunchSpec{Prompt: "напиши hello.txt"})
	require.NoError(t, err)

	assert.Equal(t, []string{"kimi", "-p", "напиши hello.txt", "--output-format", "stream-json"}, spec.Argv)
	assert.Empty(t, spec.Env)
	assert.Empty(t, spec.Stdin)

	joined := strings.Join(spec.Argv, " ")
	assert.NotContains(t, joined, "--yolo")
	assert.NotContains(t, joined, "--auto")
}

// Модель → -m, effort → env KIMI_MODEL_THINKING_EFFORT, ExtraEnv → env.
func TestBuildCommand_ModelEffortEnv(t *testing.T) {
	spec, err := kimi.New().BuildCommand(harness.LaunchSpec{
		Prompt:   "x",
		Model:    "k2-thinking",
		Effort:   "high",
		ExtraEnv: map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"kimi", "-m", "k2-thinking", "-p", "x", "--output-format", "stream-json"}, spec.Argv)
	assert.Contains(t, spec.Env, "KIMI_MODEL_THINKING_EFFORT=high")
	assert.Contains(t, spec.Env, "FOO=bar")
}

// Resume: -S <id> перед -p; модель и effort передаются заново.
func TestBuildCommand_Resume(t *testing.T) {
	spec, err := kimi.New().BuildCommand(harness.LaunchSpec{
		Prompt:    "верни исправленный JSON",
		SessionID: "session_abc",
		Model:     "k2",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"kimi", "-S", "session_abc", "-m", "k2",
		"-p", "верни исправленный JSON", "--output-format", "stream-json",
	}, spec.Argv)
}

// WorkDir и ArtifactContract в argv/env не попадают (транспорт только;
// WorkDir — через cmd.Dir, промпт собирает supervisor).
func TestBuildCommand_WorkDirNotInArgv(t *testing.T) {
	spec, err := kimi.New().BuildCommand(harness.LaunchSpec{
		Prompt:  "x",
		WorkDir: "/tmp/checkout",
		ArtifactContract: harness.ArtifactSpec{
			Path:     ".glamor/runs/1/spec.md",
			Required: true,
		},
	})
	require.NoError(t, err)

	joined := strings.Join(spec.Argv, "\x00")
	assert.NotContains(t, joined, "/tmp/checkout")
	assert.NotContains(t, joined, ".glamor/runs/1/spec.md")
}

// Длинный промпт (>100KB) — не через argv: временный файл + короткая
// инструкция «прочитай файл».
func TestBuildCommand_LongPromptViaFile(t *testing.T) {
	long := strings.Repeat("очень длинный промпт. ", 6*1024) // >100KB
	spec, err := kimi.New().BuildCommand(harness.LaunchSpec{Prompt: long})
	require.NoError(t, err)

	// В argv — короткая инструкция, а не сам промпт.
	var promptArg string
	for i, a := range spec.Argv {
		if a == "-p" && i+1 < len(spec.Argv) {
			promptArg = spec.Argv[i+1]
		}
	}
	require.NotEmpty(t, promptArg)
	assert.Less(t, len(promptArg), 1024)
	assert.Contains(t, promptArg, "Прочитай файл ")

	// Файл существует и содержит исходный промпт целиком.
	path := strings.TrimPrefix(strings.TrimSuffix(promptArg, " и выполни инструкции из него."), "Прочитай файл ")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, long, string(data))
}

// Session id — из первого события с SessionID (обычно resume_hint).
func TestExtractSessionID_FromHint(t *testing.T) {
	events := parseFile(t, "testdata/normal_run.ndjson")
	id, err := kimi.New().ExtractSessionID(events, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "session_11111111-2222-3333-4444-555555555555", id)
}

// Fallback: resume_hint нет — любое событие с session_id в потоке.
func TestExtractSessionID_Fallback(t *testing.T) {
	h := kimi.New()
	events := h.ParseStream([]byte(
		`{"role":"assistant","content":"ok","session_id":"session_fallback"}`))
	id, err := h.ExtractSessionID(events, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "session_fallback", id)
}

// Нет ни одного события с session_id → ошибка.
func TestExtractSessionID_NotFound(t *testing.T) {
	h := kimi.New()
	events := h.ParseStream([]byte(`{"role":"assistant","content":"ok"}`))

	_, err := h.ExtractSessionID(events, t.TempDir())
	require.Error(t, err)

	_, err = h.ExtractSessionID(nil, t.TempDir())
	require.Error(t, err)
}

func TestNameBinary(t *testing.T) {
	h := kimi.New()
	assert.Equal(t, "kimi", h.Name())
	assert.Equal(t, "kimi", h.BinaryName())
}
