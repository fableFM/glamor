# Итог работы: T-14…T-21 + Makefile-команды (для независимого review)

Дата: 2026-08-17 · Исполнитель: Kimi Code (+3 фоновых субагента: T-14/15/16,
T-20, T-19 — результат каждого проверен мной повторно) · Репозиторий:
`github.com/fableFM/glamor` (Go в `backend/`, фронт в `web/`).
Таски: `tasks/T-14..T-21-*.md` (все `done`, кроме T-18 `blocked`, секции
«Итог»/«Блокер» в каждой). Предыдущие части: `docs/T-01-04-review-summary.md`,
`docs/T-05-13-review-summary.md` (+дополнение про слойную реорганизацию).

## Важный контекст: слойная реорганизация (не моя)

Параллельный ревью-агент в ходе этой работы применил project-скилл
`glamor-architecture` (уточнение D-80 от 2026-08-16 в overview): слой
usecase упразднён → `service/runsmachine` (стейт-машина), `service/runsapi`
(API-сценарии), `service/catalog` (read-агрегации); контроллер не
импортирует repository; self-DI запрещён (Journal собирается из
eventsRepo+TxManager+Hub). Я принял рефактор, прогнал полный сьют (зелёный)
и весь новый код писал в этой структуре.

## Makefile: управление сервисами (запрос пользователя)

- `make start` — build + запуск glamord (порт 7380, env-overridable
  `PORT=`) и web dev-сервера (`WEB_PORT=5173`, VITE_GLAMOR_API/TOKEN
  проброшены); pid-файлы в `.run/`, повторный start безопасен.
- `make stop` — остановка обоих по pid-файлам.
- `make status` — состояние процессов + `glamor status` + tail логов
  (`~/.glamor/glamord.log`, `.run/web.log`).
Проверено вживую: start → демон+web доступны, status показывает логи,
stop гасит оба.

## Что сделано по таскам

### T-14 — Экран рана (субагент, проверен)
Header (задача/ветка/состояние/токены, stop/resume), колонка стадий
(пульс running + 2 строки стрима в ноде, ✓/✗/⚠+resume_count, j/k),
панель [Стрим/Артефакты/Промпт/Метрики]: Streamdown, сворачиваемый
thinking, чипы tool_call/result, raw-спойлер, автокролл с прилипанием,
догон истории GET /events с дедупом. Без xyflow (разрешено ТЗ).

### T-15 — Чат гейтов + инбокс (субагент, проверен)
Лента гейты+системные события, approve/reject/answer/comment с
Idempotency-Key, оптимистичный UI + reconciliation, «уже резолвнуто»
при гонке клиентов, queue notes со статусами, Interrupt&Steer с confirm,
keyboard a/c, глобальный инбокс с live-бейджем (WS run_id=*).

### T-16 — Проекты и создание рана (субагент, проверен)
Список проектов live, добавление/настройки, экран проекта (табы пайплайнов
с localStorage, раны с фильтром), форма «Новая задача» (depth/ветка/
notify_tg/превью), dirty_checkout → файлы + force, run_locked → ветка.
Живой смоук: проект создан через API, UI отдаётся, ран создаётся/стопится.

### T-17 — Дефолтный пайплайн (я)
- `service/pipeline/`: промпты портированы из ai-pipeline (planner/coder/
  reviewer/fixer + depth-пресеты), артефакт-контракт вместо terminal/
  worker_done, запрет commit/push (D-32), verdict.json strict JSON.
- Шаблонизация {{task}}/{{artifact.spec.md}}/{{depth_instructions}}/
  {{verdict}}/…; неизвестный плейсхолдер — ошибка (fail fast).
- Spec: +loop{from,to,max_iters}, +final_gate, +prompt_template;
  артефакты в {run_dir}=~/.glamor/runs/<id> (ВНЕ чекаута).
- Движок: ReenterStageByKey (найденный баг: harness первой попытки из
  спеки), SkipStage, EnsureFinalGate (comment → ре-вход последней
  стадии), pending→failed (spawn failure), промпт каждой попытки —
  артефакт prompt-<stage>-<iter>.md.
- LoopHook: verdict approved → fixer skipped; changes_required →
  fixer (<max_iters) или эскалация-гейт с findings (гейт на fixer —
  ответ резюмит его); fixer → reviewer.
- e2e: полный прогон spec→approve→code→fix→review→final→succeeded и
  эскалация петли на fake-harness, с -race.

### T-18 — Tauri shell (Я; статус blocked)
Код полностью написан (Cargo.toml tauri2 + plugins, tauri.conf.json
externalBin, main.rs: single-instance, трей, watch с restart/backoff,
daemon_info invoke), Makefile: tauri-sidecar/tauri-dev/tauri-build.
**Блокер**: нет Rust toolchain на машине; не компилировалось
(UNVERIFIED). Нужно: `brew install rustup && rustup-init` →
`make tauri-dev`. Follow-up: мост gate.opened → Tauri notification в web/.

### T-19 — Telegram-адаптер (субагент, проверен)
Миграция (tg_chats/tg_state/tg_gate_messages/runs.tg_root_message_id),
BotClient (long polling, ретрай только транспорта, токен не в логах),
Adapter (poller+sender, rate limits), маппинг событий/гейтов (кнопки,
reply-ответы), /status /stop, reply→queue note, /start+код привязки,
POST /telegram/pair, оффлайн-сводка одним сообщением (D-72), D-73
молчание, дедуп update_id. 13 тестов зелёные с -race.

### T-20 — Редактор пайплайнов (субагент, проверен)
/pipelines/$id/edit (lazy-чанк): xyflow-канва (цепочка нод, loop-петля
i/max_iters, финальный гейт), инспектор ноды со всеми ручками, редактор
промпта с подсветкой плейсхолдеров, блокирующая валидация с подсветкой
на канве, версии (read-only старые, «взять за основу», сохранение =
новая версия), Import/Export YAML кнопки (T-21 UI).

### T-21 — Версии + YAML (я)
API: POST /pipelines, /pipelines/{id}/versions, /pipelines/import;
GET /pipelines/{id}/versions, /pipeline-versions/{vid}(/export).
Неизменяемость версий, ValidateSpec (loop-ссылки, гейты, плейсхолдеры),
YAML со стабильным порядком ключей, on_conflict new|version|fail.
Seed из embedded default.yaml; `pipelines/default.yaml` в корне —
симлинк (источник в backend/internal/service/pipeline/default.yaml,
регенерация GEN_YAML=1 … + golden-тест). Round-trip тесты, живой смоук
(import → default-2, new version → v2 parent=1).

## Как проверялось

- `make build && make test && make lint` — зелёные (12 пакетов с -race,
  lint 0 issues), `npm run build` + vitest 3/3 + oxlint 0, `make gen`
  идемпотентен.
- Живой смоук сервисов: make start → API/UI доступны, pipeline
  export/import/versions через curl, make stop/status.
- Каждая таска закрыта секцией «Итог» с acceptance-проверками.

## На что смотреть ревьюеру

1. **T-18 не собирался** (нет Rust) — единственный UNVERIFIED кусок;
   версии плагинов/иконка трея/нотификации — при первой сборке.
2. **Золотые файлы harness'ов** (T-07/08) всё ещё синтезированы по
   матрице — первый реальный ран на kimi/qwen покажет корректность
   парсеров (e2e-теги не написаны).
3. **Telegram**: нотификации только в primary-чат whitelist; дроп после
   3 ретраев сдвигает маркер доставки (trade-off, зафиксирован).
4. **Промпт-шаблоны в spec_json inline** — версия пайплайна несёт свои
   промпты (плюс: неизменяемость; минус: правка промпта = новая версия).
   `default.yaml` ↔ DefaultSpec/prompts держит golden-тест.
5. **Исполнитель M1** — линейная цепочка + одна петля + final gate;
   валидация спеки не проверяет «исполнимость» произвольной топологии
   (редактор ограничивает моделью; DAG — T-28).
6. **Фронт-бандл ~815 kB** (streamdown/shiki) — для локального UI ок,
   code-splitting при желании позже.
7. События `stage.resumed`/`stage.queued`/`run.branch_mismatch` есть в
   спеке EventKind, но payload-схемы stream.* описаны укрупнённо —
   фронт терпим к доп. полям.

## Не в scope (дальше по графу)

- T-18 acceptance (после установки Rust), e2e harness'ов против реальных
  CLI (первый живой ран), T-22 janitor, T-23 vendor-память (FTS5
  наполнение), T-24 метрики UI, T-25/26 адаптеры claude/codex/opencode,
  T-27..T-29 (ACP, fan-out DAG, causal memory).

---

## Дополнение (2026-08-17, позднее): T-18 разблокирован и собран

Пользователь установил rustup (brew). Починка окружения: keg-only формула
не даёт rustup-init и не линкует прокси → `rustup default stable` +
`export PATH="/opt/homebrew/opt/rustup/bin:$PATH"` в ~/.zshrc.
Первая сборка выявила и вылечила: отсутствующий sidecar-бинарь
(make tauri-sidecar), не-RGBA иконки (сгенерированы плейсхолдеры),
пути npm-команд (выполняются из корня приложения, не из src-tauri).
Итог: `tauri build` зелёный, `glamor.app` собран со sidecar-демоном.
UNVERIFIED остаётся только живое окно (трей-нотификации, restart демона
из shell, window-state) — требует GUI-прогона; фронт-мост
gate.opened → Tauri notification — follow-up.
