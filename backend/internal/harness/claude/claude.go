// Package claude — harness-адаптер Claude Code (проверено на 2.1.81).
//
// Факты о CLI — tasks/research/harness-capability-matrix.md, раздел
// «## 3. claude»; реальный CLI в юнит-тестах не запускается (это стоит
// API-вызовы), golden-файлы синтезированы по матрице и живому прогону
// из ресёрча. Ключевые особенности против qwen (та же Claude-подобная
// NDJSON-схема):
//   - stream-json ТРЕБУЕТ --verbose (без него падает/молчит — матрица,
//     риск 5);
//   - --bare: CI-режим без hooks/MCP/CLAUDE.md (матрица, «Headless»);
//   - effort — НАТИВНЫЙ флаг --effort (у qwen его нет вовсе);
//   - system/api_retry: процесс может жить минутами в retry-лупе —
//     это «признак жизни» для stall-watchdog supervisor'а (T-09), а не
//     терминальная ошибка;
//   - --permission-mode вместо --approval-mode: этапы пайплайна
//     авто-одобрены (bypassPermissions по умолчанию).
//
// Напоминание: SIGTERM → exit 143, сессия на диске инкрементально и
// переживает прерывание (матрица, «Session id / хранилище») — обрабатывает
// supervisor, адаптеру exit code недоступен.
//
// Запас на M4-эксперимент (НЕ реализовано): --input-format stream-json
// даёт потоковый ввод — путь живого вмешательства без прерывания (T-25).
package claude

import (
	"errors"
	"fmt"
	"sort"

	"github.com/fableFM/glamor/internal/harness"
)

const (
	adapterName = "claude"
	binaryName  = "claude"

	// defaultPermissionMode — этапы пайплайна авто-одобрены (T-25):
	// headless-раны не должны останавливаться на permission-запросах.
	defaultPermissionMode = "bypassPermissions"

	// promptStdinThreshold — порог длины промпта: длиннее — короткий -p +
	// полный промпт в stdin (claude читает stdin до 10 МБ, prompt+stdin =
	// инструкция+контекст — матрица, «Headless»; защита от лимита ARG_MAX).
	promptStdinThreshold = 100 * 1024

	// stdinPromptStub — короткий -p при подаче полного промпта через stdin.
	stdinPromptStub = "The full task prompt is provided on stdin; follow it exactly."
)

// errNoSessionID — в потоке не нашлось ни одного события с session_id.
var errNoSessionID = errors.New("session id отсутствует в потоке")

type adapter struct {
	// permissionMode — значение --permission-mode (acceptEdits,
	// bypassPermissions, ...; матрица, «Auto-approve»).
	permissionMode string
}

// New конструирует адаптер claude. permissionMode — профиль авто-одобрения
// для этапов (фиксируется в конфиге пайплайна, T-25); пустая строка →
// bypassPermissions.
func New(permissionMode string) harness.Harness {
	if permissionMode == "" {
		permissionMode = defaultPermissionMode
	}
	return &adapter{permissionMode: permissionMode}
}

func (a *adapter) Name() string       { return adapterName }
func (a *adapter) BinaryName() string { return binaryName }

func (a *adapter) Capabilities() harness.Capabilities {
	return harness.Capabilities{
		StreamJSON: true,
		Resume:     true, // `claude -r <id> -p "…"` — офиц. доки (матрица)
		// EffortLevels: нативный флаг --effort low|medium|high|max
		// (матрица, «Модель/effort»).
		EffortLevels:   []string{"low", "medium", "high", "max"},
		CostReporting:  true, // result.usage + total_cost_usd в стриме
		StructuredJSON: true, // --json-schema → structured_output
		// ACP: нативно НЕТ — только внешний адаптер
		// @agentclientprotocol/claude-agent-acp (матрица, «ACP») → false.
		ACP: false,
		// Thinking: thinking-блоки приходят в assistant message content
		// (матрица, «stream-json»).
		Thinking:    true,
		UsageSource: harness.UsageSourceStream,
	}
}

// BuildCommand собирает
// `claude -p <prompt> --output-format stream-json --verbose --bare
// --permission-mode <mode> [...]`.
//
// Правила (матрица, секция claude + дополнение T-25):
//   - stream-json ТРЕБУЕТ --verbose — всегда в паре;
//   - --include-partial-messages: живой стрим text_delta/thinking_delta
//     (stream_event; T-25, «Headless»);
//   - --bare: CI-режим без hooks/MCP/CLAUDE.md;
//   - resume: `-r <id>` при spec.SessionID != ""; --json-schema передаём
//     ЗАНОВО на каждый resume — надо ли, UNVERIFIED (матрица, «Structured
//     output»), но у qwen это обязательно и повторная передача безвредна;
//   - модель: `--model`, effort: `--effort` (нативный флаг!);
//   - spec.ArtifactContract в промпт НЕ подмешивается — промпт собирает
//     supervisor, BuildCommand только транспорт;
//   - WorkDir в argv не попадает — supervisor задаёт cmd.Dir.
//
// Argv[0] — имя бинаря; вызывающий заменяет его на резолвнутый путь
// (Registry.BinaryPath). Env — дополнительные переменные к окружению
// процесса (не полное окружение).
func (a *adapter) BuildCommand(spec harness.LaunchSpec) (harness.CommandSpec, error) {
	if spec.Prompt == "" {
		return harness.CommandSpec{}, fmt.Errorf("claude: пустой промпт в LaunchSpec")
	}

	argv := []string{binaryName}
	if spec.SessionID != "" {
		// Resume = новый процесс с id + промптом (матрица, «Resume с новым
		// промптом»). Все per-run флаги передаём заново (см. --json-schema).
		argv = append(argv, "-r", spec.SessionID)
	}

	// Длинный промпт — не через argv (лимит ARG_MAX): короткий -p + полный
	// промпт в stdin (claude читает stdin до 10 МБ — матрица).
	var stdin string
	prompt := spec.Prompt
	if len(prompt) > promptStdinThreshold {
		stdin = prompt
		prompt = stdinPromptStub
	}

	argv = append(argv,
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose", // обязателен для stream-json (матрица, риск 5)
		"--include-partial-messages",
		"--bare",
		"--permission-mode", a.permissionMode,
	)
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	if spec.Effort != "" {
		argv = append(argv, "--effort", spec.Effort)
	}
	if spec.SchemaForStructured != "" {
		// UNVERIFIED: надо ли передавать схему на каждый resume (матрица,
		// «Structured output») — передаём, как у qwen.
		argv = append(argv, "--json-schema", spec.SchemaForStructured)
	}

	env := make([]string, 0, len(spec.ExtraEnv))
	keys := make([]string, 0, len(spec.ExtraEnv))
	for k := range spec.ExtraEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys) // детерминированный порядок для тестов/логов
	for _, k := range keys {
		env = append(env, k+"="+spec.ExtraEnv[k])
	}

	return harness.CommandSpec{Argv: argv, Env: env, Stdin: stdin}, nil
}

// ExtractSessionID — session id из первого подходящего события, а не из
// фиксированного типа (матрица, следствие «а»). У claude id есть в
// system/init и в result (матрица, «Session id») — обход по порядку
// покрывает оба варианта. workDir не нужен (id всегда приходит в стриме) —
// только для диагностики.
func (a *adapter) ExtractSessionID(events []harness.Event, workDir string) (string, error) {
	for _, ev := range events {
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("claude: извлечь session id (workDir %q): %w", workDir, errNoSessionID)
}
