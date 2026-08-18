# ADR-001: Стейт-машина рана/стадии/гейта

- Статус: accepted
- Дата: 2026-08-16
- Связанные решения: D-10, D-11, D-14, D-15, D-16, D-21 (tasks/00-overview.md)
- Реализация: `internal/usecase/runs/` (machine.go, transitions.go, action.go, spec.go)

## Контекст

Ядро glamor детерминировано: оркестрация пайплайна не должна зависеть от
состояния в памяти демона — демон может быть убит посреди рана (sleep,
крах окна UI, SIGKILL). Состояние обязано переживать рестарт (D-10),
а клиенты (UI, TG) — догонять пропущенное по журналу (D-11).

## Решение

### Состояние живёт только в SQLite

Стейт-машина — набор функций над БД (`runs`, `run_stages`, `gates`).
Памяти у движка нет: «что делать дальше» решается чтением БД
(`Machine.NextAction`), после любого события ядро перечитывает состояние
(event-driven tick). Переход — CAS-транзакция
(`UPDATE ... WHERE id = ? AND state = ?`) + событие журнала **в том же tx**
(D-10/D-11). Проигравший CAS получает `ErrConcurrentModification`,
переход вне таблицы — `ErrInvalidTransition` (ошибка программиста;
тесты обязаны это проверять).

### Run

```
draft → running → waiting_gate ⇄ running → succeeded | failed | stopped
```

- Переходы: `draft→running`, `running→waiting_gate`, `waiting_gate→running`,
  `running→succeeded|failed|stopped`, `waiting_gate→stopped`.
- `waiting_gate`: есть хотя бы один открытый гейт; ядро не двигает пайплайн.
  Резолв последнего открытого гейта атомарно возвращает ран в `running`
  (та же транзакция, что и резолв гейта).
- `stopped` — только явной командой пользователя (D-14); разрешён и из
  `waiting_gate` (остановка на гейте).
- reject гейта → ран `failed` (через `waiting_gate→running→failed`,
  два события — аудит каждого шага).
- Терминальные: `succeeded`, `failed`, `stopped` (выставляется `finished_at`).

### Stage

```
pending → running → succeeded | failed | interrupted
pending → skipped
```

- `interrupted` — НЕ терминал, но исходящего перехода у строки нет.
  **Попытка = новая строка**: auto-resume (D-14/16) создаёт новую строку
  `run_stages` с тем же `(run_id, stage_key)`, `iteration+1`,
  `resume_count+1`, состоянием `pending`. История попыток (pid, exit_code,
  ошибки) сохраняется для аудита; `UNIQUE(run_id, stage_key, iteration)`
  защищает от дублей.
- resume выполняется только от последней попытки цепочки; лимит
  `MaxResumeCount` (3) → исчерпание = эскалация-гейт (`ActionEscalate`).
- `skipped` заложен в enum для условных этапов (пост-M1).
- Startup recovery (D-15): все `running`-стадии при старте демона — сироты
  прошлого процесса (reattach не делаем) → `interrupted` → тот же контур
  auto-resume. Остановка пользователем — `stop_requested_by` в БД ДО
  убийства процесса → auto-resume не срабатывает (ран уходит в `stopped`).

### Gate

```
open → answered | approved | rejected | expired
```

- Виды: `plan_approval`, `question`, `escalation`, `final_review`.
- Инвариант: не более одного открытого гейта на `(run_id, stage_id)`
  (проверяется в транзакции `OpenGate`; property-тест контролирует).
- Идемпотентность (D-12): `idempotency_key` UNIQUE; повторный `OpenGate`
  с тем же ключом возвращает существующий гейт, повторный `ResolveGate`
  тем же резолюшном — no-op.

### Каждый переход порождает событие

`run.state_changed` / `stage.state_changed` / `gate.opened` / `gate.resolved`
— в журнал в той же транзакции, что и переход. Payload:
`{"from": "...", "to": "..."}` (для гейтов — объект гейта/резолюции).

## Последствия

- (+) Демон восстанавливается из БД после любого краха; UI/TG догоняют по
  `last_event_id`.
- (+) Идемпотентность бесплатна: CAS-false и уникальные ключи — штатные
  пути, а не исключения.
- (−) Каждый переход — транзакция; для локального демона с одним писателем
  (MaxOpenConns(1)) это не bottleneck.
- (−) Два события на reject-из-waiting_gate (running, затем failed) —
  осознанная плата за аудит.

## Альтернативы

- **In-memory стейт + снапшоты.** Отклонено: гонки при восстановлении,
  двойная запись, рассинхрон память/БД. D-10 прямо требует «стейт-машина
  только в SQLite».
- **Очередь задач (job queue) вместо event-tick.** Отклонено: дублирует
  журнал событий, добавляет второй источник истины о «что делать дальше»;
  NextAction чистой функцией над БД проще тестировать (property-тесты) и
  восстанавливать после краха.
- **Resume = та же строка с resume_count+1.** Отклонено: теряется аудит
  попыток (pid/exit_code предыдущих запусков), ломает
  `UNIQUE(run_id, stage_key, iteration)` как защиту от двойного запуска.
- **Терминальные переходы рана из waiting_gate напрямую.** Отклонено (кроме
  `stopped`): диаграмма T-03 разрешает терминалы только из `running`;
  проход через `running` даёт явный след в журнале.

## Дополнение (2026-08-16, T-05)

- Добавлен переход `failed → running` — **только ручной resume**
  (`POST /runs/{id}/resume`): failed формально терминал для движка
  (NextAction никогда не выбирает его сам), но пользователь может вернуть
  ран в работу; дальше применяется обычный контур (resume interrupted /
  fix-петля). `succeeded` и `stopped` остаются строго терминальными.
- Queue note / steer хранятся в таблице `notes` (миграция
  20260816120000): steer-заметка создаётся при Interrupt&Steer в одной
  транзакции с переводом стадии в `interrupted` + `stop_requested_by=user`.

## Дополнение (2026-08-16, T-10): git-политика безопасности

- Пайплайн НИКОГДА не выполняет `git commit`, `git push` и destructive-git
  (D-32). Это конвенция промптов (T-17) + deny-list в документации;
  технически не форсится (harness работает в режиме auto-approve, ядро не
  контролирует его команды) — принимаем осознанно: ран работает в основном
  чекауте (D-30), результат = незакоммиченные изменения, пользователь
  сам ревьюит diff и коммитит.
- Защита от потери работы: preflight (чистый чекаут / force, D-34) и
  branch_mismatch-check перед каждым этапом (пользователь переключил
  ветку руками → этап не стартует, событие run.branch_mismatch).

## Дополнение (2026-08-16, fix-task-0 F-03): перенос реализации

- Реализация стейт-машины перенесена из `internal/usecase/runs/` в
  `internal/service/runsmachine/` (machine.go, transitions.go, action.go,
  spec.go): CAS-переходы, гейты, recovery — доменная бизнес-логика,
  service-слой по D-80. Поведение и таблицы переходов не изменились.
- В `internal/usecase/runs/` остались API-сценарии (CreateRun, StopRun,
  ResumeRun, ResolveGateAPI, InterruptStageSteer, CreateNote) — тип
  `Usecase` над `*runsmachine.Machine`; supervisor перенесён в
  `internal/usecase/supervisor/` (оркестратор тика, usecase → service).
- *(2026-08-16, fix-task-1):* слой usecase упразднён — API-сценарии
  переехали в `internal/service/runsapi`, supervisor вернулся в
  `internal/service/supervisor`, read-фасад — `internal/service/catalog`.

## Дополнение (2026-08-17, fix-task-4 F-01): событие создания рана

- Добавлено событие журнала `run.created`: `runsapi.CreateRun` пишет его
  в ТОЙ ЖЕ транзакции, что и INSERT рана (D-11 распространён на создание,
  не только на переходы). Payload — сериализованный объект рана целиком
  (`RunCreatedPayload` в api/openapi.yaml: id, project_id,
  pipeline_version_id, task_text, base_branch, branch, state, depth,
  notify_tg, created_at) — клиент строит карточку рана без REST-догона.
- Идемпотентность (D-12): повтор по Idempotency-Key возвращает первый ран
  и НЕ порождает второго события `run.created`.
- Создание рана событием не является переходом стейт-машины: ран
  появляется сразу в `draft`, таблица переходов ADR-001 не менялась.

## Дополнение (2026-08-17)

- `draft → stopped` разрешён: отмена рана, ещё не стартовавшего (очередь).
  Найдено при реализации удаления проекта: stop на draft-ране возвращал
  409 invalid_transition.
