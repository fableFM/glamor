# T-03 Доменная стейт-машина + ADR-001

Статус: done (2026-08-16) · M1 · зависимости: T-02

## Цель

Формальная модель состояний рана/стадии/гейта, задокументированная в ADR-001
и реализованная в `internal/core` без привязки к HTTP/WS.

## Состояния

### Run

`draft → running → waiting_gate ⇄ running → succeeded | failed | stopped`

- `waiting_gate`: есть хотя бы один открытый гейт; ядро не двигает пайплайн,
  пока гейт не резолвнут.
- `stopped`: только через явную команду пользователя (D-14).
- Терминальные: `succeeded`, `failed`, `stopped`.

### Stage

`pending → running → succeeded | failed | interrupted | skipped`

- `interrupted` — НЕ терминал: из него auto-resume (D-14/16) переводит в
  новую попытку `running` (тот же stage_key, `iteration` и `resume_count++`
  — зафиксировать семантику: попытка = тот же run_stage с resume_count+1 или
  новая строка; предпочтительно новая строка с `iteration` для аудита —
  решить и записать в ADR).
- `skipped` — для условных этапов (пост-M1, заложить в enum сразу).

### Gate

`open → answered | approved | rejected | expired`

- Виды: `plan_approval`, `question` (вопросы планировщика), `escalation`
  (петля исчерпана / auto-resume исчерпан), `final_review`.
- Резолв гейта атомарно переводит ран из `waiting_gate` обратно (если других
  открытых нет) — в одной транзакции с событием.

## Правила движка

- Движок — чистая функция над состоянием БД: «что делать дальше» решается
  чтением `runs/run_stages/gates`, а не состоянием в памяти. После любого
  события ядро перечитывает состояние (event-driven tick).
- Петля code→review→fix: `max_iters` с loop-edge (D-62); при превышении —
  эскалация-гейт с нерешёнными findings; ответ пользователя → вход fix'а.
- Переходы только через store CAS-методы (T-02); неизвестный переход =
  ошибка программиста (panic в тестах, error в проде).
- Каждый переход порождает событие `run.state_changed` /
  `stage.state_changed` / `gate.opened|resolved` в журнал.

## Делiverables

- `docs/adr/001-state-machine.md`: диаграмма состояний, таблица переходов,
  семантика interrupted/resume, причины отказа от альтернатив (in-memory
  state, очередь задач вместо event-tick).
- `internal/usecase/runs/machine.go` (+ тесты на каждый разрешённый и
  запрещённый переход, включая гонки через параллельные горутины) —
  размещение по D-80 (движок — уровень usecase).

## Acceptance

- ADR-001 написан и совпадает с кодом.
- Тест «убить демон посреди рана» (симуляция): после рестарта движок
  корректно видит `interrupted` и предлагает resume.
- Property-тест: случайные последовательности команд не приводят к
  недопустимым состояниям (например двум открытым гейтам одного этапа).

## Итог (2026-08-16)

Сделано:
- `docs/adr/001-state-machine.md` — диаграммы, таблицы переходов, семантика
  interrupted/resume, причины отказа от альтернатив (in-memory, job queue,
  resume той же строкой). ADR совпадает с кодом.
- `internal/usecase/runs/` (уровень usecase по D-80):
  - `transitions.go` — таблицы разрешённых переходов run/stage/gate;
    переход вне таблицы → `ErrInvalidTransition`.
  - `machine.go` — TransitionRun/TransitionStage (валидация + CAS + событие
    в одном tx), StartStage, ResumeStage, RecoverInterrupted (D-15),
    OpenGate/ResolveGate (атомарно с waiting_gate⇄running и событиями).
  - `action.go` — NextAction: чистая функция над БД (start_run/start_stage/
    wait_stage/resume_stage/escalate/wait_gate/finish_run/none).
  - `spec.go` — разбор pipelines.spec_json (линейные этапы; loop-рёбра —
    supervisor T-09, зафиксировано в ADR).
  - `EventAppender` — интерфейс записи событий в tx; в T-04 подменяется
    на events.Journal с пост-коммитной рассылкой.
- Ключевые семантики (зафиксированы в ADR): попытка = новая строка
  (iteration+1, resume_count+1); resume только от последней попытки;
  MaxResumeCount=3 → ActionEscalate; один открытый гейт на (run,stage);
  reject гейта → run failed (waiting_gate→running→failed, два события);
  waiting_gate→stopped разрешён (остановка на гейте); повторный
  OpenGate/ResolveGate идемпотентен.

Отклонения от таски: нет по существу; «неизвестный переход = panic в
тестах» реализовано как обязательная проверка ErrInvalidTransition
в тестах (error в проде — как и требует таска).

Проверка (`go test -race ./internal/usecase/...`, зелёно):
- каждый разрешённый и запрещённый переход run/stage/gate;
- «убийство демона»: running-сирота → RecoverInterrupted → interrupted →
  NextAction=resume_stage → ResumeStage (новая строка, iteration=2,
  resume_count=1); лимит resume → ActionEscalate;
- гонки: 8 параллельных TransitionStage — одно событие running, состояние
  корректно; лишние получают ErrConcurrentModification или идемпотентный nil;
- property-тест (500 случайных команд, seed=42): инварианты после каждого
  шага — состояния из enum, ≤1 running-стадия на ключ, ≤1 открытый гейт на
  (run,stage), waiting_gate ⇒ есть открытые гейты;
- события журнала на каждый переход.
