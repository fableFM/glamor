# T-25 Адаптеры claude-code и codex

Статус: todo · M3 · зависимости: T-06, research/harness-capability-matrix.md

## Цель

Расширение зоопарка (D-42): claude-code (установлен локально) и codex
(не установлен — только доки, e2e отложен до установки).

## claude-code (проверено локально, `claude --help`)

- Headless: `claude -p "<prompt>" --output-format stream-json
  [--include-partial-messages]` — partial-messages для живого стрима
  thinking.
- Resume: `-r/--resume <id>` (+ `--fork-session` при необходимости
  разветвления — не используем по умолчанию).
- Effort: `--effort low|medium|high|max` (нативный флаг!).
- Модель: `--model`; бюджет: `--max-budget-usd` (полезно для лимитов).
- Auto-approve: `--permission-mode` (acceptEdits/bypassPermissions/auto)
  — выбрать профиль для этапов: вероятно `bypassPermissions` для
  coder-этапов, фиксируется в конфиге пайплайна.
- Structured output: `--json-schema` — идеально для verdict ревьюера.
- Особенность: `--input-format stream-json` даёт потоковый ввод —
  потенциальный путь живого вмешательства без прерывания (кандидат на
  M4-эксперимент, отметить в коде).

## codex (не установлен — по докам)

- Выяснить из ресёрча: headless-режим (`codex exec`?), формат JSON-событий,
  resume-сессии, флаги модели/approval. Всё неподтверждённое — UNVERIFIED,
  адаптер за feature-flag'ом.

## Scope (на каждый адаптер)

- Как T-07: BuildCommand/ParseStream/ExtractSessionID/retriable +
  золотые файлы testdata + e2e под build tag (codex — только после
  локальной установки).

## Acceptance

- Пайплайн M1 проходит на claude-code целиком (все роли).
- Capability matrix обновлена фактами из реализации.

## Дополнение по ресёрчу (2026-08-14)

- claude: stream-json ТРЕБУЕТ `--verbose`; `--bare` для CI-режима;
  `api_retry`-события — процесс может жить минутами в retry-лупе →
  stall-watchdog T-09 должен считать `error.retry` «признаком жизни»,
  но иметь верхний лимит на retry-лупу (конфиг, дефолт 10 мин).
- claude: `--effort` нативный; `--json-schema` для verdict; при resume —
  UNVERIFIED, проверить необходимость повторной передачи схемы.
- codex: resume = `codex exec resume <id> "<новое>"`; **все флаги
  (model/sandbox/json) передавать заново на каждый resume** (доки).
  Нет text-delta (agent_message целиком) → живой стрим ограничен
  reasoning/tool событиями. Sandbox-профиль для кодера: `workspace-write`.
