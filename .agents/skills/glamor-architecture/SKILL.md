---
name: glamor-architecture
description: Обязательные архитектурные правила и запреты backend'а glamor (Go, backend/internal) — слои controller → service → repository, полный запрет usecase-слоя, DI, DTO, ошибки. Использовать при любом написании, изменении или ревью Go-кода в backend/.
whenToUse: При создании, изменении или ревью любого Go-кода в backend/ репозитория glamor
---

# Архитектура backend'а glamor — обязательные правила

Закон проекта: `tasks/00-overview.md` (решения D-NN). Этот скилл —
операционная форма D-80 для ежедневной работы. Конфликт кода со скиллом =
дефект кода. Изменение правила — только правкой D-80 с датой и причиной.

## Слои и направление зависимостей

```text
controller (internal/controller/{http,ws})
  → service (internal/service/<domain>)
    → repository (internal/repository/<domain>)
```

Направление строго сверху вниз. Обратных и «горизонтальных» ходов
(controller→repository, service→controller) быть не может.

Доменные пакеты вне цепочки (разрешены D-80): `internal/harness/<name>`,
`internal/events`, `internal/gitx`, `internal/notify/telegram`,
`internal/daemon`. Их используют service и controller (events — журнал/WS),
но не repository.

## Запреты (НИКОГДА)

1. **Слой usecase ЗАПРЕЩЁН.** Не создавать `internal/usecase/**`, не
   называть типы `Usecase`, не вводить «сценарные» пакеты над service.
   Оркестрация сценариев, координация нескольких доменов, retry/policy —
   это роль service (сервис-оркестратор, напр. `service/supervisor`).
   Решение от 2026-08-16: для локального демона слой избыточен; канон
   SALT — repository координирует service, а не отдельный слой.
2. **Controller НЕ импортирует `internal/repository/*`.** Ни интерфейсы,
   ни константы, ни `dtorep` напрямую из repo-пакетов не являются
   основанием для импорта repository в controller.
3. **Service НЕ импортирует controller-пакеты** (`controller/http`,
   `genapi`, transport-типы) и не знает про HTTP/WS.
4. **SQL и `*sql.DB` — только в `internal/repository`** (+ `migrations/`,
   `cmd/` для `repository.Open`). Единственное зафиксированное исключение
   (D-80): `*sql.Tx` в сигнатурах `TxExecutor`/`EventAppender` для
   инварианта «CAS-переход + событие журнала в одном tx» (D-10/D-11) —
   service владеет tx-boundary, но не исполняет SQL сам.
5. **Self-DI запрещён.** Конструктор только сохраняет готовые
   зависимости (`NewX(deps...)`); он не вызывает `NewRepository`,
   не открывает БД, не ходит в сеть. Всё конструируется в
   `cmd/glamord/main.go` вручную, один раз, и раздаётся по слоям.
6. **Repository отдаёт наружу только `dtorep`**; модели в `models.go`
   неэкспортируемы; маппинг — `mappers.go` репозитория.

## Роли слоёв

- **Controller**: транспортная валидация (сгенерированная + комбинации),
  маппинг `genapi ↔ DTO`, перевод доменных ошибок в HTTP-статусы и
  безопасные сообщения. Один handler — один вызов entry point'а service.
  Никакой бизнес-логики, агрегаций и знания доменных состояний.
- **Service**: бизнес-глаголы и решения (что значит not-found/conflict/
  stale), идемпотентность, стейт-переходы, владение транзакциями,
  cross-repository оркестрация — включая read-агрегации вида
  `RunDetail`/`ProjectDetail`. Service может координировать другие
  services (оркестратор = service).
- **Repository**: ровно одна граница хранения. Форма как в
  dashboard-manager: `interfaces.go` (`Queries`/`RepositoryWithTX`/
  `OpenTx`/`Tx` + `commontx`), `query.go` (весь SQL; динамика —
  `huandu/go-sqlbuilder`, flavor SQLite), `repository.go`
  (`NewRepository(db)`/`NewTx(tx)`), приватные `models.go`, `mappers.go`.

## DTO, ошибки, интерфейсы

- DTO: транспортная граница — `genapi` (generated, не редактировать);
  внутренняя — `internal/dto/dtorep`. `dtoctrl`/`dtosvc` вводить только
  при реальном расхождении формы границ, «на будущее» — нельзя.
- Ошибки: сентинелы в `internal/cstmerrors`, оборачивание `%w`,
  классификация `errors.Is/As` на границах слоёв. Технический текст
  ошибки наружу не отдаём.
- Интерфейс объявляется у потребителя и содержит только реально
  используемые методы (исключение: `RepositoryWithTX` — устойчивая форма
  репозитория из референса, держим её целиком).

## Проверка перед сдачей

- `grep -rn "internal/repository" backend/internal/controller` — пусто.
- `grep -rn "internal/usecase" backend/` — пусто (слоя не существует).
- `grep -rn "sql.DB" backend/internal --include=*.go | grep -v repository` —
  пусто (кроме зафиксированного `*sql.Tx`-исключения).
- `NewRepository(` — только в `cmd/` и тестах.
- `make build && make test && make lint` зелёные; `make gen` → пустой diff.
