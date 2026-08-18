# Итог работы: T-22…T-29 + живые фиксы (для независимого review)

Дата: 2026-08-17 · Исполнитель: Kimi Code (+4 фоновых субагента: T-25, T-26,
T-27, UI-батчи ×3 — результат каждого проверен мной повторно).
Предыдущие части: docs/T-01-04, T-05-13, T-14-21 review-summary.md.
Таски: tasks/T-22..T-29 — все `done` (секции «Итог» в файлах).

## По таскам

### T-22 — Janitor-этапы
StageSpec kind=janitor: `commands` (shell, доверенная конфигурация — ядро
НЕ исполняет вывод LLM), `on_fail: fail_stage|warn`, `command_timeout_sec`.
Supervisor выполняет без harness (process group + таймаут), вывод →
stream.text + лог `{run_dir}/janitor.log` (артефакт janitor_log,
плейсхолдер {{artifact.janitor.log}}). Тесты: success/fail_stage/warn.

### T-23 — Vendor-память
Дельты этапов (`vendor-updates/<vendor>.md`) атомарно применяются:
локальная всегда, глобальная по `spec.memory_scope` (auto|gate);
глобальная память — git-репозиторий (авто-init, коммит на дельту).
FTS5 (`repository/vendorindex`, таблица из T-02): dirty-check по
содержимому, reindex при старте и после дельт; {{vendor_memory}} в
planner.md — FTS-выдержки по задаче (top-5, ~2k токенов). Промоушн
local→global, ручная правка, история (git log). API: /memory/tree|file|
history|promote. UI: раздел /memory.

### T-24 — Метрики
Usage накапливается инкрементально (atomic счётчики в stageProc) — сумма
usage-событий журнала == метрики этапа (тест). API: GET /runs/{id}/metrics
(per stage + totals + gate_wait_seconds — производное событий),
/runs/{id}/metrics.csv, /projects/{id}/metrics?period=. UI: таб метрик,
итоги в header, панель метрик проекта, экспорт CSV.

### T-25/26 — Адаптеры claude, codex, opencode (субагенты)
Все 5 harness-пакетов зелёные с -race. claude: --verbose + --bare +
--effort + --json-schema; api_retry → error.retry (признак жизни).
codex — только доки (UNVERIFIED, e2e отложен до установки). opencode:
tool_use только completed → start=end пара; баги #26855/#40544
задокументированы. Золотые файлы синтезированы по матрице. Матрица
дополнена блоками «Реализация».

### T-27 — ACP spike (субагент)
Живой прогон `kimi acp`: NDJSON-фрейминг (не Content-Length!), handshake
~0.6s, prompt round-trip работает, session/cancel = steer без убийства,
идемпотентности в протоколе нет. **ADR-002 (proposed): гибрид, дефолт —
вариант А** (вопросы-артефакты): неполное покрытие harness'ов + конфликт
с D-15. backend/cmd/acp-spike — самодостаточный спайк.

### T-28 — Fan-out (read-only ветки)
Spec: parallel_group/read_only + parallel_groups[{name, on_failure}].
Движок: члены группы стартуют по тикам (пул параллелит), join ждёт всех,
fail_fast/wait_all политики. Валидация: parallel-этап обязан read_only.
**ADR-003**: пишущие этапы линейны (D-30/D-32); динамический fan-out —
нет. Тесты: progression/fail_fast/wait_all. UI: группы в редакторе.

### T-29 — Causal memory (Lessons)
Таблица lessons + gate kind lesson_review (миграция с пересозданием
gates CHECK). Distill — линейный этап дефолтного пайплайна (effort low,
{{gate_answers}} = ответы на гейтах + заметки; {{rejected_lessons}} =
dedup-подсказка). Гейт «Сохранить урок?» открывается только при наличии
карточек; approve→project / comment→global / answer→+причина /
reject→rejected (dedup). Инъекция {{lessons}} в промпты (FTS, confirmed,
~2k токенов, applied_count++). API /lessons. UI-вкладка в /memory.
Relapse-автодетекция отложена (счётчик в схеме есть).

## Живые фиксы по фидбеку пользователя (между тасками)

- **CORS**: UI показывал «демон недоступен» — демон не отдавал
  Access-Control-Allow-Origin; добавлен corsMiddleware (локальные origins,
  preflight до auth). Тест + живой прогон.
- **Question-flow сломан на дефолтном пайплайне** (найдено на первом
  живом ране пользователя!): планировщик задал вопросы и вышел без
  spec.md → классификатор ронял стадию «artifact missing». Фикс:
  непустой questions.md при exit 0 — валидный исход попытки (D-20) →
  succeeded → гейт. Регрессионный тест.
- **Resume failed-рана** не работал (мгновенный повторный fail):
  ResumeRun теперь делает ReenterStage упавшей стадии до смены состояния.
  Живой ран пользователя доехал от failed до plan_approval по Resume.
- **Вопросы/саммари — в тексте гейта** (чат): questions.md и начало
  spec.md кладутся в gate.Question (усечение 3КБ/1.5КБ).
- **draft→stopped** разрешён (ADR-001 доп. 2026-08-17) — отмена рана
  до старта; нашлось при тесте удаления проекта.
- **DELETE /projects/{id}** — каскад (вся история), активные раны → 400.
- **ide_command=goland** дефолт (бэкенд + форма).
- **Settings API**: GET/PUT /settings{,/telegram,/supervisor}: TG-токен
  валидируется через getMe, hot-apply адаптера (менеджер перезапуска в
  main) и конфига supervisor (SetConfig с RWMutex), персист в config.yaml.
- **GET /fs/browse** — серверный файл-пикер (браузер не умеет абс. пути).
- UI (агент): баг /pipelines/N/edit (versions undefined → защита +
  догрузка), баннер причины падения рана, экран /settings, удаление
  проекта, пикер папки (Tauri-нативный + серверный фолбэк), вкладка
  «Уроки», навигация «← К задачам/проекту» + крошки + u/Esc, звуковые
  уведомления (success/error/gate/warning — разные тоны, Web Audio,
  live-only, mute в шапке и настройках).

## Проверки

- 19 Go-пакетов с -race зелёные, lint 0 issues, make gen идемпотентен.
- web: build чист, 59 vitest-тестов, oxlint 0 (57 файлов).
- Живой смоук: сервисы стартуют, settings API отдаёт конфиг, вопросы
  приходят в чат текстом, ран пользователя прошёл plan_approval.

## Риски / на что смотреть ревьюеру

1. Золотые файлы ВСЕХ harness'ов синтезированы по матрице — первый живой
   ран на kimi показал: формат парсится, сессия резюмится, вопросы
   работают. Но e2e-каркасы не прогонялись целиком (живой пайплайн до
   succeeded на реальном CLI — следующий шаг пользователя).
2. codex-адаптер полностью UNVERIFIED (CLI не установлен).
3. Distill-этап линейный — на failed-ранах уроки не извлекаются
   (post-mortem distill — кандидат на доработку).
4. Relapse-детекция уроков отложена (счётчик есть, эвристики нет).
5. TG-адаптер не прогонялся против реального Bot API (моки; getMe-
   валидация токена в настройках — реальный вызов).
6. Web Audio звуки не проверены на слух (нет браузера в сессии).
7. Настройки пишутся в config.yaml в рантайме — конкурентная правка
   файла руками перезапишется (локальный демон, приемлемо).
