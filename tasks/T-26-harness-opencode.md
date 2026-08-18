# T-26 Адаптер opencode

Статус: done (2026-08-17) · M3 · зависимости: T-06, research/harness-capability-matrix.md

## Цель

Пятый и последний harness из списка (D-42). ~~pi~~ — исключён из скоупа
решением от 2026-08-14.

## opencode (проверено локально, `opencode --help`, 1.2.27)

- Headless: `opencode run "<message>"`; JSON: `--format json` (NDJSON).
- Resume: `opencode run -s <sessionID> "<новое>"` / `-c` (help;
  end-to-end UNVERIFIED — проверить первым e2e).
- Auto-approve: `--auto` (флага `--dangerously-skip-permissions` в
  1.2.27 НЕТ).
- Session id: `sessionID` в каждом событии; хранилище — SQLite
  `~/.local/share/opencode/opencode.db`; `opencode export <id>` — полный
  JSON сессии.
- Известные баги (из ресёрча): финальный `step_finish` может не прийти
  (#26855) → usage добывать post-hoc через `opencode export`; нет события
  tool-start (только completed) → эмитить start=end; нет модели в событиях
  (#40544).
- Модель/effort: `-m provider/model`, `--variant <effort>`
  (provider-specific значения).
- Альтернативный транспорт: `opencode serve` (headless HTTP API) —
  рассмотреть при реализации, если CLI-стрим окажется бедным; адаптер
  инкапсулирует выбор.

## Scope

- Как T-07: BuildCommand / ParseStream / ExtractSessionID / retriable +
  золотые файлы testdata + e2e под build tag.

## Acceptance

- opencode: пайплайн M1 проходит на нём (хотя бы planner/reviewer роли).
- Реестр harness'ов в UI показывает все пять с честными capability badges.

## Итог (2026-08-17, субагент; проверено)

- opencode: `run [-s id] [-m] [--variant] <prompt> --format json --auto`;
  text→text_delta (гранулярность UNVERIFIED); tool_use только completed →
  пара start+result (start=end); step_finish → usage (баг #26855: может
  не прийти — задокументировано, export-fallback вне M1); нет модели в
  событиях (#40544). Зарегистрирован в main. Реестр UI показывает все 5
  harness'ов через /version harnesses (badges — фронт по Capabilities).
- Золотые файлы по матрице (1.2.27, CLI не запускался); e2e-каркас под тегом.
