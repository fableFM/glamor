# T-08 Адаптер qwen

Статус: done (2026-08-16) · M1 · зависимости: T-06

## Цель

Второй адаптер — проверка, что интерфейс T-06 не заточен под kimi.

## Факты (проверено локально, `qwen --help`)

- Headless: `qwen -p "<prompt>" -o stream-json`.
- Resume: `-r/--resume <id>`; continue: `-c`; есть `qwen sessions` —
  управление сессиями (может помочь с ExtractSessionID).
- Модель: `-m`; fallback: `--fallback-model` (до 3 — полезно для
  устойчивости, маппить из конфига).
- Auto-approve: в ai-pipeline использовался `--approval-mode yolo` —
  проверить наличие флага в текущей версии (в --help не показан — может
  быть deprecated/переименован; см. ресёрч, UNVERIFIED).
- Effort: вероятно НЕ поддержан → Capabilities.EffortLevels = nil.

## Scope

- Как T-07: BuildCommand / ParseStream / ExtractSessionID / retriable.
- Расхождения с kimi задокументировать в коде адаптера (комментарии со
  ссылками на ресёрч).

## Acceptance

- Те же критерии, что T-07 (золотые файлы + e2e под build tag).
- Ядро запускает один и тот же пайплайн на обоих harness'ах без ветвлений
  по имени harness'а.

## Дополнение по ресёрчу (2026-08-14)

- `--approval-mode yolo` подтверждён (headless.md бандла). Промпт: короткий
  `-p` + полный в stdin (склеиваются — паттерн Kent).
- Effort: CLI-флага НЕТ, только `model.reasoningEffort` в settings.json →
  адаптер пишет временный settings-override (`QWEN_CODE_SYSTEM_SETTINGS_PATH`)
  или Capabilities.EffortLevels=nil (выбрать; Kent пинит xhigh через settings).
- Exit codes как источник истины: 53 (max turns), 55 (budget),
  130 (SIGINT); при них result-события может не быть.
- `-o json` ≠ `-o stream-json` (буферный массив vs NDJSON) — используем
  только stream-json.
- `--json-schema` передавать ЗАНОВО на каждый resume (per-run флаг) —
  критично для verdict-протокола ревьюера.
- Полезные env: `QWEN_CODE_UNATTENDED_RETRY=1` (auto-retry 429/529 —
  синергия с нашим auto-resume: меньше ложных interrupted).

## Итог (2026-08-16)

Сделано (субагентом, проверено): `backend/internal/harness/qwen/` —
BuildCommand (`qwen -p <prompt> -o stream-json --approval-mode yolo`,
`-m`, resume `-r <id>` со ВСЕМИ флагами заново, `--json-schema` на каждый
запуск (per-run флаг), длинный промпт → короткий -p + stdin (склейка,
паттерн Kent), env QWEN_CODE_UNATTENDED_RETRY=1), Capabilities
(EffortLevels=nil — CLI-флага нет; UsageSource=stream;
StructuredJSON=true), ParseStream (system/init → session.init,
assistant content text/tool_use, result → Result+Usage+structured_result,
is_error → error), ExtractSessionID (result→init fallback), retriable
(429/529/сетевые). Золотые файлы по матрице (qwen 0.21.3).
Проверка: юнит-тесты зелёные с -race, lint 0 issues. Ядро (supervisor)
работает с обоими адаптерами без ветвлений по имени.
