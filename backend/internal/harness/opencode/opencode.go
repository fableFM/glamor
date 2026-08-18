// Package opencode — адаптер harness'а opencode CLI (факты проверены на
// 1.2.27; T-26). Формат стрима и capabilities —
// tasks/research/harness-capability-matrix.md, секция «## 5. opencode»;
// реальный CLI в юнит-тестах не запускается (это стоит API-вызовы),
// golden-файлы синтезированы по матрице и takopi.dev cheatsheet.
//
// Ключевые особенности CLI (матрица):
//   - NDJSON-конверт {type, timestamp(epoch ms), sessionID, part? | error?};
//     типы: step_start, text, reasoning, tool_use, step_finish, error;
//   - tool_use эмитится ТОЛЬКО по завершении (status=="completed"|"error") —
//     стрима «tool started» нет → адаптер эмитит пару start=end (см. stream.go);
//   - финальный step_finish может НЕ прийти (баг #26855) → usage не
//     гарантирован; post-hoc fallback — `opencode export <sessionID>`;
//   - модели в событиях нет (баг #40544) — Event.Model не заполняется;
//   - транспорт `opencode serve` (headless HTTP API, порт 4096) осознанно
//     НЕ реализуем: для M1 достаточно CLI-стрима, serve — кандидат на
//     будущее (экономия MCP cold-boot через --attach, возможный structured
//     output). Выбор транспорта инкапсулирован в адаптере (T-26).
package opencode

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fableFM/glamor/internal/harness"
)

const (
	adapterName = "opencode"
	binaryName  = "opencode"

	// maxArgvPromptLen — порог длины промпта для передачи через argv
	// (лимит ARG_MAX). stdin в `opencode run` UNVERIFIED (матрица,
	// «Headless»), поэтому длинный промпт пишем во временный файл, а в argv
	// кладём короткую инструкцию «прочитай файл» — как в kimi-адаптере.
	maxArgvPromptLen = 100 * 1024
)

// errNoSessionID — в потоке не нашлось ни одного события с sessionID.
var errNoSessionID = errors.New("session id отсутствует в потоке")

type adapter struct{}

// New конструирует адаптер opencode.
func New() harness.Harness { return adapter{} }

func (adapter) Name() string       { return adapterName }
func (adapter) BinaryName() string { return binaryName }

func (adapter) Capabilities() harness.Capabilities {
	return harness.Capabilities{
		StreamJSON: true,
		// Resume: `opencode run -s <sessionID> "<новое>"` — флаг есть в help
		// 1.2.27, но end-to-end поведение (порядок сообщений, контекст)
		// UNVERIFIED (матрица, риск 7) — проверяется e2e (build tag e2e).
		Resume: true,
		// EffortLevels: effort задаётся `--variant <строка>`, но значения
		// provider-specific (high, max, minimal…; матрица, «Модель/effort»),
		// фиксированного enum нет → nil, ручка disabled в UI (D-43).
		EffortLevels: nil,
		// CostReporting: step_finish несёт tokens+cost (матрица), НО финальный
		// step_finish может не эмититься (баг #26855) → usage в стриме не
		// гарантирован. Post-hoc fallback: `opencode export <sessionID>`
		// отдаёт полный JSON сессии (хранилище — SQLite
		// ~/.local/share/opencode/opencode.db); реализация fallback'а — за
		// пределами M1, UsageSource оставлен stream.
		CostReporting: true,
		// StructuredJSON: отдельного флага нет (UNVERIFIED; возможно, есть в
		// HTTP API opencode serve — не проверено) → промпт-контракт +
		// валидация (T-17).
		StructuredJSON: false,
		// ACP: нативный `opencode acp` (stdio nd-JSON) — запас на M4.
		ACP: true,
		// Thinking: reasoning-события парсим (assistant.thinking); CLI шлёт
		// их при `--thinking` — в базовую команду флаг не включён (ТЗ T-26).
		Thinking:    true,
		UsageSource: harness.UsageSourceStream,
	}
}

// BuildCommand собирает `opencode run "<prompt>" --format json --auto`.
//
// Правила (матрица, секция opencode, 1.2.27):
//   - --auto — авто-аппрув всего, что не запрещено permissions-конфигом;
//     флага --dangerously-skip-permissions в 1.2.27 НЕТ (ни в help, ни в
//     strings бинаря — матрица, «Auto-approve»), не добавляем;
//   - resume: `-s <id>` при spec.SessionID != "" (end-to-end UNVERIFIED);
//   - модель: `-m provider/model` при spec.Model != "";
//   - effort: `--variant <effort>` при spec.Effort != "" (provider-specific);
//   - spec.ArtifactContract в промпт НЕ подмешивается — промпт собирает
//     supervisor, BuildCommand только транспорт;
//   - WorkDir в argv не попадает — supervisor задаёт cmd.Dir.
//
// Argv[0] — имя бинаря; вызывающий заменяет его на резолвнутый путь
// (Registry.BinaryPath). Env — дополнительные переменные к окружению
// процесса (не полное окружение).
func (adapter) BuildCommand(spec harness.LaunchSpec) (harness.CommandSpec, error) {
	if spec.Prompt == "" {
		return harness.CommandSpec{}, fmt.Errorf("opencode: пустой промпт в LaunchSpec")
	}

	argv := []string{binaryName, "run"}
	if spec.SessionID != "" {
		// Resume = новый процесс с id + новым промптом (матрица, «Resume с
		// новым промптом»; end-to-end UNVERIFIED — риск 7).
		argv = append(argv, "-s", spec.SessionID)
	}
	if spec.Model != "" {
		argv = append(argv, "-m", spec.Model)
	}
	if spec.Effort != "" {
		argv = append(argv, "--variant", spec.Effort)
	}

	prompt := spec.Prompt
	if len(prompt) > maxArgvPromptLen {
		path, err := writePromptFile(prompt)
		if err != nil {
			return harness.CommandSpec{}, err
		}
		prompt = "Прочитай файл " + path + " и выполни инструкции из него."
	}
	argv = append(argv, prompt, "--format", "json", "--auto")

	extra := make([]string, 0, len(spec.ExtraEnv))
	for k, v := range spec.ExtraEnv {
		extra = append(extra, k+"="+v)
	}
	sort.Strings(extra) // детерминированность для тестов и логов

	return harness.CommandSpec{Argv: argv, Env: extra}, nil
}

// writePromptFile пишет длинный промпт во временный файл. Файл адаптером
// НЕ удаляется — он нужен процессу до конца рана; очистку директории
// (filepath.Dir(path)) выполняет supervisor (T-09) после завершения
// процесса, путь виден в промпте-инструкции.
func writePromptFile(prompt string) (string, error) {
	dir, err := os.MkdirTemp("", "glamor-opencode-prompt-")
	if err != nil {
		return "", fmt.Errorf("opencode: создать temp-директорию для промпта: %w", err)
	}
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		return "", fmt.Errorf("opencode: записать промпт во временный файл: %w", err)
	}
	return path, nil
}

// ExtractSessionID — sessionID из первого события с непустым полем
// (матрица, следствие «а»: из первого подходящего события, а не из
// фиксированного типа). opencode проставляет sessionID (`ses_…`) в каждое
// событие конверта, первое — step_start. workDir не используется:
// хранилище (~/.local/share/opencode) — запасной путь за пределами скоупа
// адаптера.
func (adapter) ExtractSessionID(events []harness.Event, _ string) (string, error) {
	for _, ev := range events {
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("opencode: извлечь session id: %w", errNoSessionID)
}
