// Package kimi — адаптер harness'а Kimi Code CLI (проверено на 0.36.0).
// Факты о CLI — tasks/research/harness-capability-matrix.md, секция
// «## 1. kimi»; реальный CLI в юнит-тестах не запускается (это стоит
// API-вызовы), golden-файлы синтезированы по матрице.
package kimi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fableFM/glamor/internal/harness"
)

// maxArgvPromptLen — порог длины промпта для передачи через argv
// (лимит ARG_MAX). stdin в `-p` режиме kimi UNVERIFIED (матрица, риск 1),
// поэтому длинный промпт пишем во временный файл, а в argv кладём короткую
// инструкцию «прочитай файл» — выбранный вариант из дополнения T-07.
const maxArgvPromptLen = 100 * 1024

// errNoSessionID — в потоке не нашлось ни одного события с session_id.
var errNoSessionID = errors.New("session id отсутствует в потоке")

type adapter struct{}

// New возвращает адаптер kimi.
func New() harness.Harness {
	return &adapter{}
}

func (a *adapter) Name() string       { return "kimi" }
func (a *adapter) BinaryName() string { return "kimi" }

func (a *adapter) Capabilities() harness.Capabilities {
	return harness.Capabilities{
		StreamJSON:     true,
		Resume:         true, // `kimi -S <id> -p "…"` — подтверждено Kent (матрица)
		EffortLevels:   []string{"low", "high", "max"},
		CostReporting:  false, // usage в stdout отсутствует
		StructuredJSON: false, // нет --json-schema; verdict — промпт-контракт (T-17)
		ACP:            true,  // `kimi acp` — запас на M4
		Thinking:       false, // thinking идёт в stderr, НЕ в stdout-стрим
		UsageSource:    harness.UsageSourceWirefile,
	}
}

// BuildCommand собирает `kimi -p <prompt> --output-format stream-json`.
//
// Правила (матрица, секция kimi):
//   - --yolo/--auto НИКОГДА не добавляются: несовместимы с -p (в headless
//     пермишен-политика всегда auto сама по себе);
//   - resume: `-S <id>` при spec.SessionID != "";
//   - модель: `-m <alias>` при spec.Model != "";
//   - effort: env KIMI_MODEL_THINKING_EFFORT=<effort> при spec.Effort != "";
//   - spec.ArtifactContract в промпт НЕ подмешивается — промпт собирает
//     supervisor, BuildCommand только транспорт;
//   - WorkDir в argv не попадает — supervisor задаёт cmd.Dir.
//
// Argv[0] — имя бинаря; вызывающий заменяет его на резолвнутый путь
// (Registry.BinaryPath). Env — дополнительные переменные к окружению
// процесса (не полное окружение).
func (a *adapter) BuildCommand(spec harness.LaunchSpec) (harness.CommandSpec, error) {
	argv := []string{a.BinaryName()}
	if spec.SessionID != "" {
		argv = append(argv, "-S", spec.SessionID)
	}
	if spec.Model != "" {
		argv = append(argv, "-m", spec.Model)
	}

	prompt := spec.Prompt
	if len(prompt) > maxArgvPromptLen {
		path, err := writePromptFile(prompt)
		if err != nil {
			return harness.CommandSpec{}, err
		}
		prompt = "Прочитай файл " + path + " и выполни инструкции из него."
	}
	argv = append(argv, "-p", prompt, "--output-format", "stream-json")

	extra := make([]string, 0, len(spec.ExtraEnv))
	for k, v := range spec.ExtraEnv {
		extra = append(extra, k+"="+v)
	}
	sort.Strings(extra) // детерминированность для тестов и логов

	env := make([]string, 0, len(extra)+1)
	if spec.Effort != "" {
		env = append(env, "KIMI_MODEL_THINKING_EFFORT="+spec.Effort)
	}
	env = append(env, extra...)

	return harness.CommandSpec{Argv: argv, Env: env}, nil
}

// writePromptFile пишет длинный промпт во временный файл. Файл адаптером
// НЕ удаляется — он нужен процессу до конца рана; очистку директории
// (filepath.Dir(path)) выполняет supervisor (T-09) после завершения
// процесса, путь виден в промпте-инструкции.
func writePromptFile(prompt string) (string, error) {
	dir, err := os.MkdirTemp("", "glamor-kimi-prompt-")
	if err != nil {
		return "", fmt.Errorf("kimi: создать temp-директорию для промпта: %w", err)
	}
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		return "", fmt.Errorf("kimi: записать промпт во временный файл: %w", err)
	}
	return path, nil
}

// ExtractSessionID — первое событие с SessionID != "" (обычно meta
// session.resume_hint; fallback — любое событие с session_id в потоке,
// ParseStream проставляет его всегда, когда поле есть в сообщении).
// workDir не используется: хранилище ~/.kimi-code — запасной путь за
// пределами скоупа адаптера.
func (a *adapter) ExtractSessionID(events []harness.Event, _ string) (string, error) {
	for _, ev := range events {
		if ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("kimi: извлечь session id: %w", errNoSessionID)
}
