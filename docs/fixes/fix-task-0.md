# Fix-task 0: приведение backend к архитектуре D-80 (слои)

Дата: 2026-08-16 · Составлено по результатам независимого ревью T-01..T-13.
Исполнителю: это самодостаточная инструкция. Репозиторий:
`/Users/user/go/fable/glamor`, Go-модуль в `backend/`
(`github.com/fableFM/glamor`). Закон: `tasks/00-overview.md` (D-NN),
`AGENTS.md`, `docs/adr/001-state-machine.md`.

## Conformance Brief

- **Публичный контракт НЕ меняется**: `api/openapi.yaml`, wire-формат WS,
  схема БД, поведение стейт-машины (ADR-001) — всё остаётся как есть.
  Меняется только внутренняя разводка слоёв и DI.
- **Целевое направление зависимостей** (D-80 + SALT-конвенция):
  `controller → usecase → service → repository`. Controller не знает про
  repository; service не знает про usecase; usecase не оперирует
  `*sql.DB`/SQL; репозитории конструируются только в `main` (DI вручную).
- **Поток моделей**: `genapi (HTTP) ↔ dtoctrl/dtousecase ↔ dtorep ↔ row`.
  Сейчас фактически `genapi ↔ dtorep` напрямую — см. F-04.
- **Владельцы**: транзакции CAS + событие журнала в одном tx (D-10/D-11) —
  стейт-машина; идемпотентность (`Idempotency-Key`, UNIQUE) — сценарии
  usecase + репозитории; транспортный маппинг ошибок — controller.
- **Доказательства**: `make build && make test && make lint` зелёные
  (тесты с `-race`), `make gen` идемпотентен, поведение существующих
  тестов не ослаблено. Рефакторинг НЕ должен менять assert'ы тестов,
  только их сборку зависимостей.

## Что сейчас зелёное (не сломать)

`go build ./...`, `go vet ./...`, `go test ./...`, `make lint`
(golangci-lint 2.12.2, `--build-tags e2e`, 0 issues), web: vitest 3/3,
`npm run build`, oxlint. Repository-слой — образцовый (форма
dashboard-manager: `interfaces.go` с `Queries`/`RepositoryWithTX`/`OpenTx`/
`Tx`, приватные `models.go`, маппинг только в `dtorep`), SQL существует
только в `internal/repository/*/query.go` и `migrations/` — **это не
трогаем и не ломаем**.

---

## F-01 (P0): controller/http ходит в repository напрямую

### Факт

`backend/internal/controller/http/server.go:21-37` — `Deps` содержит 7
репозиториев рядом с usecase:

```go
type Deps struct {
	Machine   *usecase.Machine
	Journal   *events.Journal
	Projects  projectsrep.RepositoryWithTX
	Pipelines pipelinesrep.RepositoryWithTX
	Runs      runsrep.RepositoryWithTX
	Stages    stagesrep.RepositoryWithTX
	Gates     gatesrep.RepositoryWithTX
	Artifacts artifactsrep.RepositoryWithTX
	Notes     notesrep.RepositoryWithTX
	...
}
```

То же дублируется в структуре хендлера
`backend/internal/controller/http/handlers.go:23-36`. Прямые вызовы
репозиториев из хендлеров (все — `handlers.go`):

| Хендлер | Строки | Что вызывает |
|---|---|---|
| `ListProjects` | 61 | `projects.ListProjects` |
| `CreateProject` | 70, 75, 86 | `GetProjectByPath`, `CreateProject`, `GetProjectByID` |
| `GetProject` | 94, 103, 113 | `GetProjectByID`, `runs.ListRuns`, `gates.ListOpenGates` (цикл по ранам) |
| `PatchProject` | 134, 143 | `UpdateProject`, `GetProjectByID` |
| `ListProjectPipelines` | 154 | `pipelines.ListPipelinesForProject` |
| `GetPipeline` | 162, 168 | `GetPipelineByID`, `ListPipelineVersions` |
| `ListRuns` | 235 | `runs.ListRuns` |
| `GetRun` | 243-261 | `GetRunByID` + `stages/gates/artifacts/notes.List*ByRun` (агрегация из 5 репозиториев) |
| `ResolveGate` | 337 | `gates.GetGateByID` (перечитка после `machine.ResolveGateAPI`) |
| `ListRunEvents` | 350, 365 | `runs.GetRunByID`, `journal.Replay` |

Побочные нарушения того же класса:
- `GetProject` (:100-118): контроллер знает доменный набор «активных»
  состояний рана (`draft/running/waiting_gate`) — бизнес-правило в
  транспорте.
- `CreateProject` (:69-73): идемпотентность по `path` (повтор → 200)
  реализована в транспорте, а не в сценарии.
- `handlers.go:369`: `_ = eventsrep.RunIDAll` — мёртвая ссылка ради
  импорта repository-пакета.

### Как чинить

1. Создать пакет `backend/internal/usecase/api/` — сценарии HTTP API,
   которым не нужна стейт-машина (чтения-агрегации + CRUD-сценарии).
   Структура принимает готовые интерфейсы репозиториев (форму
   `RepositoryWithTX` оставить как есть — она соответствует референсу):

   ```go
   type Usecase struct {
   	projects  projectsrep.RepositoryWithTX
   	pipelines pipelinesrep.RepositoryWithTX
   	runs      runsrep.RepositoryWithTX
   	stages    stagesrep.RepositoryWithTX
   	gates     gatesrep.RepositoryWithTX
   	artifacts artifactsrep.RepositoryWithTX
   	notes     notesrep.RepositoryWithTX
   }

   func New(projects projectsrep.RepositoryWithTX, ... ) *Usecase
   ```

2. Перенести логику из хендлеров в методы `Usecase` один-в-один,
   без изменения поведения:
   - `ListProjects(ctx)`, `CreateProject(ctx, ...)` (идемпотентность по
     path — внутри сценария: `GetProjectByPath` → вернуть существующий
     с флагом «уже был», хендлер по флагу выбирает 200/201),
     `GetProjectDetail(ctx, id)` (проект + раны + openGates; набор
     «активных» состояний рана — константа usecase-слоя),
     `PatchProject`, `ListProjectPipelines`, `GetPipeline`,
     `ListRuns`, `GetRunDetail(ctx, runID)` (агрегация 5 репозиториев),
     `ListRunEvents(ctx, runID, afterID, limit)` (run + `journal.Replay`;
     `*events.Journal` передать в конструктор `Usecase`).
   - `ResolveGate` (:337): перечитку гейта убрать — пусть
     `machine.ResolveGateAPI` возвращает резолвнутый гейт (расширить его
     сигнатуру), контроллер только маппит.
3. `Deps`/handler в controller: оставить только
   `Machine` (точнее — интерфейс нужных методов машины, объявленный в
   пакете controller: интерфейс объявляется у потребителя), `*api.Usecase`,
   `Draining`, `Token`, `Version`, `Harnesses`. Поля-репозитории удалить.
4. `mappers.go`: источник данных меняется только если вводятся dtousecase
   (см. F-04); в минимальном варианте маппинг `genapi ↔ dtorep` остаётся,
   но dtorep приходит из usecase-фасада.
5. `cmd/glamord/main.go:157-171`: собрать `api.New(...)` из тех же
   конструкторов репозиториев, передать в `Deps`.
6. Обновить `handlers_test.go:43-60` — тестовый сервер собирается тем же
   способом, что и main (через `api.New`). **Assert'ы не менять**:
   поведение (200/201/409, тела ответов) должно остаться один-в-один.

Приёмка: в `internal/controller/http` нет импортов
`internal/repository/*`; `grep -rn "internal/repository" backend/internal/controller` — пусто.

---

## F-02 (P0): usecase/runs держит `*sql.DB` и делает self-DI

### Факт

`backend/internal/usecase/runs/machine.go`:

- `:46` — поле `db *sql.DB` в `Machine`. После конструктора не читается
  нигде (`m.db` не встречается) — мёртвое поле storage-типа.
- `:59-74` — `NewMachine(db *sql.DB, appender EventAppender)` **сам**
  конструирует 6 репозиториев и TxManager:
  `runsrep.NewRepository(db)`, …, `repository.NewTxManager(db)`.
  По D-80 («DI вручную в main») репозитории рождаются в main и
  внедряются готовыми интерфейсами.
- `:32-41` — интерфейсы `TxExecutor`/`EventAppender` содержат `*sql.Tx`
  в сигнатурах (`WithTx(ctx, fn func(ctx, tx *sql.Tx) error)`):
  storage-тип торчит в контракте usecase. Внутри колбэков usecase строит
  tx-обёртки `runsrep.NewTx(tx)` (machine.go:128, 173, 205, 248, 335,
  374-375, 464-465, 548; api.go:128-129, 272-273, 380).

SQL-текста в usecase НЕТ (проверено grep'ом) — нарушение именно в
владении соединением и self-DI.

Та же болезнь у журнала: `backend/internal/events/journal.go:25-31` —
`NewJournal(db *sql.DB, hub)` сам строит `eventsrep.NewRepository(db)` и
`repository.NewTxManager(db)`.

### Как чинить

1. `NewMachine` принимает готовые зависимости (интерфейсы
   `RepositoryWithTX` объявлены в repository-пакетах и остаются там —
   это форма референса dashboard-manager; интерфейсы `TxExecutor`/
   `EventAppender` объявлены в usecase у потребителя — оставить):

   ```go
   func NewMachine(
   	runs      runsrep.RepositoryWithTX,
   	stages    stagesrep.RepositoryWithTX,
   	gates     gatesrep.RepositoryWithTX,
   	pipelines pipelinesrep.RepositoryWithTX,
   	projects  projectsrep.RepositoryWithTX,
   	notes     notesrep.RepositoryWithTX,
   	txm       TxExecutor,
   	appender  EventAppender,
   ) *Machine
   ```

   Поле `db` удалить. `repoAppender` (fallback при `appender == nil`)
   использует repo-слой — оставить, но он должен получать
   `eventsrep.RepositoryWithTX` из main, а не собираться из `db`.
2. `NewJournal(repo eventsrep.RepositoryWithTX, txm *repository.TxManager,
   hub *Hub) *Journal` — то же: зависимости снаружи.
3. `cmd/glamord/main.go`: сконструировать все репозитории **один раз**
   (`runsRepo := runsrep.NewRepository(db)` и т.д., `txm :=
   repository.NewTxManager(db)`), раздать в machine, journal, supervisor,
   api-фасад (F-01). Сейчас main уже конструирует их отдельно для
   supervisor и controller — после фикса дублирование исчезает.
4. `*sql.Tx` в сигнатурах `TxExecutor`/`EventAppender`: НЕ переписывать
   механику (это сознательная адаптация паттерна dashboard-manager под
   `database/sql`, кросс-доменные tx «CAS+событие» по D-11). Вместо этого
   **зафиксировать уточнение в D-80** (`tasks/00-overview.md`, с датой):
   «TxExecutor/EventAppender оперируют `*sql.Tx` — осознанное отклонение:
   единственный писатель SQLite, `pkg/commontx` — аналог
   dashboard-manager; usecase не исполняет SQL, а только собирает
   tx-обёртки репозиториев через `NewTx(tx)».
5. Обновить сборку зависимостей в тестах: `machine_test.go`,
   `events/events_test.go`, `events/machine_integration_test.go`,
   `handlers_test.go` — все они сейчас зовут `NewMachine(db, ...)`.

Приёмка: `grep -rn "sql.DB\|sql.Tx" backend/internal/usecase` — только
`*sql.Tx` в сигнатурах `TxExecutor`/`EventAppender` и `NewTx(tx)` в
колбэках (зафиксировано в D-80); поля `db` нет; `NewRepository(` вне
`internal/repository` и `cmd/` не встречается (кроме тестов).

---

## F-03 (P0): service/supervisor содержит usecase (инверсия слоёв)

### Факт

`backend/internal/service/supervisor/supervisor.go:79-93`:

```go
type Supervisor struct {
	machine   *usecase.Machine   // :80 — service содержит usecase
	registry  *harness.Registry
	journal   *events.Journal
	...
	runs      runsrep.RepositoryWithTX      // :89-93 + 4 своих репозитория
	stages    stagesrep.RepositoryWithTX
	projects  projectsrep.RepositoryWithTX
	pipelines pipelinesrep.RepositoryWithTX
	notes     notesrep.RepositoryWithTX
}
```

Конструктор `New(machine *usecase.Machine, ...)` (:107-112). Вызовы
машины из supervisor: `NextAction` (supervisor.go:241),
`TransitionRun` (:260, :294), `StartStage` (:265), `ResumeStage` (:301),
`OpenGate` (:284, gates.go:35, :59), `TransitionStage` (flow.go:110,
124, 147, 164, 170, 267), `UpdateRunningStage` (supervisor.go:321),
`ReenterStage` (gates.go:127). Плюс `DefaultConfig` тянет константу
`usecase.MaxResumeCount` (supervisor.go:44).

По смыслу supervisor — это оркестратор сценария «тик пайплайна»
(tick → reconcile → NextAction → действие), т.е. уровень usecase,
ошибочно лежащий в `service/`.

### Как чинить (рекомендуемый вариант)

Развести по настоящим ролям SALT:

1. **Machine → service.** Перенести стейт-машину (доменные бизнес-
   решения: CAS-переходы, гейты, recovery) из `internal/usecase/runs` в
   `internal/service/runsmachine`: файлы `machine.go`, `transitions.go`,
   `action.go`, `spec.go`, `machine_test.go`. Пакет `runsmachine`,
   импорты обновить. ADR-001 уже называет реализацию
   `internal/usecase/runs/` — дополнить ADR строкой с датой о переносе.
2. **API-сценарии остаются в usecase.** `api.go` (CreateRun, StopRun,
   ResumeRun, ResolveGateAPI, InterruptStageSteer, CreateNote) остаётся в
   `internal/usecase/runs`, поле `machine` меняет тип на
   `*runsmachine.Machine`.
3. **Supervisor → usecase.** Перенести пакет
   `internal/service/supervisor` → `internal/usecase/supervisor` целиком
   (все 9 файлов + testdata): это оркестратор тика, использующий service
   `runsmachine` (законно: usecase → service), доменный пакет `harness`,
   `gitx`-хуки и журнал. Поле `machine *usecase.Machine` становится
   `machine *runsmachine.Machine`. Прямые репозитории supervisor'а
   (чтения due-стадий и т.п.) на этом шаге оставить — зафиксировать в
   D-80, что оркестратор тика читает репозитории напрямую (event-driven
   tick, ADR-001).
4. `MaxResumeCount`: константа остаётся в `runsmachine`; импорт
   поправить.
5. Обновить `cmd/glamord/main.go` (импорты, порядок конструирования) и
   все тесты supervisor'а (`supervisor_test.go`, `dialog_test.go`,
   `recovery_test.go`, `recovery_helpers_test.go`) — только импорты и
   сборка зависимостей, не поведение.
6. Дополнить D-80 в `tasks/00-overview.md` (с датой): «supervisor —
   usecase-оркестратор тика; стейт-машина — service/runsmachine;
   usecase/runs — API-сценарии».

Недопустимый «быстрый» вариант: оставить supervisor в `service/` и
прятать machine за интерфейсом — направление service→usecase сохранится
по смыслу, пользователь это явно отклонил.

Приёмка: `grep -rn "internal/usecase" backend/internal/service` — пусто;
`go list -deps` не показывает service→usecase; направление импортов:
controller → usecase → {service/runsmachine, repository} .

---

## F-04 (P1): DTO-гигиена не реализована (пустые dto-пакеты)

### Факт

`internal/dto/dtoctrl`, `dtosvc`, `dtousecase` существуют, но пустые
(только `.gitkeep`); ни один `.go`-файл их не импортирует. Фактически
`dtorep` гоняется через все слои: controller маппит `genapi ↔ dtorep`
(`mappers.go:15-134`), `Machine` возвращает `*dtorep.Run/Stage/Gate/Note`
(api.go), controller/ws принимает `dtorep.Event` (handler.go:67).
D-80: «данные, пересекающие границы слоёв, не утекают чужими типами».

### Как чинить

Выбрать ОДИН вариант и зафиксировать в `tasks/00-overview.md` (D-80, с
датой):

- **Вариант A (минимальный, рекомендуется для M1):** признать `dtorep`
  единым доменным DTO-языком демона (локальный демон, один модуль,
  слоёвые DTO — избыточная церемония). Удалить пустые пакеты
  `dtoctrl/dtosvc/dtousecase`, дописать в D-80: «dtorep используется на
  всех границах внутри backend; genapi остаётся транспортной границей;
  разделение dtoctrl/dtosvc/dtousecase отложено до появления второго
  потребителя (TG-адаптер, M2)».
- **Вариант B (полный):** заполнить `dtousecase` (то, что отдаётся
  контроллеру: Project/ProjectDetail/Run/RunDetail/Stage/Gate/Note/
  Event), маппинг `dtorep → dtousecase` в usecase-слое,
  `dtousecase → genapi` в controller. Дороже, но строго по D-80.

Если фикс-агент не получил указания от пользователя — брать вариант A.

---

## F-05 (P2): мелочи (убрать заодно, дёшево)

1. `controller/ws/handler.go:20,86` и `controller/http/handlers.go:369` —
   импорт `repository/events` ради константы `RunIDAll`. Перенести
   константу в `internal/events` (домен журнала) или `dtorep`; из
   контроллеров импорт repository убрать.
2. `service/supervisor/flow.go:359` — `var _ = eventsrep.RunIDAll`
   (мёртвый импорт); после F-03 путь станет `usecase/supervisor/...`.
   Убрать.
3. `service/supervisor/gates.go:171` — `var _ = json.Marshal` (мёртвый
   импорт stdlib). Убрать.
4. Мёртвое поле `db` в `Machine` — закрыто в F-02.
5. `controller/http/errors.go:15` — `gitx.IsDirtyCheckout(err)` в маппере
   ошибок: допустимо (gitx — домен D-80), но убедиться, что после F-01
   preflight-ошибка доезжает до контроллера как сентинел/обёртка
   (`errors.Is`), а не как тип gitx в ответе.

---

## F-06 (P2): тестовые пробелы (по сверке acceptance T-01..T-13)

Не блокеры рефакторинга; оформить как отдельные доработки (можно в этом
же PR отдельными коммитами, если пользователь попросит):

1. **T-07/T-08: e2e-теги не написаны.** В `backend/` нет ни одного файла
   с `//go:build e2e`, хотя acceptance T-07/T-08 требует e2e с реальными
   CLI. Итоги тасок это честно признают. Минимум: пометить в файлах
   тасок acceptance-пункт как невыполненный или написать каркас e2e-теста
   (skip без бинаря), полноценный прогон — в T-17.
2. **T-04: нет WS-уровнего теста drop → close 1001** «resync required»
   (есть только hub-уровень `TestHub_SlowSubscriberDropped`; код close
   1001 — `controller/ws/handler.go:148`). Добавить тест на handler.
3. **T-05/T-12: нет HTTP-тестов** на `400 dirty_checkout` (код —
   errors.go:15, preflight не wired в тестовый сервер) и `503 draining`
   (код — handlers.go:189). Добавить.
4. **T-11: нет сквозного теста** эскалация → ответ → final_review →
   succeeded (механизмы покрыты разрозненно: escalation только через
   stall-watchdog supervisor_test.go:276). Добавить сценарный тест.

---

## Что НЕ трогать

- `internal/repository/**` (кроме переноса константы `RunIDAll` в F-05):
  слой соответствует референсу dashboard-manager.
- `internal/harness/**`, `internal/gitx/**`, `internal/daemon/**` —
  чистые доменные пакеты без чужих слоёв.
- `internal/controller/http/genapi/api.gen.go` — generated, только через
  `make gen`.
- Поведение стейт-машины и таблицы переходов (ADR-001), схема БД,
  openapi.yaml, web/.
- Assert'ы существующих тестов: рефакторинг меняет только сборку
  зависимостей в тестах, не ожидаемое поведение.
- Git-мутaции (commit/push) — только по явной просьбе пользователя.
  Отдельно заметить пользователю: вся работа T-01..T-13 сейчас untracked
  в worktree (один коммит `cda2594 initial commit`), плюс пре-существующие
  staged-удаления `pkg/harness/codex/*` — не трогать.

## Порядок выполнения

1. F-02 (DI machine/journal) + обновление main и тестов.
2. F-01 (usecase/api фасад, controller без репозиториев).
3. F-03 (runsmachine → service, supervisor → usecase, правки D-80/ADR-001).
4. F-04 (вариант A: удалить пустые dto-пакеты + правка D-80).
5. F-05 (мелочи).
6. F-06 — отдельно, по запросу пользователя.
7. Финал: `make build && make test && make lint` зелёные
   (`make test` гоняет `-race`), `make gen` → пустой diff, diff перечитан,
   правки `tasks/00-overview.md` (D-80) и `docs/adr/001-state-machine.md`
   с датами и причинами — по протоколу AGENTS.md.

## Проверка результата (чек-лист)

- [ ] `grep -rn "internal/repository" backend/internal/controller` — пусто.
- [ ] `grep -rn "internal/usecase" backend/internal/service` — пусто
      (кроме `service/runsmachine`, если он ссылается на общие типы —
      не должен).
- [ ] `grep -n "sql.DB" backend/internal/usecase backend/internal/events` —
      нет (кроме оговорённого `*sql.Tx` в TxExecutor/EventAppender).
- [ ] `NewRepository(` только в `cmd/` и тестах.
- [ ] `make build && make test && make lint` — зелёные; `make gen` ×2 →
      пустой diff.
- [ ] Все тесты T-02..T-12 проходят без изменения assert'ов.
- [ ] `tasks/00-overview.md` (D-80) и ADR-001 дополнены с датой.
