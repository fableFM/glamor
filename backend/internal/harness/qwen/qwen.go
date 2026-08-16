// Package qwen — harness-адаптер Qwen Code (T-08).
//
// Факты о CLI — tasks/research/harness-capability-matrix.md, раздел
// «## 2. qwen (Qwen Code 0.21.3)». Ключевые расхождения с kimi:
//   - родной stream-json (NDJSON, Claude-подобная схема): usage и
//     терминальный result приходят прямо в стриме (у kimi usage нет в
//     stdout — только best-effort из wire.jsonl);
//   - structured output нативный: --json-schema → result.structured_result
//     (у kimi — только промпт-контракт + валидация на нашей стороне);
//   - session id есть и в system/init, и в result (у kimi — отдельное
//     session.resume_hint);
//   - длинный промпт подаём через stdin: qwen склеивает короткий -p со
//     stdin («Appended to input on stdin (if any)» — help, паттерн Kent).
//
// Напоминание: exit codes 53 (max-session-turns), 55 (budget exceeded),
// 130 (SIGINT) — равноправный канал истины (при 130/53 result-события
// может НЕ быть); их обрабатывает supervisor (T-09), адаптеру они
// недоступны (матрица, «stream-json / json»).
package qwen

import (
	"fmt"
	"sort"

	"github.com/fableFM/glamor/internal/harness"
)

const (
	adapterName = "qwen"
	binaryName  = "qwen"

	// promptStdinThreshold — порог длины промпта: длиннее — короткий -p +
	// полный промпт в stdin (паттерн Kent; защита от лимита ARG_MAX,
	// см. harness.CommandSpec.Stdin).
	promptStdinThreshold = 100 * 1024

	// stdinPromptStub — короткий -p при подаче полного промпта через stdin
	// (qwen склеивает -p со stdin — матрица, «Headless»).
	stdinPromptStub = "The full task prompt is provided on stdin; follow it exactly."
)

type adapter struct{}

// New конструирует адаптер qwen.
func New() harness.Harness { return adapter{} }

func (adapter) Name() string       { return adapterName }
func (adapter) BinaryName() string { return binaryName }

func (adapter) Capabilities() harness.Capabilities {
	return harness.Capabilities{
		StreamJSON: true,
		Resume:     true,
		// EffortLevels: CLI-флага effort НЕТ — только model.reasoningEffort
		// в settings.json (матрица, «Модель/effort»). Settings-override через
		// QWEN_CODE_SYSTEM_SETTINGS_PATH осознанно НЕ реализуем → nil, ручка
		// disabled в UI (D-43).
		EffortLevels:   nil,
		CostReporting:  true,
		StructuredJSON: true,
		// ACP: --experimental-acp (матрица, «ACP») — experimental, в пайплайне
		// не используем; несовместим с --json-schema. Флаг — запас на M4.
		ACP: true,
		// Thinking: thinking-блоки в stdout-стриме не включаем
		// (требуют enable_thinking; матрица, «Рекомендации»).
		Thinking:    false,
		UsageSource: harness.UsageSourceStream,
	}
}

func (adapter) BuildCommand(spec harness.LaunchSpec) (harness.CommandSpec, error) {
	if spec.Prompt == "" {
		return harness.CommandSpec{}, fmt.Errorf("qwen: пустой промпт в LaunchSpec")
	}

	// Только stream-json: `-o json` — это буферный JSON-МАССИВ в конце,
	// другой транспорт (матрица, «stream-json / json»).
	// Argv[0] — имя бинаря (конвенция адаптеров, как у kimi).
	argv := []string{binaryName}
	if spec.SessionID != "" {
		// Resume = новый процесс с id + промптом; ВСЕ флаги передаём заново,
		// включая --json-schema (per-run флаг — критично для verdict-протокола
		// ревьюера; матрица, «Resume с новым промптом»).
		argv = append(argv, "-r", spec.SessionID)
	}

	// Длинный промпт — не через argv (лимит ARG_MAX): короткий -p + полный
	// промпт в stdin, qwen их склеивает (паттерн Kent).
	var stdin string
	prompt := spec.Prompt
	if len(prompt) > promptStdinThreshold {
		stdin = prompt
		prompt = stdinPromptStub
	}
	argv = append(argv, "-p", prompt, "-o", "stream-json", "--approval-mode", "yolo")

	if spec.Model != "" {
		argv = append(argv, "-m", spec.Model)
	}
	if spec.SchemaForStructured != "" {
		argv = append(argv, "--json-schema", spec.SchemaForStructured)
	}
	// WorkDir в argv НЕ пробрасываем — supervisor задаёт cmd.Dir.

	// QWEN_CODE_UNATTENDED_RETRY=1 — auto-retry 429/529 на стороне CLI,
	// синергия с auto-resume supervisor'а: меньше ложных interrupted
	// (матрица, «Модель/effort»). ExtraEnv идёт следом и может переопределить.
	env := []string{"QWEN_CODE_UNATTENDED_RETRY=1"}
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
// фиксированного типа (матрица, следствие «а»). У qwen id есть в system/init
// и в result (Kent: `result.session_id or init.session_id`) — обход по
// порядку покрывает оба варианта. workDir не нужен (сессии qwen
// project-scoped, но id всегда приходит в стриме) — только для диагностики.
func (adapter) ExtractSessionID(events []harness.Event, workDir string) (string, error) {
	for _, ev := range events {
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("qwen: session id не найден ни в одном событии (workDir %q)", workDir)
}
