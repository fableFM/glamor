# T-10 Git-интеграция (без worktree, без коммитов)

Статус: done (2026-08-16) · M1 · зависимости: T-02

## Цель

Git-контур по D-30..34: ран работает в основном чекауте, пайплайн не
коммитит и не пушит.

## Scope

- `internal/gitx` (через `git` CLI, не go-git — нужна полная совместимость
  с конфигами/hooks пользователя; команды с таймаутами и `-C <path>`):
  - `Preflight(projectPath, baseBranch, branch)`:
    - репозиторий валиден, base_branch существует;
    - рабочее дерево чистое (`git status --porcelain`) — иначе ошибка
      `dirty_checkout` со списком файлов (API позволяет override-флагом
      `force: true`, зафиксировать в спеке);
    - ветка не занята активным раном — lock в БД `(project_id, branch)`
      (UNIQUE-индекс на частично-активные раны или проверка в транзакции
      создания рана — выбрать, зафиксировать);
    - если branch не указана: сгенерировать `glamor/<slug-из-задачи>`,
      при коллизии суффикс `-2`, `-3`.
  - `PrepareBranch`: `git checkout -b <branch> <base>` (или `git checkout
    <branch>`, если ветка указана пользователем и существует — тогда
    предупреждение в гейт: «ветка не пустая?» — нет, просто работаем).
  - `DiffStat(projectPath, baseBranch)` — для UI и summary: файлы,
    +/- строки (включая untracked: `git status` + `git diff --stat`).
  - `CurrentBranch` — sanity-check перед каждым этапом: пользователь мог
    переключить ветку руками → этап не стартует, событие
    `run.branch_mismatch` + гейт (это защита от потери работы).
- Политика безопасности (из BRAINSTORM): пайплайн никогда не выполняет
  `git push`, `git commit`, destructive-git. Это конвенция промптов (T-17)
  + deny-list в документации; технически не форсим (harness всё равно
  yolo) — честно описать в ADR-001.

## Acceptance

- Тесты на tmp-репозиториях: preflight на чистом/грязном чекауте,
  генерация slug, коллизия имён веток, branch_mismatch детектится.
- Два рана на одном проекте с разными ветками — второй стартует; с той же
  веткой — 409 `run_locked`.

## Итог (2026-08-16)

Сделано: `backend/internal/gitx/` (git CLI с таймаутом 30s, `-C <path>`):
- Preflight: валидный репо, base существует, чистый чекаут →
  DirtyCheckoutError со списком файлов (API: 400 dirty_checkout +
  details.files; override force:true — уже в спеке T-05). Lock ветки —
  частичный UNIQUE-индекс БД (T-05), не здесь.
- SuggestBranch: glamor/<slug> с суффиксами -2/-3 при коллизии в git
  (подключён как Machine.SetBranchNamer).
- PrepareBranch: checkout существующей или checkout -b от base
  (PostRunHook supervisor'а).
- CheckBranchMismatch перед каждым этапом (PreStageHook): событие
  run.branch_mismatch + этап не стартует.
- GetDiffStat: numstat + untracked для UI/summary.
- Политика «никогда не commit/push/destructive» зафиксирована в ADR-001
  (доп. 2026-08-16) — конвенция, технически не форсится.

Проверка: тесты на tmp-репозиториях — preflight чистый/грязный/force/
нет base/не репо, коллизии имён (-2/-3), prepare+current+mismatch,
diff stat (numstat + untracked). Всё зелёное с -race, lint 0 issues.
