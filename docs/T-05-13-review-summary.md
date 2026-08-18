# Итог работы: T-05…T-13 (для независимого review)

Дата: 2026-08-16 · Исполнитель: Kimi Code (+3 фоновых субагента: T-07, T-08,
T-13 — их результат проверен мной повторно) · Репозиторий:
`github.com/fableFM/glamor` (Go-модуль в `backend/`, фронт в `web/`).
Таски: `tasks/T-05..T-13-*.md` (все `done`, секции «Итог» в каждой).
Предыдущая часть (T-01..T-04): `docs/T-01-04-review-summary.md`.

## Что сделано

### T-05 — OpenAPI-контракт + REST API
- `api/openapi.yaml` — полный REST v1 (projects/pipelines/runs/gates/
  stages/events/system + /ws). Единый `Error{code,message,details?}` с
  default-ответом на всех эндпоинтах.
- Кодогенерация oapi-codegen (models + strict-server + std-http) →
  `backend/internal/controller/http/genapi/api.gen.go`. Хендлеры поверх
  StrictServerInterface, ручного кода минимум (mappers/errors/server).
- Идемпотентность (D-12): POST /runs повтор ключа → 200 первым раном;
  lock ветки через частичный UNIQUE-индекс (миграция 20260816120000) →
  409 run_locked; resolve гейта повтором тем же резолюшном → 200
  already_resolved=true, другим → 409 gate_already_resolved.
- Auth: Bearer на всё, кроме /healthz; /ws — query-токен.
- Usecase: CreateRun (slug ветки, preflight-хук), StopRun
  (stop_requested_by=user), ResumeRun (failed→running — **ADR-001
  дополнен**), ResolveGateAPI, InterruptStageSteer, CreateNote.
- Таблица `notes` (queue note/steer, D-22) + репозиторий.

### T-06 — Harness-интерфейс
- `backend/internal/harness/`: Harness (BuildCommand→CommandSpec{Argv,
  Env, Stdin}, ParseStream, ExtractSessionID), Capabilities (+UsageSource,
  +Thinking), ArtifactSpec (D-13), нормализованные события по
  рекомендациям capability-матрицы (session.init, assistant.*,
  tool_call.*, usage, result, error(.retry), raw), JournalKind, Registry
  (overrides путей, CheckBinaries).

### T-07/T-08 — адаптеры kimi и qwen (субагенты, проверено)
- kimi: `-p <prompt> --output-format stream-json` (БЕЗ --yolo), effort
  env, resume `-S <id> -p`, длинный промпт → временный файл; Thinking=
  false (идёт в stderr), UsageSource=wirefile; не-JSON строки → raw.
- qwen: `-p -o stream-json --approval-mode yolo`, `-r <id>` со всеми
  флагами заново, `--json-schema` per-run, короткий -p + stdin для
  длинных промптов, QWEN_CODE_UNATTENDED_RETRY=1; EffortLevels=nil.
- Золотые файлы СИНТЕЗИРОВАНЫ по матрице (реальный CLI не запускался —
  риск для e2e отмечен в тасках). Юнит-тесты зелёные с -race.

### T-09 — Supervisor этапа
- `internal/service/supervisor/`: tick-контур (completions → due resumes →
  steers → reconcile → NextAction), spawn в process group, stdout→
  ParseStream→StreamBatcher + сырой лог в runs/<id>/stage-*.log
  (receipts), session id из стрима, watchdog (stall/timeout/K=3
  retriable), классификация (D-13/14), auto-resume с backoff, пул
  процессов (семафор + stage.queued), Drain.
- Найденные и исправленные гонки: scanners join до cmd.Wait (os/exec
  закрывает pipes); двойной resume (backoff-очередь vs NextAction).

### T-10 — Git-интеграция
- `internal/gitx/` (git CLI, таймауты): Preflight (dirty → 400
  dirty_checkout + details.files, force-override), SuggestBranch
  (-2/-3 при коллизии), PrepareBranch, CheckBranchMismatch (перед каждым
  этапом), GetDiffStat. Lock ветки — в БД (T-05). Политика «никогда не
  commit/push» зафиксирована в ADR-001.

### T-11 — Гейты и диалоговый протокол
- D-20: StageSpec.+questions_path/gate_after; supervisor после succeeded:
  questions.md непуст → гейт question; иначе gate_after →
  plan_approval/final_review.
- Machine.ReenterStage — диалоговый ре-вход (новая попытка БЕЗ
  resume_count++); ResolveGateAPI: answer/comment с текстом → ре-вход с
  steer-заметкой.
- D-22: processSteers (interrupted by=user + steer → ре-вход),
  consumeNotes (заметки → промпт ближайшей попытки → consumed).
- **Багфикс**: NextAction не предлагает auto-resume для interrupted со
  stop_requested_by (D-14) — иначе перехватывал steer (поймано тестом).

### T-12 — Lifecycle демона
- `internal/daemon/`: pid-lock (второй инстанс → «already running»,
  stale → снимается), token (генерация, 600), daemon.json, RotateWriter.
- main: lock → config (создаётся при первом запуске) → token → лог
  stderr+файл → миграции → startup recovery (сироты → interrupted;
  daemon-флаг очищается → auto-resume) → listeners → daemon.json →
  graceful: draining (POST /runs → 503) → Drain (by=daemon + kill grace
  15s + checkpoint interrupted) → shutdown → cleanup.
- CLI `glamor`: status / runs / stop.

### T-13 — Web scaffold (субагент, проверено)
- web/: TanStack Router, zustand, Tailwind v4 (тёмная), lucide.
- api/client.ts (typed, без any, ApiError), lib/events.ts (WS-клиент:
  backoff, атомарный replay с bulk-apply на synced, дедуп по id),
  сторы (projects/runs/runDetails/streams cap 2000/connection),
  ConnectionBanner, роуты-заглушки. vitest 3/3, npm build чист.

## Как проверялось

- `make build && make test && make lint` — зелёные (10 Go-пакетов с
  -race, lint 0 issues), `npm run build` + `npx vitest run` зелёные,
  `make gen` идемпотентен.
- Живой смоук демона: создание ~/.glamor (config/token 600/daemon.json/
  lock/log), /healthz, /version (harnesses kimi+qwen обнаружены),
  `glamor status`, второй инстанс отклонён, SIGTERM → graceful shutdown.
- Acceptance-таски покрыты тестами; особо: restart-посреди-рана и
  graceful-drain-recovery (T-12), диалоговые e2e (T-11), все ветки
  классификации (T-09), WS-реконнект (T-04/13).

## На что смотреть ревьюеру (риски/спорные места)

1. **Золотые файлы harness'ов синтезированы по матрице**, реальный CLI
   не запускался (экономия API-вызовов). e2e-теги не написаны — при
   первом реальном ране (T-17) возможна корректировка парсеров. Это
   главный риск M1.
2. **MaxOpenConns(1)** — все чтения/записи через одно соединение;
   supervisor держит несколько горутин (стрим-записи батчами + тики).
   Дедлоков не выявлено (тесты с -race и параллельными CAS), но при
   нагрузке следить.
3. **WithTx(ctx, func(ctx, tx))** — ctx из аргументов fn обязателен для
   публикации событий (Journal). Молчаливая потеря рассылки при ошибке
   использования — кандидат на lint-правило/проверку в ревью новых вызовов.
4. **Drain не дожидается backoff-очередей** (resumeAfter) — прерванные с
  отложенным resume стадии при рестарте резюмятся сразу (backoff
  теряется). Задокументировано в T-09.
5. **qwen --approval-mode yolo** — harness работает с полными правами в
  чекауте пользователя; «не коммитить/не пушить» — конвенция промптов
  (ADR-001), технически не форсится. Осознанный риск из D-30..32.
6. **ReenterStage эмитит 2 события** (state_changed + stage.resumed) —
  избыточность для аудита, не баг.
7. **Token в query-параметре /ws** — попадает в access-логи проксей, если
  появятся; localhost-демон, осознанно (D-08).
8. **web/: backoff константы** (500ms×2 cap 8s) отличаются от примера в
   ТЗ (cap 30s) — трактовка субагента, константы вынесены.
9. Пре-существующее состояние worktree (staged-удаления pkg/harness/codex,
   удалённый корневой go.mod после переезда в backend/) — сохранено,
   не моё.

## Не входило в scope (следующие таски)

- T-14/15/16 (экраны UI), T-17 (дефолтный пайплайн plan→code→review→fix
  с промптами и verdict-протоколом), T-18 (Tauri), e2e-теги harness'ов
  против реальных CLI (T-07/08 acceptance, не в CI).
- M2+: TG-адаптер, редактор пайплайнов, vendor-память, метрики.

---

## Дополнение (2026-08-17): слойная реорганизация (параллельный рефактор)

После сдачи T-05..T-13 независимый агент провёл рефактор по project-скиллу
`.agents/skills/glamor-architecture` (закреплённому операционной формой
D-80, уточнение 2026-08-16 в overview): **слой usecase упразднён**;
контроллер не импортирует repository; self-DI запрещён.

Текущая карта backend:
- `internal/service/runsmachine` — стейт-машина (ex `usecase/runs`);
- `internal/service/runsapi` — API-сценарии ранов (CreateRun/StopRun/…);
- `internal/service/catalog` — read-агрегации для контроллера;
- `internal/service/supervisor`, `internal/service/pipeline` — как было;
- контроллер получает сервисы через интерфейсы, объявленные у потребителя
  (Deps.Machine — интерфейс, не конкретный тип);
- `events.Journal` собирается из eventsRepo+TxManager+Hub (без self-DI);
- все репозитории конструируются один раз в main и раздаются готовыми.

Проверено после рефактора: `make build/test/lint` зелёные, все тесты с
-race проходят (включая supervisor/pipeline e2e). Весь новый код
(T-17/T-21/T-19/T-20) пишется уже в этой структуре.
