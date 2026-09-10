# Glamor — обзор продукта и реестр решений

> Это не ТЗ, а слепок архитектурных решений, принятых в обсуждении.
> Каждая таска ссылается сюда. Если решение меняется — правится этот файл.

## Суть

Десктопное приложение для запуска конфигурируемых многоэтапных AI-пайплайнов
разработки (plan → code → review → fix → ...) поверх CLI-harness'ов
(kimi, qwen, claude-code, codex, opencode). Ядро-оркестратор
детерминировано: LLM только внутри этапов, никогда в контуре координации.

Предшественник: bash-пайплайн `~/go/ai-pipeline` (роли в `roles/`,
vendor-память, гейты) — переносим идеи и промпты, не код.

## Архитектура

```
   Tauri/Web UI ─┐
   CLI (glamor) ─┼── HTTP (REST, OpenAPI) + WebSocket ──► glamord ──► harness-процессы
   TG-adapter   ─┘        (in-proc модуль демона)             │
                                                              ▼
                                                   SQLite (WAL, event journal =
                                                   единый источник истины)
```

- **glamord** — Go-демон, единственный писатель в SQLite. Переживает рестарт
  UI, крах окна, sleep.
- **UI** — React+Vite SPA в Tauri-shell. Данные — только по HTTP/WS localhost,
  никакого Tauri IPC для бизнес-данных. Браузерный фолбэк бесплатен.
- **TG-адаптер** — модуль внутри демона, клиент тех же внутренних событий/
  команд, что и UI. Никаких приватных лазеек.
- Монорепо: `api/` (openapi.yaml — общий контракт фронта и бека),
  `backend/` (весь Go-код: `cmd/glamord`, `cmd/glamor` (CLI), `internal/
  {controller,service,repository,events,harness,gitx,notify,dto,
  cstmerrors}`, `migrations/`, `pkg/`, go.mod + vendor), `web/`, `src-tauri/`.
  *(уточнено 2026-08-16: изначально Go-код лежал в корне — вынесен в
  `backend/`, чтобы не смешиваться с web/ и src-tauri/.)*

## Реестр решений (D-NN)

### Платформа и стек

- **D-01** Backend: Go, отдельный демон `glamord`. UI — клиент.
- **D-02** API: OpenAPI-first (`api/openapi.yaml` — source of truth),
  кодогенерация Go (серверный scaffold + модели), TS-клиент фронта
  генерится из той же спеки.
  *(уточнено 2026-08-16: изначально планировался go-swagger, но он
  поддерживает только Swagger 2.0 и не работает с OpenAPI 3 — заменён на
  **oapi-codegen** (net/http, модели + strict-server scaffold в T-05);
  TS-типы — **openapi-typescript**. Спека пишется в OpenAPI 3.0.3.)*
- **D-03** Транспорт: `net/http` (НЕ fasthttp — go-swagger генерит net/http,
  fasthttp не поддерживает hijack/streaming, coder/websocket требует
  http.Hijacker; для локального демона перфоманс fasthttp нерелевантен).
- **D-04** WS: `coder/websocket`, один endpoint `/ws`, подписки по `run_id`.
- **D-05** SQLite: `modernc.org/sqlite` (pure Go, без CGO), WAL-режим,
  миграции goose v3 **Go-файлами, по образу dashboard-manager**
  (`migrations/<timestamp>_name.go` + `AddMigrationContext`, blank-import
  в main, `goose.Up` при старте демона), запросы через
  `huandu/go-sqlbuilder` (динамика) + голый SQL (простые статичные).
- **D-06** UI: React + TypeScript + Vite + zustand + `@xyflow/react`
  (граф и редактор пайплайнов). Стрим-виджет кастомный: Streamdown для текста,
  чипы для tool calls, сворачиваемые thinking-блоки. НЕ xterm.js.
- **D-07** Упаковка: Tauri с первого дня (только shell: окно, трей,
  нотификации, updater; демон как sidecar). Electron отклонён.
- **D-08** Localhost-API защищено токеном (`~/.glamor/token`, perms 600),
  порт динамический, записывается в `~/.glamor/daemon.json`.

### Ядро и надёжность

- **D-10** Стейт-машина живёт только в SQLite. Память демона — восстанавливаемый
  кэш. Переходы состояний — транзакции с CAS
  (`UPDATE ... WHERE state = ?`).
- **D-11** Event journal (таблица `events`, append-only, монотонный id) —
  outbox: событие пишется в той же транзакции, что и переход состояния;
  клиенты догоняют по `last_event_id`. Даёт: resume UI, историю рана,
  receipts, TG-доставку.
- **D-12** Идемпотентность команд: `Idempotency-Key` на все мутации;
  TG использует `update_id` как ключ.
- **D-13** Источник истины о завершении этапа — exit code процесса +
  наличие/валидация файла-артефакта. Stream-json парсится только для UI и
  метрик; падение парсера деградирует стрим до сырого текста, ран не падает.
- **D-14** Единый контур «прерванный этап»: stall-watchdog (нет событий N сек),
  non-zero exit без артефакта, error-события стрима, сироты после рестарта
  демона → стадия `interrupted` → auto-resume (max N попыток, backoff).
  Остановка пользователем = запись `stop_requested_by` в БД ДО убийства
  процесса → auto-resume не срабатывает.
- **D-15** Graceful shutdown: SIGTERM → стоп приёма новых ранов → прерывание
  живых этапов (пометка `interrupted`, НЕ `failed`) → закрытие listeners →
  выход. Startup recovery: скан `running` → добить сирот → `interrupted` →
  тот же auto-resume. Reattach к живым harness-процессам не делаем.
- **D-16** Auto-resume = resume сессии harness'а (`session_id` в `run_stages`)
  с системным сообщением «продолжи, ты был прерван».

### Диалог и управление

- **D-20** Двусторонний диалог — вариант А: «вопросы как артефакт».
  Этап кладёт `questions.md` и завершается → гейт в UI/TG → ответ →
  resume сессии с ответом. ACP — отложенный эксперимент (M4).
- **D-21** Гейты — first-class объекты в БД (`gates`): approve/reject/answer/
  comment. Видны в UI (чат + плашка на ноде + инбокс) и TG (inline-кнопки,
  reply-to-message).
- **D-22** Вмешательство в работающий этап: Interrupt & Steer (прервать →
  resume с сообщением) и Queue Note (заметка к ближайшему событию без
  прерывания). Прямого ввода в живой процесс нет.
- **D-23** Fix-петля: fixer = resume сессии кодера + verdict-фидбек.
  Лимит итераций → эскалация-гейт пользователю.

### Git и изоляция

- **D-30** Worktree НЕ используем. Переноса результата (apply-to-checkout)
  НЕТ. Ран работает в основном чекауте проекта.
- **D-31** Старт рана: базовая ветка (дефолт — default branch проекта) +
  опционально имя рабочей ветки. Не указана → `glamor/<slug>` от базовой.
- **D-32** Пайплайн НИЧЕГО не коммитит и не пушит. Пользователь смотрит diff
  в своей IDE и коммитит сам. Результат рана = незакоммиченные изменения
  на ветке.
- **D-33** Lock: один активный ран на `(project_id, branch)`. Параллельные
  раны на разных проектах — да (пул процессов, лимит в конфиге, дефолт 4).
- **D-34** Preflight при старте: чекаут чист (или явный override),
  ветка не занята другим раном.

### Harness'ы

- **D-40** Harness Adapter interface в Go; каждый harness — пакет
  `internal/harness/<name>`. Формат stream-json конкретного CLI заперт
  внутри адаптера.
- **D-41** Нормализованная схема событий для ядра и фронта:
  `thinking / text / tool_call / tool_result / usage / error / system`.
- **D-42** M1: адаптеры kimi + qwen. M3: claude-code, codex, opencode.
  pi (pi-mono) — поддержку решили НЕ делать (2026-08-14).
  Capability matrix: `tasks/research/harness-capability-matrix.md`.
- **D-43** Effort/модель per-stage; capability matrix видна в UI (нет effort —
  ручка disabled).

### Память и метрики

- **D-50** Vendor-память — файлы (НЕ в БД), двухуровневая
  (`~/.glamor/vendors/` + `<repo>/.glamor/vendors/`), формат из
  ai-pipeline/vendors/README.md. Обновления = артефакт-дельта рана,
  применяется ядром атомарно; diff виден в summary. Опциональный гейт на
  глобальную память. FTS5-индекс в SQLite для релевантной выборки.
- **D-51** Метрики токенов/времени per stage из stream-json usage / логов.
- **D-52** Причинно-следственная память (Lessons): карточки
  «ситуация → симптом → причина → правило», извлекаются distill-этапом
  из журнала рана, сохраняются ТОЛЬКО после подтверждения пользователем
  (гейт «Сохранить урок?»), инъецируются в промпты следующих этапов
  (FTS, top-K, токен-бюджет). Внешние memory-фреймворки (mem0/Zep/Letta)
  не используем — локальность и файловая модель. Детали: T-29.
- **D-81** Эволюция уроков (расширяет D-52): distill анализирует полный
  детерминированный трейс поведения рана (findings ревьюера, fix-итерации,
  падения проверок, ответы на гейтах, исход), а не только сигналы
  пользователя; карточки эволюционируют дельтами NEW/REFINE/SUPERSEDE/LINK
  вместо только-добавления; vendor-уроки «вендор + точная версия + область
  + урок» из ресёрча планировщика с outdated-деградацией при смене версии;
  ретрив — скоринг relevance×importance×recency вместо FTS top-10;
  relapse/outcome-трекинг применений. Distill — **обязательная часть
  пайплайна** при настройке «формировать уроки» (settings.lessons в
  spec_json): ядро дописывает встроенный distill, если этап удалили, и
  запускает его на любом терминальном исходе, включая failed; выключение —
  мастер-switch, пропускающий distill даже при наличии ноды в YAML.
  Подтверждение пользователем, FTS5 и файловая модель сохраняются.
  Детали: T-30.

## Наследие orca/kent — что берём и что нет

Из **orca**: гейты как объекты БД (D-21), receipts/аудит сырых вызовов
(логи ранов, T-09), мобильный доступ → наш TG-адаптер (D-70, лучше
companion-приложения), протокол явного завершения воркера →
артефакт-контракт этапов (D-13). Worktree-изоляция — отклонена (D-30).
Federated workers, setup hooks — не берём.

Из **kent**: разделение ролей и триплеты harness/model/effort (D-43),
external-boundary inventory в планировщике, модель кардинальности,
assumption register, regression-first для багфиксов, независимый ревьюер
со свежей сессией, маленький strict-JSON verdict (всё — в T-17, промпты).
Кандидаты при портировании ролей: execution contract (versioned spec),
invariants/forbidden_patterns с привязкой к источнику, edge-case matrix
для deep, consumer-research блок, probes в лёгкой форме (список команд
в spec без детерминированных гейтов).

Осознанно **не берём** (токены/падения): гигантские strict-JSON контракты
плана, SHA-аттестацию скиллов, детерминированные гейты на каждый чих,
обязательные непустые массивы.

### Кодстайл (SALT)

- **D-80** Код пишем в стиле SALT-сервисов пользователя (референсы:
  `~/go/salt/dashboard-manager`, security-gate). Конкретика:
  - **Layout**: `backend/cmd/glamord` (main + config.go), слои —
    `backend/internal/controller/{http,ws}` (транспорт) →
    `internal/service/<domain>` (бизнес-логика, оркестрация, владение
    транзакциями) → `internal/repository/<domain>` (хранение). Плюс наши
    домены: `internal/harness/<name>`, `internal/events`, `internal/gitx`,
    `internal/notify/telegram`. Общие хелперы — `backend/pkg/`.
    *(уточнено 2026-08-16: Go-корень — `backend/`.)*
    *(уточнено 2026-08-16: **слой `internal/usecase` УПРАЗДНЁН** — решение
    пользователя после ревью T-01..T-13. Оркестрация сценариев и
    cross-repository координация — роль service (канон SALT: repository
    координирует service, отдельный usecase-слой для локального демона —
    избыточная церемония). Операционная форма правил и запретов —
    project-скилл `.agents/skills/glamor-architecture`.)*
    *(уточнено 2026-08-17, fix-task-2 F-09: доменный пакет
    (`internal/notify/telegram`, `internal/daemon`, `internal/gitx`) МОЖЕТ
    владеть репозиторием собственного состояния (`repository/telegram`),
    но бизнес-чтения и мутации чужих доменов (runs/stages/gates/projects/…)
    делает только через service (`catalog` — чтения/агрегации, `runsapi` —
    действия). Причина: TG-адаптер читал runs/stages/gates/projects
    напрямую — незафиксированный прецедент «доменам можно всё»; read-
    агрегация /status перенесена в `catalog.ListActiveRunsStatus`.)*
  - **DTO между слоями**: `internal/dto/{dtoctrl,dtosvc,dtorep}` —
    данные, пересекающие границы слоёв, не утекают чужими типами; модели
    репозитория неэкспортируемы, маппинг — `mappers.go` в каждом слое.
    *(уточнено 2026-08-16: `dtousecase` упразднён вместе со слоем usecase;
    `dtoctrl`/`dtosvc` вводить только при реальном расхождении формы
    границ, не «на будущее».)*
    *(уточнено 2026-08-16, fix-task-0 F-04, вариант A):* `dtorep` — единый
    доменный DTO-язык демона на всех границах внутри backend (локальный
    демон, один модуль — слоёвые DTO избыточны); `genapi` остаётся
    транспортной границей. Пустые `dtoctrl`/`dtosvc`/`dtousecase` удалены;
    разделение отложено до появления второго потребителя (TG-адаптер, M2).
  - **Repository-паттерн как в dashboard-manager**: `interfaces.go`
    (интерфейс, в т.ч. `RepositoryWithTX`), общий `query`-struct, работающий
    и на пуле, и на транзакции; `OpenTx(ctx) (Tx, error)` с
    `Commit/Rollback`. Для SQLite — та же форма поверх `database/sql` +
    modernc вместо pgx.
  - **Запросы**: `huandu/go-sqlbuilder` (во flavor SQLite) — совпадает с
    D-05 и с тем, что уже используется в SALT.
  - **Ошибки**: сентинелы в `internal/cstmerrors` (`var ErrNotFound =
    errors.New(...)`), оборачивание `fmt.Errorf("...: %w")`,
    `errors.Is/As` на границах.
  - **Config**: структура с вложенными struct'ами и тегами
    `validate:"required"` (как `cmd/app/config.go`), файлы конфигурации
    на окружение — у нас `~/.glamor/config.yaml`.
  - **DI вручную в main** (конструкторы `NewRepository(...)`,
    `NewService(...)`), без DI-фреймворков.
  - **Трейсинг**: в SALT — otel tracer per package (`tracerName` = полный
    путь пакета). В локальном демоне упрощаем до `log/slog` с полем
    `component=<package>`; если захочется otel — добавим отдельно.
  - gofumpt-форматирование, импорты группами: stdlib / внешние / свои.
  - *(уточнено 2026-08-16, fix-task-0 F-02):* `TxExecutor`/`EventAppender`
    оперируют `*sql.Tx` — осознанное отклонение: единственный писатель
    SQLite, `pkg/commontx` — аналог dashboard-manager; service не исполняет
    SQL, а только собирает tx-обёртки репозиториев через `NewTx(tx)`.
  - *(уточнено 2026-08-16, fix-task-0 F-03):* supervisor — оркестратор
    тика (`internal/service/supervisor`), читает репозитории напрямую
    (event-driven tick, ADR-001); стейт-машина — `internal/service/runsmachine`;
    `internal/service/runsapi` — API-сценарии (идемпотентность D-12,
    git-preflight T-10); `internal/service/catalog` — read-фасад/CRUD
    для контроллера. Слой usecase упразднён (см. выше).

### UX

- **D-60** IA: Проекты → Проект (селектор пайплайна → раны этого пайплайна)
  → Ран → Редактор пайплайнов.
- **D-61** Экран рана: слева граф (xyflow, пульс активной ноды), справа панель
  этапа (Стрим/Артефакты/Промпт/Метрики), снизу чат с пайплайном (гейты).
- **D-62** Редактор пайплайнов — красивый граф-редактор на xyflow. Ноды:
  llm-stage / janitor / human-gate. Рёбра: обычные + loop-edge (max_iters,
  эскалация). Валидация: один entry, нет недостижимых нод, циклы только через
  loop-рёбра.
- **D-63** «Открыть в IDE»: настраиваемая команда (goland/code/cursor)
  на проект.
- **D-64** Keyboard-first: j/k по этапам, a — approve, c — comment.

### Telegram

- **D-70** TG = облегчённый пульт: нотификации по этапам, гейты с кнопками,
  ответы reply-to-message, `/status`, `/stop`. НЕ стримить вывод harness'а.
- **D-71** Long polling (без webhook). Токен в конфиге. Привязка: /start →
  код в UI → whitelist chat_id.
- **D-72** Оффлайн-доставка: offset последнего доставленного event_id в БД;
  после простоя — сводка одним сообщением.
- **D-73** Per-run флаг «не уведомлять в TG» (NDA-задачи).

## MVP-разбиение

- **M1** — скелет: демон, стейт-машина, журнал, WS, REST, kimi/qwen адаптеры,
  supervisor (auto-resume), git-интеграция, гейты, Web UI (граф, стрим, чат),
  дефолтный пайплайн plan→code→review→fix, Tauri-shell.
- **M2** — TG-адаптер, граф-редактор пайплайнов, версионирование + YAML
  import/export, janitor-этапы.
- **M3** — vendor-память UI + FTS5, метрики, «Открыть в IDE»,
  адаптеры claude-code/codex/opencode/pi.
- **M4** — ACP-эксперимент (живое вмешательство), fan-out подзадач, updater.

## Карта тасок

См. файлы `T-NN-*.md` в этом каталоге. Статусы: `todo | in_progress | done`.
Исследования: `research/`.
