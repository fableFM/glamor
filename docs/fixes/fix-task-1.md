# Fix-task 1: полное упразднение слоя usecase (запрос для фикс-агента)

Дата: 2026-08-16 · Репозиторий: `/Users/user/go/fable/glamor`,
Go-модуль в `backend/` (`github.com/fableFM/glamor`).

> Это самодостаточный запрос. Передай агенту этот файл целиком.

## Контекст

Прошёл рефакторинг по `docs/fixes/fix-task-0.md`, но его целевая схема
была ошибочной: в ней usecase-слой ходил в repository напрямую. По канону
SALT (`salt-manager-service`, `references/architecture.md`) repository
координирует **service**, а usecase — только сценарии между services.
Пользователь принял решение: **слой usecase в glamor упраздняется
полностью** (зафиксировано в `tasks/00-overview.md`, D-80, уточнение от
2026-08-16).

## Обязательно прочитать перед работой

1. `.agents/skills/glamor-architecture/SKILL.md` — правила и запреты
   (это закон для этого фикса; при расхождении с fix-task-0.md верен
   скилл и обновлённый D-80).
2. `tasks/00-overview.md` — D-80 (с уточнениями), D-10/D-11 (CAS +
   событие в одном tx), D-12 (идемпотентность).
3. `docs/adr/001-state-machine.md` — семантика стейт-машины (не менять).
4. `AGENTS.md` — протокол работы.

## Цель

В репозитории не остаётся слоя usecase: ни пакета `internal/usecase`,
ни типов `Usecase`, ни импортов `internal/usecase/*`. Итоговое направление
зависимостей:

```text
controller (internal/controller/{http,ws})
  → service (internal/service/<domain>)
    → repository (internal/repository/<domain>)
```

Публичный контракт НЕ меняется: `api/openapi.yaml`, wire-формат WS,
схема БД, поведение стейт-машины (ADR-001), тела и статусы HTTP-ответов.

## Текущее состояние (проверить фактически, не доверять описанию)

До фикса по fix-task-0 было: стейт-машина в `internal/usecase/runs`
(machine.go, transitions.go, action.go, spec.go, api.go), supervisor в
`internal/service/supervisor` (содержал `*usecase.Machine`), контроллер
держал 7 репозиториев. Что именно получилось после fix-task-0 — сначала
сними фактическую карту:

```bash
cd backend
go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/... | grep glamor
grep -rn "internal/usecase" --include='*.go' . | grep -v _test
```

## Что куда переезжает

Применять к фактическому состоянию; имена пакетов можно уточнить, если
рядом уже есть устоявшийся dialect — но направление зависимостей
неизменно.

1. **Стейт-машина** (machine/transitions/action/spec) —
   `internal/service/runsmachine` (или существующий service-пакет, если
   fix-task-0 её уже перенёс). Это доменная бизнес-логика: CAS-переходы,
   гейты, recovery, владение tx «CAS + событие журнала» (D-10/D-11;
   `*sql.Tx` в `TxExecutor`/`EventAppender` — зафиксированное исключение,
   оставить как есть).
2. **API-сценарии ранов** (CreateRun, StopRun, ResumeRun, ResolveGateAPI,
   InterruptStageSteer, CreateNote) — в service домена runs (можно в тот
   же `service/runsmachine`, если пакет остаётся читаемым; иначе
   `service/runsapi`). Это бизнес-операции с идемпотентностью и
   preflight-хуками — роль service.
3. **Read-агрегации и CRUD для контроллера** (ListProjects, CreateProject
   с идемпотентностью по path, GetProjectDetail, PatchProject,
   ListProjectPipelines, GetPipeline, ListRuns, GetRunDetail,
   ListRunEvents) — в service-слой. Cross-repository агрегация
   (RunDetail из 5 репозиториев, ProjectDetail с openGates) — это
   «cross-repository orchestration», каноническая роль service.
   Допустимо: `internal/service/catalog` (read-фасад) + доменные методы
   в соответствующих service. Знание «активных» состояний рана
   (`draft/running/waiting_gate`) — константа service-слоя.
4. **Supervisor** — `internal/service/supervisor`: оркестратор тика,
   координирует `runsmachine` (service→service допустимо), `harness`,
   `events`, `gitx`-хуки. Если после fix-task-0 он уехал в usecase —
   вернуть в service, убрав импорты usecase.
5. **Controller**: только transport-валидация, маппинг `genapi ↔ DTO`,
   перевод ошибок в статусы. Один handler — один вызов entry point'а
   service. Если handler'у нужны данные нескольких services — ввести
   метод-агрегат в service, а не дёргать два services из контроллера
   (контроллер не оркестрирует).
6. **`internal/events`** остаётся доменным пакетом (D-80): журнал/Hub.
   Конструктор `NewJournal` принимает готовые зависимости
   (repo + TxManager + hub) из main — self-DI запрещён.
7. **DI**: все репозитории конструируются в `cmd/glamord/main.go` один
   раз и раздаются в services; services — в controller. Ни один
   конструктор не вызывает `NewRepository` внутри себя.
8. **DTO**: `internal/dto/dtoctrl/dtosvc/dtousecase` — пустые пакеты
   удалить (решение зафиксировано в D-80). Внутренняя модель — `dtorep`;
   если в процессе обнаружится реальное расхождение формы на границе
   controller↔service — можно ввести `dtosvc` точечно, с обоснованием в
   «Итоге».
9. **Мелочи** (если ещё живут после fix-task-0): константа `RunIDAll`
   не должна тянуть `repository/events` в контроллеры — перенести в
   `internal/events` или `dtorep`; мёртвые `var _ = ...` импорты убрать.

## Порядок работ

1. Снять фактическую карту импортов (команды выше), выписать план
   перемещений.
2. Перемещения пакетов/файлов + правка импортов (`go list`, gopls rename
   по желанию; движение механическое, без изменения логики).
3. Пересборка DI в `cmd/glamord/main.go`.
4. Тесты: менять ТОЛЬКО сборку зависимостей ( wiring в `*_test.go` под
   новые конструкторы). Assert'ы, сценарии, инварианты property-тестов —
   не трогать. Поведение не должно измениться ни в одном тесте.
5. Удалить `internal/usecase/` целиком и пустые dto-пакеты.
6. Gofumpt-формат; импорты группами stdlib / внешние / свои.

## Проверка (всё обязательно)

```bash
cd backend
grep -rn "internal/usecase" .  # пусто
grep -rn "internal/repository" internal/controller  # пусто
grep -rn "sql\.DB" internal --include='*.go' | grep -v repository | grep -v _test  # пусто
grep -rn "NewRepository(" --include='*.go' . | grep -v cmd/ | grep -v _test  # пусто
make build && make test && make lint   # зелёные (test с -race)
make gen && git diff --stat            # gen идемпотентен: пустой diff
```

Плюс: `go vet ./...` чист; web/ не трогаем.

## Запреты

- Не менять `api/openapi.yaml`, `genapi` (только через `make gen`),
  схему БД/миграции, поведение стейт-машины, harness-адаптеры, web/.
- Не ослаблять и не «чинить» тесты под новую структуру — если тест
  падает после механического переноса, это признак ошибки переноса.
- Никаких git-мутаций (commit/push/reset) без явной просьбы пользователя.
- Не вводить новых слоёв/абстракций «на будущее».

## Отчёт

В конце: список перемещённых файлов/пакетов, итоговая карта зависимостей
(`go list`), результаты всех проверок из чек-листа, отклонения от этого
запроса (если были) с причинами. Если меняются правила из
`.agents/skills/glamor-architecture` или D-80 — только через явную правку
`tasks/00-overview.md` с датой и причиной.
