// Package codex — harness-адаптер OpenAI Codex CLI (T-25).
//
// ВНИМАНИЕ: CLI локально НЕ установлен — все факты только по официальной
// документации (tasks/research/harness-capability-matrix.md, раздел
// «## 4. codex»; developers.openai.com/codex/noninteractive, docs/exec.md).
// Всё неподтверждённое помечено UNVERIFIED; golden-файлы синтезированы
// по примерам из доков, реальный CLI НЕ запускался. E2E отложен до
// локальной установки (T-25).
//
// Ключевые особенности против claude/qwen:
//   - JSONL-события item-ориентированные (item.started/completed), НЕ
//     message-ориентированные; text-delta стрима НЕТ — agent_message
//     приходит целиком (item.completed) → живой стрим ограничен
//     reasoning/tool событиями;
//   - session id = thread.started.thread_id;
//   - resume: `codex exec resume <id> "<новое>"` — сохраняется ТОЛЬКО
//     контекст диалога, ВСЕ флаги (model/sandbox/json) передаём заново
//     на каждый resume (доки, exec.md);
//   - аппрувов в exec нет вообще; уровень доступа = --sandbox
//     (для кодера: workspace-write — дополнение T-25).
package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fableFM/glamor/internal/harness"
)

const (
	adapterName = "codex"
	binaryName  = "codex"

	// sandboxProfile — профиль песочницы для этапов пайплайна (дополнение
	// T-25: sandbox-профиль для кодера — workspace-write).
	sandboxProfile = "workspace-write"

	// promptStdinThreshold — порог длины промпта: длиннее — `codex exec -`
	// с полным промптом в stdin (матрица, «Headless»; защита от лимита
	// ARG_MAX, см. harness.CommandSpec.Stdin).
	promptStdinThreshold = 100 * 1024
)

// errNoSessionID — в потоке не нашлось ни одного события с thread_id.
var errNoSessionID = errors.New("session id отсутствует в потоке")

type adapter struct{}

// New конструирует адаптер codex.
func New() harness.Harness { return &adapter{} }

func (a *adapter) Name() string       { return adapterName }
func (a *adapter) BinaryName() string { return binaryName }

func (a *adapter) Capabilities() harness.Capabilities {
	return harness.Capabilities{
		StreamJSON: true, // `codex exec --json` → JSONL (доки)
		Resume:     true, // `codex exec resume <id> "…"` (доки)
		// EffortLevels: точный enum model_reasoning_effort для текущей
		// версии UNVERIFIED (матрица, «Модель/effort») → nil, ручка
		// disabled в UI (D-43), пока не подтвердим на установленном CLI.
		EffortLevels:   nil,
		CostReporting:  true, // turn.completed.usage в стриме
		StructuredJSON: true, // --output-schema + -o (UNVERIFIED — только доки)
		// ACP: нативно НЕТ — только внешний адаптер
		// @zed-industries/codex-acp (матрица, «ACP») → false.
		ACP: false,
		// Thinking: reasoning items в стриме (матрица, «JSON»).
		Thinking:    true,
		UsageSource: harness.UsageSourceStream,
	}
}

// BuildCommand собирает
// `codex exec "<prompt>" --json --sandbox workspace-write --skip-git-repo-check`.
//
// Правила (матрица, секция codex — ТОЛЬКО доки):
//   - resume: `codex exec resume <id> "<новое>"` при spec.SessionID != "";
//     ВСЕ флаги (model/sandbox/json/schema) передаём заново — resume
//     сохраняет только контекст диалога (доки, exec.md; issues #11750,
//     #32061);
//   - модель: `-m <model>`;
//   - effort: `-c model_reasoning_effort=<effort>` — override config.toml;
//     точный enum уровней UNVERIFIED (матрица), поэтому EffortLevels в
//     capabilities = nil, но если supervisor всё же передал effort —
//     пробрасываем как есть;
//   - structured: `--output-schema <file>` принимает ПУТЬ к файлу схемы
//     (не inline JSON) + `-o <file>` финального ответа (UNVERIFIED по
//     докам); схему пишем во временный файл;
//   - длинный промпт: `codex exec -` — весь промпт из stdin; форма
//     `codex exec resume <id> -` (resume + stdin) — UNVERIFIED;
//   - WorkDir в argv не попадает — supervisor задаёт cmd.Dir.
//
// Argv[0] — имя бинаря; вызывающий заменяет его на резолвнутый путь
// (Registry.BinaryPath). Env — дополнительные переменные к окружению
// процесса (не полное окружение).
func (a *adapter) BuildCommand(spec harness.LaunchSpec) (harness.CommandSpec, error) {
	if spec.Prompt == "" {
		return harness.CommandSpec{}, fmt.Errorf("codex: пустой промпт в LaunchSpec")
	}

	argv := []string{binaryName, "exec"}
	if spec.SessionID != "" {
		// Resume = `codex exec resume <id> "<новое>"`; все флаги заново
		// (доки: сохраняется только контекст диалога).
		argv = append(argv, "resume", spec.SessionID)
	}

	// Длинный промпт — не через argv (лимит ARG_MAX): `exec -` + stdin.
	var stdin string
	prompt := spec.Prompt
	if len(prompt) > promptStdinThreshold {
		stdin = prompt
		prompt = "-" // UNVERIFIED для resume-формы (матрица описывает `exec -`)
	}
	argv = append(argv, prompt,
		"--json",
		"--sandbox", sandboxProfile,
		"--skip-git-repo-check", // чекаут может быть вне git-репо (матрица)
	)

	if spec.Model != "" {
		argv = append(argv, "-m", spec.Model)
	}
	if spec.Effort != "" {
		// UNVERIFIED: enum model_reasoning_effort (minimal…xhigh по
		// third-party — матрица, «Модель/effort»).
		argv = append(argv, "-c", "model_reasoning_effort="+spec.Effort)
	}
	if spec.SchemaForStructured != "" {
		// UNVERIFIED (только доки): --output-schema принимает путь к файлу
		// со strict JSON Schema; -o — файл финального ответа (финальный
		// JSON при этом остаётся и в stdout — матрица, «Structured output»).
		schemaPath, outPath, err := writeStructuredFiles(spec.SchemaForStructured)
		if err != nil {
			return harness.CommandSpec{}, err
		}
		argv = append(argv, "--output-schema", schemaPath, "-o", outPath)
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

// writeStructuredFiles пишет JSON-схему во временный файл для
// --output-schema и резервирует путь для -o. Файлы адаптером НЕ удаляются —
// они нужны процессу до конца рана (как temp-промпт у kimi-адаптера);
// очистка — за пределами скоупа адаптера.
func writeStructuredFiles(schema string) (schemaPath, outPath string, err error) {
	dir, err := os.MkdirTemp("", "glamor-codex-schema-")
	if err != nil {
		return "", "", fmt.Errorf("codex: создать temp-директорию для схемы: %w", err)
	}
	schemaPath = filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
		return "", "", fmt.Errorf("codex: записать JSON-схему во временный файл: %w", err)
	}
	return schemaPath, filepath.Join(dir, "last-message.json"), nil
}

// ExtractSessionID — thread.started.thread_id (это и есть session id для
// resume — матрица, «Session id»); обход по порядку — первое событие с
// SessionID != "". workDir не нужен (id всегда приходит в стриме) —
// только для диагностики.
func (a *adapter) ExtractSessionID(events []harness.Event, workDir string) (string, error) {
	for _, ev := range events {
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("codex: извлечь session id (workDir %q): %w", workDir, errNoSessionID)
}
