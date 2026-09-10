# Анализ T-30: эволюция системы уроков (handoff для агента-исполнителя)

Дата: 2026-08-19 · Назначение: самодостаточный анализ для глубокого
понимания задачи T-30 без чтения переписки. Контракт таски —
`tasks/T-30-lessons-evolution.md`, решения — D-52/D-81 в
`tasks/00-overview.md`. Этот документ объясняет **почему** дизайн такой и
**где** лежит каждая затрагиваемая механика.

---

## 1. Как уроки работают сейчас (карта кода, T-29 / D-52)

### 1.1 Пайплайн и distill-этап

- Дефолтный пайплайн `planner → coder → reviewer → fixer (loop ≤4) →
  distill → final_review`: `backend/internal/service/pipeline/default.yaml:290-298`
  — этап `distill`, kind `llm-stage`, harness kimi, модель kimi-code/k3,
  effort low, артефакт `{run_dir}/lessons.md` (required), `gate_after:
  lesson_review`.
- Шаблон материализуется в spec пайплайна при создании версии:
  `backend/internal/service/pipeline/pipeline.go:120-123` (distill собирается
  из `prompts/distill.md` + `gate_after: lesson_review`).
- Spec пайплайна (`pipelines.spec_json`, версии неизменяемы, T-21):
  `backend/internal/service/runsmachine/spec.go:11-29` — `Stages`,
  `Loop`, `FinalGate`, `ReviewPolicy`, `MemoryScope`, `ParallelGroups`.
  **Прецедент плоских policy-полей** (`review_policy`, `memory_scope`)
  важен для дизайна настройки «формировать уроки» (см. §4.7).
- Distill — рядовой этап в линейной цепочке успеха: стейт-машина
  (`backend/internal/service/runsmachine/transitions.go:34,48`) ведёт ран к
  `succeeded` только через цепочку succeeded-этапов; на failed-ране distill
  не запускается.

### 1.2 Вход distill: сигналы взаимодействия

- Промпт `backend/internal/service/pipeline/prompts/distill.md`: входы
  `{{task}}`, `{{gate_answers}}`, `{{rejected_lessons}}`. Правило: сигналов
  нет или они тривиальны → `NO_LESSONS`.
- Рендер плейсхолдеров: `backend/internal/service/pipeline/render.go:103-111`
  (`lessons`, `gate_answers`, `rejected_lessons`), реестр известных
  плейсхолдеров — `validate.go:126-132`.
- Источник `{{gate_answers}}` — `LaunchContext.LessonSignals`
  (`supervisor.go:70-72`), должен заполняться из
  `Supervisor.lessonSignals()` (`supervisor/gates.go:200-225`): ответы
  пользователя на резолвнутых гейтах + заметки рана.

### 1.3 ⚠ P0: вход distill фактически не подключён

`Supervisor.lessonSignals()` **нигде не вызывается** (мёртвый код).
`LaunchContext` строится в `supervisor/stageproc.go:89-92` — поле
`LessonSignals` не заполняется (заполняются Run/Stage/StageSpec/IsResume/
RunDir/ProjectPath, далее MaxIterations и queue notes). Поиск по всему
`backend/`: присваиваний `LessonSignals` нет.

Следствие: `{{gate_answers}}` всегда рендерится как «(сигналов нет)»
(render.go:106-108) → distill по своему же правилу обязан писать
`NO_LESSONS`. **В проде уроки сейчас не формируются вовсе** (кроме
гипотетической галлюцинации LLM на пустом входе). T-29 сдан с тестами на
парсер/гейт/инъекцию, но end-to-end связка сигналов не была покрыта —
это корневая причина, почему баг прошёл незамеченным. T-30 чинит это
вместе с расширением входа (§4.1), а не отдельным хотфиксом: трейс
поведения подменяет собой и сломанный, и узкий канал сигналов.

### 1.4 Гейт lesson_review и применение решения

- Вид гейта зарегистрирован: `pipeline/validate.go:117-124`
  (`GateKindLessonReview`), CHECK в БД — миграция
  `backend/migrations/20260817140000_lessons.go` (пересоздание `gates`).
- Резолв: `runsapi/api.go:395-401` — для `lesson_review` вызывается
  `LessonsFinalizer` (интерфейс `api.go:46-55`), ре-входа этапа нет.
- Эффекты: `lessons/finalize.go:29-85` — approve → scope=project,
  confirmed; comment → global, confirmed; answer → project + ответ
  дописывается в секцию «Причина»; reject → rejected (хранится для
  дедупа). Карточки парсятся из черновика `lessons.md` гейта.
- Гейт открывается только при наличии карточек (NO_LESSONS не донимает) —
  см. итог T-29.

### 1.5 Хранение

- Таблица `lessons`: миграция `20260817140000_lessons.go:19-34` — id,
  title, scope (global|project), status (proposed|confirmed|rejected|
  superseded), project_id, path, triggers_json, run_id, stage_key,
  **applied_count, relapse_count**, created_at, updated_at.
- Файлы: markdown с frontmatter, двухуровнево `~/.glamor/lessons/` +
  `<repo>/.glamor/lessons/` (`lessons.go:46-51,143-166`), атомарная запись
  через tmp+rename.
- FTS: общий индекс с vendor-памятью — `repository/vendorindex/
  vendorindex.go:23` (Upsert), `:58` (Search) (механика T-23).
- Дедуп: урок с title отклонённого ранее не сохраняется
  (`lessons.go:133-141`, `IsDuplicate:309-311`) — точное совпадение
  заголовка, только для rejected.

### 1.6 Инъекция в промпты

- Плейсхолдер `{{lessons}}` есть **только в planner.md** (default.yaml:70-74).
- `RelevantLessons` (`lessons.go:196-228`): FTS5-запрос из слов задачи
  (`ftsQueryFromText:286-303` — слова ≥3 символов, до 12 термов через OR),
  `index.Search(ctx, query, 10)`, фильтр пути `*/lessons/*`, только
  confirmed, токен-бюджет `LessonTokenBudget = 2000*4` символов
  (константа, `lessons.go:31`), `applied_count++` при каждой инъекции
  (`:225`).
- `findByPath` (`lessons.go:231-242`) — линейный скан всех уроков на
  каждый FTS-хит (O(N·K), слабость производительности заодно).

### 1.7 UI и API

- REST: `backend/internal/controller/http/lessons.go` (list/get/set status/
  read content), схема — `api/openapi.yaml` (lessons-эндпоинты, поля
  applied/relapse уже в `schema.d.ts`).
- Web: `web/src/components/memory/LessonsPanel.tsx` (список, статусы,
  фильтры), страница `web/src/pages/MemoryPage.tsx`, уведомления о гейтах —
  `web/src/components/InboxBell.tsx`.

### 1.8 Мёртвые механики (задекларированы, не работают)

- `relapse_count`: колонка есть, `IncrementRelapse`
  (`repository/lessons/query.go:162-168`) есть, **вызывающих нет**. В итоге
  T-29 честно задокументировано: «relapse-автодетекция отложена, эвристика
  связывания finding↔lesson — за рамками M3».
- `StatusSuperseded` (`lessons.go:27`): константа и CHECK есть, никто не
  выставляет — supersede-цепочек нет.
- Счётчик `applied_count` растёт при **инъекции**, а не при успешном
  применении — метрика «помог ли урок» отсутствует.

---

## 2. Проблемы (каждая с доказательством)

| # | Проблема | Доказательства |
|---|----------|----------------|
| P0 | Вход distill не подключён — уроки не формируются вообще | §1.3: gates.go:202 мёртвый, stageproc.go:89 без LessonSignals |
| P1 | Вход узкий: только пользовательские сигналы; поведение рана (findings, fix-петля, падения проверок) не используется | distill.md входы; gates.go:200-225 |
| P2 | Карточки только добавляются; нет уточнения/вытеснения/связей; distill не видит confirmed-уроки | lessons.go:133-141 (дедуп только rejected по title); StatusSuperseded мёртвый |
| P3 | Нет петли качества: relapse/outcome не собираются | query.go:162 без вызывающих; applied_count = инъекции, не успехи |
| P4 | Ретрив примитивный: FTS top-10, без важности/свежести, инъекция только в planner | lessons.go:196-228, default.yaml:72 |
| P5 | Нет vendor-уроков: знание о поведении конкретной версии вендора не извлекается и не деградирует при смене версии | T-23 хранит документацию, не правила; lessons без полей vendor |
| P6 | Distill необязателен (удаляется редактором) и не запускается на провальных ранах | pipeline.go:120-123; transitions.go:34,48 |

---

## 3. Исследовательская база

Полные ссылки — в §7. Здесь: что именно берём из каждой работы и что
осознанно **не** берём.

### 3.1 ExpeL — опыт из собственных провалов и успехов (→ P1)

[ExpeL: LLM Agents Are Experiential Learners](https://arxiv.org/abs/2308.10144)
(AAAI 2024). Агент без файнтюна собирает трейсы на пачке задач, извлекает
из них правила на естественном языке (insights) и инжектит их в промпт.
Ключевые механики: (а) источник опыта — **и успешные, и провальные**
траектории; (б) сравнение похожих успех/провал пар выделяет решающий
фактор; (в) извлечённые правила — текст, не веса.

Что берём: вход distill = полный трейс рана, fix-итерации — готовые пары
«провал (verdict changes_required + findings) → успех (approve на следующей
итерации)». У нас пара даже богаче, чем у ExpeL: провал машиночитаем
(verdict.json), не нужен self-judge.

Что не берём: обучение на пачке «тренировочных» задач — у нас нет offline
батча, каждый ран боевой; подтверждение человеком вместо автоматического
принятия инсайтов.

### 3.2 ReasoningBank — провал как первоклассный сигнал (→ P1, P3)

[ReasoningBank: Scaling Agent Self-Evolving with Reasoning Memory](https://arxiv.org/abs/2509.25140)
(Google, сен 2025). Дистилляция обобщённых стратегий рассуждений из
self-judged успешного **и неуспешного** опыта; память как стратегии, а не
сырые трейсы; плюс memory-aware test-time scaling.

Что берём: findings ревьюера (blocking/major) и упавшие проверки —
self-judged провалы, уже структурированные; урок должен содержать
обобщённую стратегию («когда X — делай Y»), а не пересказ рана (у нас это
уже в формате карточки: триггер → правило).

Что не берём: их scaling-схему (MaTTs) — у нас один проход на ран, нет
RL-петли.

### 3.3 Reflexion — вербальная рефлексия над провалом (→ P1)

[Reflexion: Language Agents with Verbal Reinforcement Learning](https://arxiv.org/abs/2303.11366)
(NeurIPS 2023). После провала агент пишет себе текстовую рефлексию («что
пошло не так, что делать иначе») и использует её в следующей попытке.

Что берём: per-finding рефлексия как промежуточный слой в distill —
каждый blocking-finding превращается в кандидат-урок с явной причиной
(корневая, из `required_fix`/`forbidden_fix` — наши findings уже содержат
поля, которые Reflexion генерирует с нуля).

Что не берём: эпизодическую память «на одну попытку» — наши уроки
персистентны и подтверждаются человеком.

### 3.4 ACE — эволюция контекста дельтами (→ P2)

[Agentic Context Engineering: Evolving Contexts for Self-Improving Language Models](https://arxiv.org/abs/2510.04618)
(Stanford/SambaNova, окт 2025). Контекст как эволюционирующий плейбук:
генератор → рефлектор → куратор; **инкрементальные дельта-апдейты**
вместо переписывания целиком; grow-and-refine защищает от «context
collapse» (когда переписывание стирает накопленное).

Что берём: операции distill как дельты над существующими карточками —
NEW / REFINE(id) / SUPERSEDE(id) / LINK(id1,id2). Distill обязан видеть
confirmed-уроки (раньше видел только rejected-заголовки). Гейт показывает
diff, человек — куратор последней инстанции.

Что не берём: автономное накопление без человека; их «плейбук» живёт в
контексте, наши карточки — файлы+БД (D-52).

### 3.5 A-MEM — связи и эволюция записей (→ P2)

[A-MEM: Agentic Memory for LLM Agents](https://arxiv.org/abs/2502.12110)
(NeurIPS 2025). Zettelkasten для агентов: атомарные заметки со
структурированными атрибутами, автоматические **связи** между заметками,
эволюция старых записей при появлении новых.

Что берём: `related`-связи между уроками, supersede-цепочки (старая
запись не удаляется, а вытесняется со ссылкой), консолидация дублей как
операция над графом уроков.

Что не берём: эмбеддинги и векторный поиск (нарушают локальность D-52;
у нас FTS5) и LLM-на-каждую-запись эволюцию без гейта.

### 3.6 Generative Agents — скоринг ретрива (→ P4)

[Generative Agents: Interactive Simulacra of Human Behavior](https://arxiv.org/abs/2304.03442)
(UIST 2023). Каноническая формула извлечения из памяти:
`score = w1·recency + w2·importance + w3·relevance` (exp-decay по времени,
LLM-оценка важности 1-10, косинусная близость эмбеддингов).

Что берём: формулу скоринга с нашими детерминированными компонентами:
relevance = нормированный ранг FTS5 (вместо эмбеддингов), importance =
производная от источника и статистики (applied_success ↑, relapse ↓),
recency = exp-decay от last_applied_at/created_at.

Что не берём: LLM-оценку важности на каждую запись (дорого и
недетерминированно — считаем из счётчиков) и эмбеддинги.

### 3.7 Dynamic Cheatsheet — outcome-трекинг памяти (→ P3)

[Dynamic Cheatsheet: Test-Time Learning with Adaptive Memory](https://arxiv.org/abs/2504.07952)
(2025). Кумулятивная «шпаргалка» обновляется после каждой задачи; память
оценивается по **вкладу в результат**, а не по факту извлечения.

Что берём: различение «урок инжектирован» vs «урок помог»: outcome-сигнал
(approve без blocking в области урока → applied_success++; повторный
finding в области → relapse++). Здоровье урока = applied/relapse —
основа importance и UI-фильтра «требуют внимания».

Что не берём: авто-переписывание памяти моделью без человека.

### 3.8 Словарь и рамка

- [CoALA: Cognitive Architectures for Language Agents](https://arxiv.org/abs/2309.02427)
  (TMLR 2024): наши behavior-уроки = semantic (правила) + episodic
  (ситуация/симптом) память; vendor-уроки = semantic память о внешних
  контрактах; procedural-память (скиллы действий) — осознанно вне T-30.
- [A Survey on the Memory Mechanism of LLM-based Agents](https://arxiv.org/abs/2404.13501)
  (ACM TOIS 2025): операции write/management/read как ось дизайна; T-30
  закрывает все три (трейс+distill = write, консолидация = management,
  скоринг-ретрив = read).

### 3.9 Принципиально отвергнуто

Векторные БД/эмбеддинги, внешние memory-фреймворки (mem0, Zep/Graphiti,
Letta) — противоречат локальности и файловой модели (D-52, подтверждено
D-81). Автоматическое подтверждение уроков — никогда (D-52 неизменен).

---

## 4. Дизайн-решения T-30 (проблема → решение → источник → точки изменений)

### 4.1 BehaviorTrace — детерминированный трейс поведения рана (P0, P1)

Решение: ядро (без LLM) собирает `run_facts.json` в run_dir перед
distill'ом. Источники фактов:

- гейты и заметки — то, что должен был собирать `lessonSignals`
  (gates.go:200-225) — чинит P0;
- findings ревьюера: парсинг `verdict.json` каждой итерации (поля severity,
  file/line, required_fix, forbidden_fix — схема в default.yaml:210-229);
- fix-петля: какие findings закрыты на какой итерации, DISPUTED-споры
  (handoff.md фиксера), счётчик итераций (`Loop.MaxIters`, spec.go:39-44);
- упавшие проверки: логи этапов `runs/<id>/stage-<key>-<iter>.log`
  (stageproc.go:142-143) + exit codes (stageproc.go:332-334);
- исход: финальный verdict, approve/reject гейтов, длительности этапов
  (метрики T-24).

Основание: ExpeL §3.1, ReasoningBank §3.2, Reflexion §3.3. Трейс — факт
(подтверждения не требует); уроки из него — требуют.

Точки изменений: новый сборщик в `service/lessons` (или `service/supervisor`
— решить по владению данными: гейты/notes/logs у supervisor),
`LaunchContext.LessonSignals` → заменяется/дополняется трейсом,
`stageproc.go:89` — подключение (тем самым закрывается и P0).

### 4.2 Distill v2 — операции над карточками (P2)

Вход: `{{behavior_trace}}` + `{{existing_lessons}}` (id+title+триггеры
confirmed — **новое**) + `{{rejected_lessons}}`. Выход: операции
NEW / REFINE(id) / SUPERSEDE(id) / LINK(id1,id2) / QUESTION / NO_LESSONS.
Основание: ACE §3.4 (дельты), A-MEM §3.5 (связи, эволюция).

Гейт `lesson_review` показывает REFINE/SUPERSEDE с diff'ом; резолв —
per-card (принять часть операций). Применение атомарно: файл + БД +
FTS-реиндекс одной tx (по D-10..D-16 — событие в том же tx).

Точки изменений: `prompts/distill.md`, `render.go` (новые плейсхолдеры +
реестр validate.go:126-132), `ParseCards` → парсер операций (устойчивость
к мусору сохранить), `finalize.go` (применение операций),
`runsapi/api.go:397` (per-card resolve — возможно расширение
`LessonsFinalizer`).

### 4.3 Vendor-уроки (P5)

Формат карточки (kind=vendor): `vendor` / `vendor_version` (точная, из
go.mod/lockfile — не «latest») / `area` / секция «Урок» / «Доказательства»
+ `source` (URL/путь). Точка захвата: ресёрч-шаг planner'а пишет черновики
в `{run_dir}/vendor-lessons.md`; distill добирает из spec.md; тот же гейт.

Хранение: те же таблица/файлы + колонки kind/vendor/vendor_version/area;
каталог `~/.glamor/lessons/vendor/<name>/`. Деградация: при старте рана
сверка vendor_version с lockfile → расхождение = статус `outdated`,
инъекция с префиксом-предупреждением (не удаляем).

Инъекция: секция «Уроки по вендорам» в planner (отдельно от
behavior-уроков), триггер — упоминание вендора в задаче (FTS).

Точки изменений: миграция (§4.8), `prompts/planner.md` (инструкция +
плейсхолдер), `lessons.go` (SaveCard/RelevantLessons по kind),
`finalize.go`, новый компонент сверки версий (читатель go.mod —
переиспользовать существующий парсер, если есть в T-23; иначе минимальный).

### 4.4 Консолидация (P2)

Distill-операции — основной механизм (§4.2). Плюс ручной prune из UI:
кандидаты в дубли (overlap триггеров), superseded старше N дней,
relapse ≥ applied. Никакой авто-консолидации.

Точки: `LessonsPanel.tsx` (вкладка «Консолидация»), `controller/http/
lessons.go` (эндпоинт кандидатов), `repository/lessons` (запросы).

### 4.5 Ретрив v2 — скоринг (P4)

`score = w_rel·fts_rank + w_imp·importance + w_rec·recency_decay`
(Generative Agents §3.6, детерминированные компоненты). importance из
счётчиков (источник: answer/comment > auto; applied_success ↑; relapse ↓),
хранится в колонке, пересчёт при изменении счётчиков. Инъекция и в
coder.md, не только planner. Бюджет из константы (`lessons.go:31`) → в
конфиг демона.

Точки: `lessons.go:196-228` (переписать RelevantLessons), конфиг демона,
`prompts/coder.md`, `render.go`. Заодно: `findByPath` O(N·K) → выборка по
path из БД.

### 4.6 Петля качества (P3)

Relapse: после этапа reviewer, если blocking/major finding FTS-матчится с
телом/триггерами урока, **инжектированного в этот ран**, →
`IncrementRelapse` + событие в журнал + UI-флаг. Outcome: финальный
approve без blocking в области инжектированных уроков →
`applied_success_count++`. Основание: Dynamic Cheatsheet §3.7. Эвристика
может ошибаться → только счётчики и подсветка, статус не меняется.

Точки: supervisor (хук после reviewer / после финального гейта),
`repository/lessons/query.go`, `LessonsPanel.tsx` (здоровье уроков).
Предусловие: ядро должно знать, какие уроки были инжектированы в ран —
сохранять список id в контекст рана (сейчас нигде не фиксируется!).

### 4.7 Обязательный distill — настройка пайплайна (P6)

Плоское поле в `Spec` по прецеденту `review_policy`/`memory_scope`
(spec.go:19-25): `lessons` (`"on"`|`"off"`; дефолтный пайплайн — on).
Версионируется в `spec_json` (T-21). UI: чекбокс «Формировать уроки» в
редакторе (T-20).

Семантика ядра:
- `on` + нет distill-этапа → supervisor детерминированно дописывает
  встроенный distill (prompts/distill.md, lessons.md, gate_after
  lesson_review) перед финальным гейтом; пользовательский distill-этап
  уважается (гарантия = «хотя бы один», не «наш шаблон»);
- `off` → distill пропускается (событие skip в журнале) даже при наличии
  ноды в YAML; BehaviorTrace не собирается;
- `on` → distill и на терминальных неуспешных исходах (failed,
  escalation-rejected): хук supervisor'а на терминальные переходы
  (`machine.go:180,197` isTerminalRunState; `transitions.go:34`);
  тривиальные трейсы (падение harness) отсекает правило NO_LESSONS.

Точки: `runsmachine/spec.go`, `validate.go`, supervisor (инъекция этапа в
план + терминальный хук), `pipeline.go:120-123` (встроенный distill как
fallback), UI-редактор пайплайнов, openapi (если чекбокс идёт через API).

### 4.8 Модель данных (миграция, goose Go-файлом по D-05)

`ALTER TABLE lessons` (+ пересоздание для CHECK статусов, прецедент —
миграция T-29): `kind ('behavior'|'vendor')`, `vendor`, `vendor_version`,
`area`, `importance REAL DEFAULT 0.5`, `applied_success_count`,
`last_applied_at`, `superseded_by REFERENCES lessons(id)`,
`related_json DEFAULT '[]'`, статус `outdated`. Обратная совместимость:
существующие → kind=behavior, дефолты.

---

## 5. Карта изменений по файлам

**Backend (новое):**
- `internal/service/lessons/trace.go` — BehaviorTrace-сборщик;
- `internal/service/lessons/operations.go` — парсер и применение операций
  NEW/REFINE/SUPERSEDE/LINK;
- `internal/service/lessons/scoring.go` — ретрив-скоринг;
- `internal/service/lessons/vendor.go` — vendor-уроки + сверка версий;
- `internal/service/lessons/quality.go` — relapse/outcome эвристики;
- `migrations/<ts>_lessons_v2.go` — схема §4.8.

**Backend (правки):**
- `service/pipeline/prompts/distill.md` (трейс + операции),
  `prompts/planner.md` (vendor-уроки + секция инъекции),
  `prompts/coder.md` (инъекция уроков);
- `service/pipeline/render.go` (плейсхолдеры behavior_trace,
  existing_lessons, vendor_lessons), `validate.go` (реестр + поле lessons);
- `service/pipeline/pipeline.go:120-123` (встроенный distill как fallback);
- `service/runsmachine/spec.go` (поле lessons), `machine.go` (терминальный
  хук), `transitions.go` (при необходимости);
- `service/supervisor/supervisor.go` (инъекция этапа, запись
  инжектированных lesson ids в контекст рана), `stageproc.go:89`
  (подключение трейса — закрывает P0), `gates.go` (lessonSignals → трейс);
- `service/lessons/lessons.go` (SaveCard по kind, RelevantLessons →
  скоринг, findByPath → SQL по path), `finalize.go` (операции, per-card);
- `repository/lessons` (новые колонки, запросы для консолидации);
- `service/runsapi/api.go` (per-card resolve гейта);
- `controller/http/lessons.go` + `api/openapi.yaml` (новые поля, эндпоинт
  консолидации, здоровье).

**Web:** `components/memory/LessonsPanel.tsx` (kind-фильтр, здоровье,
вкладка консолидации, diff для REFINE/SUPERSEDE в гейте),
`components/run/GateChat.tsx` (per-card resolve UI), редактор пайплайнов
(чекбокс «Формировать уроки»).

**Тесты (acceptance = минимум, по AGENTS.md):** трейс-сборщик (парсинг
verdict.json/логов), парсер операций (валидные/мусор), атомарность
REFINE/SUPERSEDE, скоринг-ретрив, версионная деградация, relapse-эвристика
(позитив/негатив), гарантия distill (удалён/выключен/провальный ран).

---

## 6. Открытые вопросы (развилки, решить при реализации и зафиксировать в Итоге)

1. Формат операций в `lessons.md`: JSON-массив рядом с карточками vs поле
   `op:` в frontmatter (критерий — устойчивость парсера к мусору LLM).
2. Project-scope для vendor-уроков: допустим ли (свой форк/pin версии) или
   vendor-уроки всегда глобальны.
3. Per-card resolve гейта: если UX в батче не тянет — fallback «всё или
   ничего» (зафиксировать отклонение в Итоге таски).
4. Владение BehaviorTrace: `service/lessons` vs `service/supervisor`
   (данные у supervisor, потребитель — lessons).
5. Где хранить список инжектированных в ран уроков (нужен для
   relapse/outcome): контекст рана в БД vs артефакт в run_dir.
6. Веса скоринга: константы с обоснованием в коде; тюнинг — отдельной
   задачей после накопления статистики.

---

## 7. Ссылки

Исследования:
- ExpeL (AAAI 2024): https://arxiv.org/abs/2308.10144
- ReasoningBank (Google, 2025): https://arxiv.org/abs/2509.25140
- Reflexion (NeurIPS 2023): https://arxiv.org/abs/2303.11366
- ACE (2025): https://arxiv.org/abs/2510.04618
- A-MEM (NeurIPS 2025): https://arxiv.org/abs/2502.12110
- Generative Agents (UIST 2023): https://arxiv.org/abs/2304.03442
- Dynamic Cheatsheet (2025): https://arxiv.org/abs/2504.07952
- CoALA (TMLR 2024): https://arxiv.org/abs/2309.02427
- Survey on Memory Mechanism of LLM Agents (ACM TOIS 2025):
  https://arxiv.org/abs/2404.13501

Проект:
- Таска: `tasks/T-30-lessons-evolution.md`
- Решения: `tasks/00-overview.md` (D-52, D-81)
- Предыстория: `tasks/T-29-causal-memory.md` (включая итог — что
  осознанно отложено), `tasks/T-23-vendor-memory.md` (FTS-механика)
