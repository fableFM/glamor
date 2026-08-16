# T-02 Схема SQLite + store-слой

Статус: done (2026-08-16) · M1 · зависимости: T-01

## Цель

Схема БД, миграции и типизированный store-слой — фундамент стейт-машины (D-10)
и журнала событий (D-11).

## Scope (in)

- Миграции — **по образу dashboard-manager** (проверено в
  `~/go/salt/dashboard-manager/migrations/`): `pressly/goose/v3`,
  **Go-миграции** (не .sql-файлы): `migrations/20260814000000_init.go`
  с `goose.AddMigrationContext(up, down)` в `init()`, raw SQL внутри
  Go-функций, ошибки через `fmt.Errorf("...: %w")`. Пакет `migrations`
  blank-import'ится в `cmd/glamord`, запуск `goose.Up` — при старте
  демона (в dashboard-manager это делает salt-app bootstrap; у нас
  демон сам, перед открытием listeners — старт «поверх мигрированной
  БД»). Диалект goose — `sqlite3` (для modernc драйвер `sqlite`).
- Таблицы:
  - `projects(id, path UNIQUE, name, default_branch, ide_command, created_at)`
  - `pipelines(id, project_id NULL /*NULL=глобальный*/, name, version,
    parent_version_id NULL, spec_json, created_at)` — версии неизменяемы,
    правка = новая версия с `parent_version_id`.
  - `runs(id, project_id, pipeline_version_id, task_text, base_branch,
    branch, state, depth, notify_tg, idempotency_key UNIQUE, created_at,
    finished_at NULL)`
  - `run_stages(id, run_id, stage_key, iteration, state, harness,
    session_id NULL, pid NULL, exit_code NULL, stop_requested_by NULL,
    resume_count, started_at NULL, finished_at NULL, tokens_in, tokens_out,
    error NULL)` — UNIQUE(run_id, stage_key, iteration).
  - `events(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id, stage_id NULL,
    ts, kind, payload_json)` — append-only; индекс (run_id, id).
  - `gates(id, run_id, stage_id NULL, kind, question, context_json, state,
    answer NULL, idempotency_key UNIQUE, created_at, resolved_at NULL)`
  - `artifacts(id, run_id, stage_id NULL, path, kind, created_at)`
  - `vendor_index` — FTS5 (content=vendor-файлы) — заготовка, наполнение
    в T-23.
- Подключение: `modernc.org/sqlite`, WAL, `busy_timeout`, один писатель
  (single connection для write pool или мьютекс — зафиксировать в коде).
- Store-слой — по D-80 (SALT-стиль): `internal/repository/<domain>`
  (projects, pipelines, runs, stages, gates, events, artifacts);
  `interfaces.go` с `RepositoryWithTX`, общий `query`-struct поверх
  `*sql.DB` и `*sql.Tx`, `OpenTx(ctx) (Tx, error)`; модели
  неэкспортируемы, наружу — `internal/dto/dtorep`. Динамические фильтры
  (список ранов по проекту/пайплайну/статусу, история событий с
  `after_id`) через `huandu/go-sqlbuilder` (flavor SQLite); статичные —
  голым SQL.
- Все переходы состояний — методы вида
  `TransitionStage(id, from, to, fields...) (bool, error)` с CAS в WHERE
  (D-10). Возврат `bool` = «переход применён» — основа идемпотентности.
- Хелпер `WithTx` для «переход + событие в одной транзакции» (D-11).

## Scope (out)

- Стейт-машина и её правила (T-03) — здесь только механика хранения.
- Журнальная рассылка по WS (T-04).

## Acceptance

- Миграции применяются на пустую БД и на БД предыдущей версии.
- Юнит-тесты: CAS-переход не применяется дважды; параллельные переходы не
  дают двух `running` стадий одного ключа; append события + переход атомарны
  (падение между ними невозможно — один tx).
- `PRAGMA integrity_check` чист после тестов.

## Итог (2026-08-16)

Статус: done (2026-08-16)

Сделано:
- `migrations/20260816000000_init.go` — goose v3 Go-миграция
  (`AddMigrationContext`, raw SQL, `%w`-обёртки): projects, pipelines
  (UNIQUE(project_id,name,version)), runs (CHECK state, UNIQUE
  idempotency_key), run_stages (CHECK state, UNIQUE(run_id,stage_key,
  iteration)), events (AUTOINCREMENT + idx (run_id,id)), gates (CHECK
  kind/state, UNIQUE idempotency_key), artifacts, `vendor_index` FTS5
  (заготовка, T-23). FK + индексы по задаче.
- `internal/repository`: Open (modernc sqlite, WAL, busy_timeout=5000,
  foreign_keys, **MaxOpenConns(1)** — «один писатель» зафиксирован в коде),
  Migrate (goose.SetDialect("sqlite3") + UpContext), IntegrityCheck,
  MapError (ErrNoRows→ErrNotFound, unique→ErrDuplicate), Conn-интерфейс
  поверх *sql.DB/*sql.Tx, TxManager.WithTx для кросс-доменных транзакций.
- Репозитории по D-80: projects, pipelines, runs, stages, gates, events,
  artifacts — у каждого interfaces.go (Queries/RepositoryWithTX/Tx),
  repository.go, query.go (общий query-struct), models.go (приватные),
  mappers.go. Наружу — только `internal/dto/dtorep`. Динамика (ListRuns,
  ReplayEvents) — go-sqlbuilder flavor SQLite; статика — голый SQL.
- CAS-методы: `TransitionRunState`, `TransitionStageState` (с
  StageTransitionFields), `ResolveGate` — все `UPDATE ... WHERE id=? AND
  state=?`, возврат bool «переход применён».
- Демон накатывает миграции при старте до listeners (blank-import
  migrations в cmd/glamord).

Отклонения/решения:
- Кросс-доменные транзакции (D-11 «переход+событие в одном tx») реализованы
  через `repository.TxManager.WithTx(ctx, func(*sql.Tx))` + `NewTx(sqlTx)`
  в каждом доменном пакете; OpenTx на репозитории тоже есть (D-80-форма).
  Это адаптация паттерна dashboard-manager на database/sql — разрешена D-80
  («та же форма поверх database/sql»).
- Время — колонки TIMESTAMP, значения time.Time (конверсия modernc-драйвера).
- id ранов/гейтов — TEXT uuid (pkg/uuid, crypto/rand, без внешних deps);
  в openapi.yaml run_id уже описан как string/uuid.

Проверка: `go test -race ./internal/...` зелёный — CAS не применяется дважды;
из 8 параллельных CAS ровно один успешен; UNIQUE не даёт двух стадий одного
ключа под конкурентностью; переход+событие откатываются вместе при ошибке
в tx; повторный idempotency_key → ErrDuplicate; повторный накат миграций —
no-op; `PRAGMA integrity_check` = ok в cleanup каждого теста; демон накатил
миграции на чистую БД (смоук, /healthz ok). `make lint` — 0 issues.
