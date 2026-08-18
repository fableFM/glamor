# T-05 OpenAPI-контракт + REST API (go-swagger)

Статус: done (2026-08-16) · M1 · зависимости: T-01, T-03

## Цель

`api/openapi.yaml` как единый контракт (D-02): серверный scaffold и модели —
go-swagger, TS-клиент фронта — из той же спеки.

## Эндпоинты (v1)

- `GET /projects`, `POST /projects`, `GET /projects/{id}` (с активными ранами
  и счётчиком открытых гейтов), `PATCH /projects/{id}` (ide_command,
  default_branch).
- `GET /projects/{id}/pipelines`, `GET /pipelines/{id}` (с версиями).
  M1: пайплайны read-only (дефолтный из T-17); CRUD — T-20/21.
- `POST /runs` — body: project_id, pipeline_version_id, task_text,
  base_branch?, branch?, depth, notify_tg. Header `Idempotency-Key` (D-12).
  Ответ 201 или 200-с-тем-же-run при повторе ключа.
- `GET /runs?project_id=&pipeline_id=&state=` (фильтры через sqlbuilder),
  `GET /runs/{id}` (полный срез: стадии, гейты, артефакты).
- `POST /runs/{id}/stop` (идемпотентно, D-14), `POST /runs/{id}/resume`
  (ручной рестарт interrupted/failed).
- `POST /gates/{id}/resolve` — body: action (approve/reject/answer/comment),
  text?; header `Idempotency-Key`.
- `POST /runs/{id}/notes` — queue note (D-22).
- `POST /stages/{id}/interrupt` — Interrupt & Steer: body message.
- `GET /runs/{id}/events?after_id=&limit=` — REST-доступ к журналу
  (для отладки и TG-догона).
- `GET /healthz`, `GET /version`.

## Требования

- Все мутации идемпотентны по ключу (D-12); семантика повтора: вернуть
  первый результат (ключ хранится, ответ восстанавливается).
- Аутентификация: токен из `~/.glamor/token` (D-08), middleware на всё,
  кроме `/healthz`.
- Ошибки: единый формат `Error{code, message, details?}`; коды домена
  (`run_locked`, `invalid_transition`, `gate_already_resolved`, ...).
- Генерация: `swagger generate server` — но тонко: используем сгенерированные
  модели + валидаторы, роутинг/хендлеры свои поверх (или стандартный
  scaffold — решить в реализации, критерий: минимум ручного кода поверх
  генерённого, `make gen` идемпотентен).
- TS-клиент: openapi-typescript / orval в `web/src/api/`.

## Acceptance

- Спека валидна (`swagger validate`), генерация воспроизводима.
- Интеграционные тесты хендлеров на реальной SQLite (tmp файл): CRUD
  проектов, создание рана с повтором Idempotency-Key → один ран,
  resolve гейта дважды → второй раз 200 с тем же результатом (или 409
  `gate_already_resolved` — выбрать и зафиксировать; рекомендация: 200 +
  флаг `already_resolved: true`, чтобы сетевые ретраи были безопасны).
- `Error` во всех неуспешных ответах соответствует схеме.

## Итог (2026-08-16)

Статус: done (2026-08-16)

Сделано:
- `api/openapi.yaml` — полный REST v1: projects (CRUD+PATCH), pipelines
  (read-only), runs (create/list/detail/stop/resume/notes/events), gates
  resolve, stages interrupt, healthz/version, /ws. Единый `Error{code,
  message, details?}` с default-ответом на ВСЕХ эндпоинтах.
- Кодогенерация: oapi-codegen (models + strict-server + std-http,
  embedded-spec) в `internal/controller/http/genapi/api.gen.go` — решение
  «сгенерированные модели + strict interface, роутинг из спеки», ручного
  кода поверх генерённого минимум (handlers + mappers).
- `internal/controller/http`: handlers (StrictServerInterface), mappers
  (dtorep→genapi), errors (доменные сентинелы → коды/статусы), server
  (auth-мидлварь Bearer на всё, кроме /healthz; MountOn: /ws — отдельный
  контроллер, паттерн специфичнее catch-all).
- Usecase (`internal/usecase/runs/api.go`): CreateRun (идемпотентность по
  ключу → 200 первым раном; lock ветки через частичный UNIQUE-индекс →
  409 run_locked; slug ветки glamor/<slug>; preflight-хук для T-10),
  StopRun (stop_requested_by=user + interrupted running-стадий в одном tx),
  ResumeRun (failed→running — **ADR-001 дополнен 2026-08-16**),
  ResolveGateAPI (повтор тем же резолюшном → 200 already_resolved=true,
  другим → 409 gate_already_resolved), InterruptStageSteer (steer-заметка),
  CreateNote (идемпотентно).
- Миграция 20260816120000: таблица `notes` (queue note/steer, D-22) +
  частичный UNIQUE-индекс `idx_runs_active_branch` (D-33).
- Репозитории: +notes (полный паттерн), +UpdateProject,
  +ListPipelinesForProject, +ListPipelineVersions, +ListGatesByRun.

Отклонения/решения:
- go-swagger → oapi-codegen (уже зафиксировано в D-02 от 2026-08-16).
- Семантика повтора резолва гейта: 200 + already_resolved (рекомендация
  таски принята).
- POST /runs создаёт ран в draft; запуск этапов — supervisor (T-09),
  предусмотрен хук SetPreflight для git-preflight (T-10).
- /ws остался ручным хендлером (oapi-codegen strict не поддерживает
  upgrade); в спеке задокументирован, монтируется на родительском mux.

Проверка: `go test -race ./...` зелёный — интеграционные тесты на реальной
SQLite: CRUD проектов, идемпотентность POST /runs (201→200, один ран),
run_locked 409, resolve гейта дважды (200 already_resolved / 409),
stop/resume семантика, notes+steer в срезе рана, auth 401 без/с неверным
токеном, /healthz без auth. `make lint` — 0 issues; `make gen` идемпотентен.

## Дополнение к Итогу (2026-08-17, fix-task-4)

- F-01: `runsapi.CreateRun` пишет событие `run.created` в журнал в той же
  транзакции, что и создание рана (D-11); повтор по Idempotency-Key второго
  события не пишет (D-12). В спеке: `EventKind` += `run.created`, новая
  схема `RunCreatedPayload`, `EventPayload` документирует маппинг.
- F-02: новый эндпоинт `GET /runs/{id}/artifacts/{artifactId}/content`
  (text/plain; чужой/несуществующий/traversal → 404; > 5 МБ → 413
  `too_large`). Реализован через strict-интерфейс oapi-codegen (прецедент
  text/yaml export), кастомный handler не понадобился. Сервис —
  `catalog.GetArtifactContent` (чтение записи из repository/artifacts,
  проверка пути внутри run_dir, чтение файла — всё в service-слое);
  repository/artifacts += `GetArtifactByID`; cstmerrors += `ErrTooLarge`.
- Тесты: runsapi (событие в tx, live-доставка через Hub, replay, дедупликация
  по ключу), controller/http (200/404/traversal/missing file/413).
- Проверка: `go build/vet/test -race` зелёные, `make gen` идемпотентен.
