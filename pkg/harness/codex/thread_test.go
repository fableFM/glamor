package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestThread_Run(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{
			`{"type":"thread.started","thread_id":"thread-123"}`,
			`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"done"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":3}}`,
		},
	})

	network := true
	client := mustNewForTest(t, Options{
		CodexPath: fake.path,
		BaseURL:   "https://example.test/v1",
		APIKey:    "test-key",
		Config: ConfigObject{
			"show_raw_agent_reasoning": true,
		},
	})
	thread := client.StartThread(ThreadOptions{
		Model:                "gpt-test",
		SandboxMode:          SandboxModeWorkspaceWrite,
		WorkingDirectory:     "/tmp/repo",
		AdditionalDirs:       []string{"/tmp/extra"},
		SkipGitRepoCheck:     true,
		ModelReasoningEffort: ModelReasoningEffortHigh,
		NetworkAccessEnabled: &network,
		WebSearchMode:        WebSearchModeLive,
		ApprovalPolicy:       ApprovalModeOnRequest,
	})

	turn, err := thread.Run(context.Background(), "hello codex")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if thread.ID() != "thread-123" {
		t.Fatalf("thread ID = %q, want %q", thread.ID(), "thread-123")
	}
	if turn.FinalResponse != "done" {
		t.Fatalf("final response = %q, want %q", turn.FinalResponse, "done")
	}
	if turn.Usage == nil || turn.Usage.ReasoningOutputTokens != 3 {
		t.Fatalf("usage = %#v, want reasoning output tokens 3", turn.Usage)
	}
	if got := strings.TrimSpace(readFile(t, fake.stdinPath)); got != "hello codex" {
		t.Fatalf("stdin = %q, want %q", got, "hello codex")
	}

	wantArgs := []string{
		"exec",
		"--experimental-json",
		"--config",
		"show_raw_agent_reasoning=true",
		"--config",
		`openai_base_url="https://example.test/v1"`,
		"--model",
		"gpt-test",
		"--sandbox",
		"workspace-write",
		"--cd",
		"/tmp/repo",
		"--add-dir",
		"/tmp/extra",
		"--skip-git-repo-check",
		"--config",
		`model_reasoning_effort="high"`,
		"--config",
		"sandbox_workspace_write.network_access=true",
		"--config",
		`web_search="live"`,
		"--config",
		`approval_policy="on-request"`,
	}
	if got := readLines(t, fake.argsPath); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("args = %#v, want %#v", got, wantArgs)
	}
	if got := readFile(t, fake.apiKeyPath); strings.TrimSpace(got) != "test-key" {
		t.Fatalf("CODEX_API_KEY = %q, want %q", strings.TrimSpace(got), "test-key")
	}
}

func TestThread_Run_Resume(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{
			`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"resumed"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0}}`,
		},
	})
	client := mustNewForTest(t, Options{CodexPath: fake.path})
	thread := client.ResumeThread("thread-old", ThreadOptions{})

	turn, err := thread.Run(context.Background(), "continue")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if turn.FinalResponse != "resumed" {
		t.Fatalf("final response = %q, want %q", turn.FinalResponse, "resumed")
	}

	wantArgs := []string{"exec", "--experimental-json", "resume", "thread-old"}
	if got := readLines(t, fake.argsPath); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("args = %#v, want %#v", got, wantArgs)
	}
}

func TestThread_RunStream(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{
			`{"type":"thread.started","thread_id":"thread-123"}`,
			`{"type":"item.updated","item":{"id":"item-1","type":"agent_message","text":"hel"}}`,
			`{"type":"item.updated","item":{"id":"item-1","type":"agent_message","text":"hello"}}`,
			`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"hello!"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`,
		},
	})
	client := mustNewForTest(t, Options{CodexPath: fake.path})
	thread := client.StartThread(ThreadOptions{})

	chunks, errs := thread.RunStream(context.Background(), "stream")

	var got []string
	for chunk := range chunks {
		got = append(got, chunk.Text)
		if chunk.ItemType != ItemAgentMessage {
			t.Fatalf("chunk item type = %q, want %q", chunk.ItemType, ItemAgentMessage)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("RunStream() error = %v", err)
		}
	}

	want := []string{"hel", "lo", "!"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks = %#v, want %#v", got, want)
	}
}

func TestThread_Run_TurnFailed(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{
			`{"type":"thread.started","thread_id":"thread-123"}`,
			`{"type":"turn.failed","error":{"message":"boom"}}`,
		},
	})
	client := mustNewForTest(t, Options{CodexPath: fake.path})
	thread := client.StartThread(ThreadOptions{})

	_, err := thread.Run(context.Background(), "fail")
	if !errors.Is(err, ErrTurnFailed) {
		t.Fatalf("Run() error = %v, want ErrTurnFailed", err)
	}
	var turnErr *TurnFailedError
	if !errors.As(err, &turnErr) {
		t.Fatalf("Run() error = %T, want *TurnFailedError", err)
	}
	if turnErr.ThreadError == nil || turnErr.ThreadError.Message != "boom" {
		t.Fatalf("turn error = %#v, want boom", turnErr.ThreadError)
	}
	var threadErr *ThreadError
	if !errors.As(err, &threadErr) || threadErr.Message != "boom" {
		t.Fatalf("thread error = %#v, want boom", threadErr)
	}
}

func TestThread_Run_EventError(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{
			`{"type":"thread.started","thread_id":"thread-123"}`,
			`{"type":"error","message":"stream boom"}`,
		},
	})
	client := mustNewForTest(t, Options{CodexPath: fake.path})
	thread := client.StartThread(ThreadOptions{})

	_, err := thread.Run(context.Background(), "fail")
	if !errors.Is(err, ErrThreadEvent) {
		t.Fatalf("Run() error = %v, want ErrThreadEvent", err)
	}
	var eventErr *ThreadEventError
	if !errors.As(err, &eventErr) {
		t.Fatalf("Run() error = %T, want *ThreadEventError", err)
	}
	if eventErr.Message != "stream boom" {
		t.Fatalf("event error message = %q, want %q", eventErr.Message, "stream boom")
	}
}

func TestThread_Run_DecodeError(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{"not-json"},
	})
	client := mustNewForTest(t, Options{CodexPath: fake.path})
	thread := client.StartThread(ThreadOptions{})

	_, err := thread.Run(context.Background(), "fail")
	if !errors.Is(err, ErrDecodeEvent) {
		t.Fatalf("Run() error = %v, want ErrDecodeEvent", err)
	}
	var decodeErr *DecodeEventError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("Run() error = %T, want *DecodeEventError", err)
	}
	if string(decodeErr.Line) != "not-json" {
		t.Fatalf("decode line = %q, want %q", string(decodeErr.Line), "not-json")
	}
}

func TestThread_Run_ConfigError(t *testing.T) {
	fake := newFakeCodex(t, fakeCodexOptions{
		events: []string{
			`{"type":"thread.started","thread_id":"thread-123"}`,
		},
	})
	client := mustNewForTest(t, Options{
		CodexPath: fake.path,
		Config: ConfigObject{
			"bad": nil,
		},
	})
	thread := client.StartThread(ThreadOptions{})

	_, err := thread.Run(context.Background(), "fail")
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("Run() error = %v, want ErrConfig", err)
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("Run() error = %T, want *ConfigError", err)
	}
	if configErr.Path != "bad" {
		t.Fatalf("config error path = %q, want %q", configErr.Path, "bad")
	}
}

func TestSerializeConfigOverrides(t *testing.T) {
	t.Parallel()

	got, err := serializeConfigOverrides(ConfigObject{
		"alpha": "x",
		"nested": ConfigObject{
			"enabled": true,
			"limit":   3,
		},
	})
	if err != nil {
		t.Fatalf("serializeConfigOverrides() error = %v", err)
	}

	want := []string{
		`alpha="x"`,
		"nested.enabled=true",
		"nested.limit=3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overrides = %#v, want %#v", got, want)
	}
}

type fakeCodex struct {
	path       string
	argsPath   string
	stdinPath  string
	apiKeyPath string
}

type fakeCodexOptions struct {
	events []string
}

func newFakeCodex(t *testing.T, options fakeCodexOptions) fakeCodex {
	t.Helper()

	dir := t.TempDir()
	fake := fakeCodex{
		path:       filepath.Join(dir, "codex"),
		argsPath:   filepath.Join(dir, "args.txt"),
		stdinPath:  filepath.Join(dir, "stdin.txt"),
		apiKeyPath: filepath.Join(dir, "api-key.txt"),
	}

	eventsPath := filepath.Join(dir, "events.txt")
	if err := os.WriteFile(eventsPath, []byte(strings.Join(options.events, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write events fixture: %v", err)
	}

	script := `#!/bin/sh
: > "$ARGS_FILE"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$ARGS_FILE"
done
cat > "$STDIN_FILE"
printf '%s\n' "${CODEX_API_KEY:-}" > "$API_KEY_FILE"
cat "$EVENTS_FILE"
`
	if err := os.WriteFile(fake.path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("ARGS_FILE", fake.argsPath)
	t.Setenv("STDIN_FILE", fake.stdinPath)
	t.Setenv("API_KEY_FILE", fake.apiKeyPath)
	t.Setenv("EVENTS_FILE", eventsPath)

	return fake
}

func mustNewForTest(t *testing.T, options Options) *Codex {
	t.Helper()

	client, err := New(options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()

	content := strings.TrimSpace(readFile(t, path))
	if content == "" {
		return []string{}
	}
	return strings.Split(content, "\n")
}
