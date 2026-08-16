# T-17 Дефолтный пайплайн M1 (порт ролей ai-pipeline)

Статус: todo · M1 · зависимости: T-03, T-07, T-11

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
