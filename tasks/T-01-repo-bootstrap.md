# T-01 Bootstrap монорепо

Статус: done (2026-08-16) · M1 · зависимости: нет

## Цель

Каркас репозитория `glamor`, в котором собираются и демон, и web UI, и
Tauri-shell, с воспроизводимыми командами сборки/тестов/кодогенерации.

## Scope (in)

- Структура каталогов по 00-overview.md (D-80, SALT-стиль):
  `cmd/glamord` (main + config.go), `cmd/glamor`, `internal/controller/
  {http,ws}`, `internal/usecase`, `internal/service/`, `internal/
  repository/`, `internal/harness/`, `internal/events`, `internal/gitx`,
  `internal/notify/`, `internal/dto/`, `internal/cstmerrors`, `api/`
  (openapi.yaml), `web/`, `src-tauri/` (пустышка до T-18),
  `docs/adr/`, `migrations/`, `pkg/`.
- `go.mod` (Go 1.24+), минимальный `main.go` демона («hello, port from env»)
  с Config-struct'ом в `cmd/glamord/config.go` (validate-теги, D-80).
- `web/`: Vite + React + TS scaffold, `npm run dev/build`.
- `Makefile` или `Taskfile.yml`: `gen` (openapi→Go + TS), `build`, `test`,
  `lint`, `dev` (демон с авторелоадом необязателен, достаточно run).
- `.golangci.yml` (разумный дефолт: govet, staticcheck, errcheck, gofumpt).
- Базовый CI (GitHub Actions или локальный скрипт): build + test + lint +
  `npm run build`.
- `docs/adr/` — шаблон ADR; ADR-001 = стейт-машина (см. T-03).

## Scope (out)

- Любая бизнес-логика, БД, API-эндпоинты — отдельные таски.
- Tauri-настройка (T-18).

## Ключевые решения

- D-01, D-02, D-05, D-06 (00-overview.md).
- Кодогенерация openapi запускается явной командой `make gen`, сгенерённое
  коммитим (ревьюабельные диффы контракта).

## Acceptance

- `make build && make test && make lint` зелёные с нуля на чистом клоне.
- `make gen` идемпотентен (повторный запуск — пустой diff).
- `web/` собирается, демон стартует и слушает порт из env.

## Итог (2026-08-16)

Сделано:
- Структура каталогов по D-80: `cmd/glamord` (main + config.go),
  `cmd/glamor` (заглушка), `internal/{controller/{http,ws},usecase,service,
  repository,harness,events,gitx,notify,dto/{dtoctrl,dtosvc,dtousecase,
  dtorep},cstmerrors}`, `api/`, `web/`, `src-tauri/` (пустышка),
  `docs/adr/`, `migrations/`, `pkg/`, `scripts/`.
- `cmd/glamord`: Config с validate-тегами (defaults < ~/.glamor/config.yaml <
  env `GLAMOR_*`), slog, `/healthz`, динамический порт (0), graceful shutdown
  по SIGINT/SIGTERM.
- `api/openapi.yaml` (OpenAPI 3.0.3): `/healthz` + компоненты Event/EventKind/
  EventPayload/SyncedMessage для журнала (T-04 дополнит payload-схемы).
- Кодогенерация `make gen`: Go-модели через **oapi-codegen** (go tool
  directive) → `internal/controller/http/genapi/models.gen.go`; TS-типы через
  **openapi-typescript** → `web/src/api/schema.d.ts`. Повторный запуск —
  пустой diff (проверено).
- `web/`: Vite + React 19 + TS (typescript закреплён на ^5 — peer-конфликт
  openapi-typescript с TS 6), `npm run dev/build/lint` работают.
- `Makefile`: gen/gen-go/gen-ts/build/test/lint/dev/web-build/ci.
- `.golangci.yml` (golangci-lint **v2**-формат): govet, staticcheck, errcheck,
  revive, gocritic, depguard, misspell, unconvert, unparam + gofumpt
  (formatters). В CI — golangci-lint-action@v7, v2.12.2.
- CI: `.github/workflows/ci.yml` — job `go` (build/test/lint), job `web`
  (npm ci + build), job `gen-idempotent` (make gen + git diff --exit-code).
- `docs/adr/`: 000-template.md + заглушка 001-state-machine.md (T-03).

Отклонения от таски/решений:
- **D-02 уточнён** (с пометкой в 00-overview.md): go-swagger не поддерживает
  OpenAPI 3 → заменён на oapi-codegen + openapi-typescript.
- golangci-lint взят v2 (v1.64.8 не поддерживает Go 1.25) — конфиг в v2-формате.
- В `web/` из шаблона Vite остался дефолтный App — заменяется в T-13.

Проверка: `make build && make test && make lint` зелёные; `make gen`
идемпотентен; демон стартует и слушает порт из env (`GLAMOR_HTTP_PORT=18123`,
`/healthz` → ok); `npm run build` в `web/` зелёный.

### Дополнение к Итогу (2026-08-16, layout)

По запросу пользователя весь Go-код вынесен в `backend/` (cmd, internal,
migrations, pkg, go.mod/go.sum, vendor, .golangci.yml). Корень: `api/`
(общий контракт), `backend/`, `web/`, `src-tauri/`, `docs/`, `tasks/`.
Makefile/CI обновлены (tarгеты через `cd backend`), D-80 и раздел
«Архитектура» в 00-overview.md уточнены с датой. Проверка после переноса:
`make gen/build/test/lint` — зелёные, gen идемпотентен.
