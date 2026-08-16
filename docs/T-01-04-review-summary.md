# Итог работы: T-01…T-04 (для независимого review)

Дата: 2026-08-16 · Исполнитель: Kimi Code · Репозиторий: `github.com/fableFM/glamor`
Таски: `tasks/T-01..T-04-*.md` (все в статусе `done`, секции «Итог» в каждой).
Закон: `tasks/00-overview.md` (D-NN), `AGENTS.md`, ADR-001 (`docs/adr/001-state-machine.md`).

## Что сделано

### T-01 — Bootstrap монорепо
- Layout по D-80: `cmd/glamord` (main + config.go), `cmd/glamor` (заглушка),
  `internal/{controller/{http,ws},usecase,service,repository,harness,events,
  gitx,notify,dto/{dtoctrl,dtosvc,dtousecase,dtorep},cstmerrors}`, `api/`,
  `web/` (Vite+React19+TS), `src-tauri/` (пустышка), `docs/adr/`,
  `migrations/`, `pkg/`.
- Конфиг: defaults < `~/.glamor/config.yaml` < env `GLAMOR_*`, validate-теги
  (go-playground/validator). Демон: slog, `/healthz`, порт 0 = динамический,
  graceful shutdown.
- `make gen/build/test/lint/dev/web-build/ci`; `.golangci.yml` (v2-формат);
  CI `.github/workflows/ci.yml` (go, web, gen-idempotent).
- **Отклонение, зафиксированное в D-02 (с датой): go-swagger → oapi-codegen**
  (go-swagger поддерживает только Swagger 2.0, спека — OpenAPI 3.0.3).
  TS-типы — openapi-typescript. Сгенерённое коммитится, gen идемпотентен.
- golangci-lint v2.12.2 (v1.64.8 не работает с Go 1.25).

### T-02 — Схема SQLite + store-слой
- `migrations/20260816000000_init.go`: goose v3 Go-миграция
  (`AddMigrationContext`): projects, pipelines, runs, run_stages, events,
  gates, artifacts, `vendor_index` FTS5 (заготовка, T-23). CHECK-констрейнты
  состояний, UNIQUE(idempotency_key), UNIQUE(run_id,stage_key,iteration),
  FK, индексы. Накат при старте демона (blank-import в main, dialect sqlite3).
- `internal/repository`: Open (modernc.org/sqlite, WAL, busy_timeout=5000,
  foreign_keys, **MaxOpenConns(1)** — «один писатель» в коде), Migrate,
  MapError (ErrNoRows→ErrNotFound, unique→ErrDuplicate), Conn-интерфейс
  поверх `*sql.DB`/`*sql.Tx`, `TxManager.WithTx(ctx, func(ctx, tx) error)`
  для кросс-доменных транзакций.
- 7 доменных репозиториев по паттерну dashboard-manager (interfaces.go с
  Queries/RepositoryWithTX/Tx, query-struct, приватные models, mappers;
  наружу только `internal/dto/dtorep`). Динамика (ListRuns, ReplayEvents) —
  go-sqlbuilder (flavor SQLite); статика — голый SQL.
- CAS: `TransitionRunState`, `TransitionStageState` (+StageTransitionFields),
  `ResolveGate` — `UPDATE ... WHERE id=? AND state=?` → bool.

### T-03 — Стейт-машина + ADR-001
- `internal/usecase/runs/`: transitions.go (таблицы переходов), machine.go
  (TransitionRun/Stage, StartStage, ResumeStage, RecoverInterrupted,
  OpenGate/ResolveGate — всё CAS+событие в одном tx), action.go (NextAction —
  чистая функция над БД), spec.go (разбор spec_json).
- Ключевые семантики (ADR-001): попытка = новая строка (iteration+1,
  resume_count+1); resume только от последней попытки; MaxResumeCount=3 →
  ActionEscalate; один открытый гейт на (run,stage); reject гейта → run
  failed (через waiting_gate→running→failed); waiting_gate→stopped разрешён;
  idempotency повторных OpenGate/ResolveGate.
- Тесты: все разрешённые/запрещённые переходы; kill-daemon → recovery →
  resume; параллельные CAS (8 goroutines); property-тест (500 случайных
  команд, инварианты после каждого шага).

### T-04 — Event journal + WS hub
- `internal/events`: Journal (EventAppender + TxExecutor; публикация в Hub
  ПОСЛЕ коммита через collector в ctx), Hub (pub/sub по run_id, буфер 256,
  медленный подписчик → drop), Replay, StreamBatcher (200мс/64КБ).
- `internal/controller/ws`: `GET /ws?run_id=&last_event_id=&token=`
  (coder/websocket): replay → `synced {last_event_id}` → live; ping 30s;
  drop → close 1001 «resync required»; token auth (D-08, пустой = off).
- `Machine.SetTxExecutor(journal)` — переходы машины рассылаются в Hub.
- openapi.yaml: /ws, payload-схемы событий, SyncedMessage; gen обновлён.

## Архитектурная карта (что куда смотрит)

```
cmd/glamord ── repository.Open/Migrate ── migrations (blank import)
           ── events.NewHub/NewJournal ── controller/ws.Handler (/ws)
internal/usecase/runs.Machine ── repository.{TxManager|runs|stages|gates|pipelines}
                             ── EventAppender (repoAppender | events.Journal)
                             ── TxExecutor   (TxManager   | events.Journal)
internal/events.Journal ── repository/events (Append/Replay) + Hub
```

Кросс-доменные транзакции: `TxManager.WithTx(ctx, func(ctx, tx))` +
`NewTx(sqlTx)` в каждом доменном пакете (адаптация паттерна
dashboard-manager на database/sql, разрешена D-80).

## Как проверялось

- `make build && make test && make lint` — зелёные (test с `-race`).
- `make gen` дважды → пустой diff (idempotent).
- `npm run build` в web/ — зелёный.
- Смоук: демон стартует, мигрирует чистую БД, `/healthz`=ok, порт из env.
- `PRAGMA integrity_check` — ok (cleanup каждого теста store).
- Acceptance-пункты тасок покрыты тестами (см. «Итог» в каждой таске).

## На что смотреть ревьюеру (риски/спорные места)

1. **D-02**: замена go-swagger → oapi-codegen зафиксирована в overview.
   Убедиться, что T-05 примет oapi-codegen (strict-server) — иначе править
   D-02 ещё раз.
2. **MaxOpenConns(1)**: все чтения сериализованы за единственным
   соединением. Осознанно (корректность > перфоманс локального демона), но
   replay 100k событий и live-нагрузка делят одно соединение.
3. **Сигнатура WithTx(ctx, func(ctx, tx) error)**: ctx из аргументов fn
   ОБЯЗАТЕЛЕН для Journal.Append (иначе события не публикуются — молча).
   Кандидат на явную проверку при ревью будущих вызовов.
4. **Reject гейта → run failed**: жёсткая политика, зашита в машине
   (escalation-гейт с reject тоже фейлит ран). Подтвердить, что это
   соответствует замыслу D-21/ADR-001.
5. **OriginPatterns: ["*"]** в WS — осознанно (localhost-демон), token —
   единственная защита (D-08).
6. **genapi-модели генерятся, но пока не используются** в Go-коде
   (wire-формат WS описан руками в controller/ws, соответствует спеке).
   T-05 должен перейти на сгенерированные типы.
7. Property-тест сидирован (seed 42) — недетерминизм не ожидается, но
   расширение набора команд приветствуется.
8. Пре-существующие staged-удаления `pkg/harness/codex/*` и изменённый
   `go.mod` были в worktree до начала работ — сохранены как есть.

## Не входило в scope (следующие таски)

- REST API + go-swagger/oapi-codegen server scaffold (T-05).
- Harness-интерфейс и адаптеры (T-06..T-08), supervisor (T-09),
  git (T-10), гейты-диалог (T-11), lifecycle демона (T-12: запись порта в
  daemon.json, token-файл, startup recovery вызов RecoverInterrupted).
- vendor_index наполнение (T-23), UI (T-13+).

---

## Дополнение (2026-08-16, после сдачи): layout backend/

Весь Go-код перенесён в `backend/` (запрос пользователя): `backend/{cmd,
internal,migrations,pkg}`, `backend/go.mod`, `backend/vendor`,
`backend/.golangci.yml`. `api/` остался в корне как общий контракт
фронта и бека. Makefile/CI работают через `cd backend`; `make
gen/build/test/lint` после переноса — зелёные. Изменения зафиксированы
в 00-overview.md (Архитектура + D-80, с датой).

По вопросу «зачем usecase, если service пуст» (обсуждение с пользователем):
разделение оставлено — usecase = сценарии/оркестрация (стейт-машина),
service = доменные сервисы (supervisor T-09, gitx T-10, harness T-06..08,
vendor-память T-23, notify T-19); service заполнится начиная с T-05/T-09.
