# T-17 Дефолтный пайплайн M1 (порт ролей ai-pipeline)

Статус: done (2026-08-17) · M1 · зависимости: T-03, T-07, T-11

## Цель

Пайплайн plan→code→review→fix как данные (записи в `pipelines`), с
промптами-ролями, портированными из `~/go/ai-pipeline/roles/`.

## Scope

- **Seed при миграции/первом старте**: глобальный пайплайн «default»
  версии 1: этапы planner → coder → reviewer → (loop: fixer → reviewer,
  max_iters=4) → final gate.
- **Шаблонизация промптов**: плейсхолдеры `{{task}}`, `{{base_branch}}`,
  `{{artifact.spec}}`, `{{artifact.questions}}`, `{{vendor_memory_paths}}`,
  `{{depth}}`, `{{queue_notes}}`, `{{verdict}}` (для fixer). Движок
  рендерит перед запуском этапа; отсутствующий плейсхолдер — ошибка на
  старте рана (fail fast, а не молча пустой промпт).
- **Порт ролей** из ai-pipeline (`planner.md`, `coder.md`, `reviewer.md`,
  `fixer.md`, `depth-*.md`) — с правками:
  - «терминал Orca» → артефакт-контракт (questions.md / spec.md /
    verdict.json / handoff.md);
  - явная инструкция кодеру/фиксеру: НЕ коммитить, НЕ пушить (D-32);
  - verdict.json — strict JSON (машиночитаемый ревьюер, как раньше).
- **Depth-пресеты** (quick/standard/deep) — подмешиваемый кусок промпта
  планировщика, как в ai-pipeline.
- **Триплеты по умолчанию** (из config.example.sh ai-pipeline):
  planner/coder/reviewer — модель пользователя, effort high; fixer — low.
  Хранятся в spec_json пайплайна, переопределяются в конфиге демона.
- **Vendor-память**: пути `~/.glamor/vendors/` + `<repo>/.glamor/vendors/`
  подставляются в промпт планировщика (чтение); запись-дельты — в M3 (T-23),
  в M1 планировщик только читает.

## Acceptance

- Ран на тестовом репозитории проходит весь пайплайн: spec → approve →
  код → verdict → финальный гейт.
- Промпт каждого этапа сохранён в артефактах рана (`prompt-<stage>.md`) —
  как в ai-pipeline, для отладки.
- Depth меняет поведение планировщика (проверяется по содержимому spec).

## Итог (2026-08-17)

Сделано:
- `internal/service/pipeline/` — дефолтный пайплайн как данные:
  planner → coder → reviewer → (loop: fixer → reviewer, max_iters=4) →
  final_review. `DefaultSpec()` собирает spec_json с inline
  prompt_template; `Seed()` вставляет глобальный «default» v1
  идемпотентно при старте.
- Промпты портированы из ai-pipeline/roles (prompts/*.md, embed):
  terminal/worker_done → артефакт-контракт (questions.md/spec.md/
  verdict.json/handoff.md в {run_dir}=~/.glamor/runs/<id>), явный запрет
  commit/push (D-32), verdict.json — strict JSON; depth-пресеты
  quick/standard/deep — подмешиваемый {{depth_instructions}}.
- Шаблонизация: {{task}}, {{base_branch}}, {{branch}}, {{run_dir}},
  {{artifact.<имя-файла>}} ({{artifact.spec.md}} и т.п.),
  {{vendor_memory_paths}}, {{depth}}, {{depth_instructions}},
  {{queue_notes}}, {{verdict}}, {{iteration}}, {{max_iterations}}.
  Неизвестный плейсхолдер — ошибка рендера (fail fast).
- Spec: +loop{from,to,max_iters}, +final_gate, +prompt_template;
  плейсхолдеры путей {run_id}/{run_dir} (артефакты ВНЕ чекаута).
- Движок: Machine.ReenterStageByKey (harness первой попытки из спеки —
  найденный баг), SkipStage (условный этап), EnsureFinalGate (comment →
  ре-вход последней стадии через T-11), supervisor: финальный гейт
  перед succeeded, сохранение prompt-<stage>-<iter>.md как артефакт,
  регистрация stage_output-артефактов; pending→failed в таблице
  (spawn failure).
- LoopHook (OnStageSucceeded): reviewer → verdict.json: approved →
  SkipStage(fixer); changes_required → ReenterStageByKey(fixer) (<
  max_iters) или эскалация-гейт с findings (гейт на fixer → ответ
  резюмит fixer); fixer → ReenterStageByKey(reviewer).
- Триплеты: всё kimi, model kimi-code/k3, effort high (fixer low) — из
  ai-pipeline config.example.sh; Overrides в коде, из конфига демона —
  TODO при первой необходимости (сейчас правка = новая версия пайплайна).

Проверка:
- e2e полного пайплайна на fake-harness: spec → plan_approval(approve) →
  код → verdict changes_required → fix (prompt содержит verdict) →
  approved → final_review(approve) → succeeded; попытки и resume_count
  корректны; промпты сохранены артефактами и отрендерены (без {{}}).
- эскалация петли: max_iters=2 → escalation-гейт с findings → ответ →
  fixer iteration 3.
- рендер: все плейсхолдеры, depth-пресеты, unknown → ошибка.
- seed идемпотентен; полный прогон make test/lint зелёные.
