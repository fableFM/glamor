# T-30 Эволюция уроков: глубокий анализ поведения рана и vendor-уроки

Статус: done (2026-08-20) · M4 · зависимости: T-29 (lessons, done), T-23 (vendor-память,
FTS5, done), T-09 (журнал/receipts), T-20 (редактор пайплайнов), T-21
(версии пайплайнов), T-24 (метрики рана)

Эволюция D-52 → **D-81**. Не ломаем контракты T-29: человек подтверждает
каждый урок, файлы — источник содержимого, FTS5 без внешних векторных БД.

Глубокий анализ с картой кода и обоснованием дизайна (handoff для
исполнителя): `tasks/research/T-30-lessons-evolution-analysis.md`.

## Цель

Уроки перестают быть побочным продуктом «если пользователь что-то
комментировал». Каждый ран проходит **глубокий анализ поведения**: ядро
детерминированно собирает полный трейс (findings ревьюера, fix-итерации,
падения проверок, ответы на гейтах, исход), distill дистиллирует из него
уроки — включая уроки из **собственных провалов и успехов** пайплайна без
участия пользователя. Карточки эволюционируют (уточняются, вытесняются,
связываются), а не только копятся. Плюс новый вид уроков — **vendor-уроки**:
знание «вендор X версии Y в области Z ведёт себя так» из ресёрча
планировщика, с обязательной фиксацией версии.

## База исследования (на чём построен дизайн)

- **[ExpeL: LLM Agents Are Experiential Learners](https://arxiv.org/abs/2308.10144)**
  (AAAI 2024) — агент извлекает правила из трейсов **успехов и провалов**
  без файнтюна; ключевая механика — сравнение похожих успешных/провальных
  попыток. У нас: вход distill расширяется с «ответов пользователя» на
  полный трейс рана; fix-итерации = готовые пары «провал → успех».
- **[ReasoningBank](https://arxiv.org/abs/2509.25140)** (Google, 2025) —
  дистилляция обобщённых стратегий из self-judged успешного и неуспешного
  опыта; провал — первоклассный источник сигнала. У нас: findings ревьюера
  и упавшие проверки — это self-judged провалы, уже машиночитаемые
  (verdict.json).
- **[Reflexion](https://arxiv.org/abs/2303.11366)** (NeurIPS 2023) —
  вербальная рефлексия над провалом («что пошло не так и что делать
  иначе»). У нас: per-finding рефлексия — промежуточный слой между
  сырым finding и карточкой урока.
- **[ACE: Agentic Context Engineering](https://arxiv.org/abs/2510.04618)**
  (2025) — контекст как эволюционирующий плейбук: **инкрементальные
  дельта-апдейты** (generator → reflector → curator) вместо переписывания,
  защита от context collapse. У нас: distill возвращает операции
  NEW/REFINE/SUPERSEDE над существующими карточками, а не только новые.
- **[A-MEM: Agentic Memory](https://arxiv.org/abs/2502.12110)** (NeurIPS
  2025) — Zettelkasten: атомарные заметки, **связи** между ними, эволюция
  старых записей при появлении новых. У нас: `related` между уроками,
  supersede-цепочки, консолидация дублей.
- **[Generative Agents](https://arxiv.org/abs/2304.03442)** (UIST 2023) —
  retrieval score = recency × importance × relevance. У нас: замена
  «FTS top-10» на взвешенный скоринг.
- **[Dynamic Cheatsheet](https://arxiv.org/abs/2504.07952)** (2025) —
  кумулятивная память с измерением вклада в результат. У нас:
  outcome-трекинг — применили урок, а область снова сломалась → relapse.
- **[CoALA](https://arxiv.org/abs/2309.02427)** — словарь: наши
  behavior-уроки = semantic+episodic память, vendor-уроки = semantic
  память о внешних контрактах. Procedural (скиллы) — не в этой таске.
- Обзорно: [A Survey on the Memory Mechanism of LLM-based
  Agents](https://arxiv.org/abs/2404.13501) — операции write/management/read.

Принципиально **не берём**: векторные БД/эмбеддинги (нарушают
локальность, D-52), внешние memory-фреймворки (mem0/Zep/Letta),
автоматическое подтверждение уроков без человека.

## Проблемы текущей реализации (что чиним)

1. **Узкий вход distill**: только `lessonSignals()` — ответы на гейтах +
   queue notes (`supervisor/gates.go:202`). Молчаливый ран (даже с тремя
   fix-итерациями и десятком findings) даёт ноль уроков. Главный источник
   опыта — собственное поведение пайплайна — выброшен.
2. **Только-добавление**: карточки копятся, дедуп — точное совпадение
   title для rejected (`lessons.go:133`). Distill не видит confirmed-уроки
   и не может их уточнить. `superseded` в схеме есть, но никто не
   выставляет.
3. **Мёртвый relapse**: `relapse_count` и `IncrementRelapse` существуют,
   но не вызываются нигде (задокументировано в итоге T-29). Урок может
   не работать годами — никто не узнает.
4. **Примитивный ретрив**: FTS по словам задачи, top-10, общий бюджет
   (`RelevantLessons`, `lessons.go:196`). Нет важности, нет свежести, нет
   связи с исходом применений. Инъекция только в planner.
5. **Нет vendor-уроков**: знание «goose v3 требует AddMigrationContext»
   живёт в vendor-памяти как документация (T-23), но правила поведения
   с конкретной версией вендора не извлекаются и не версионируются.
6. **Distill необязателен и хрупок**: это рядовой этап в шаблоне
   `default.yaml`, материализуемый в `spec_json.stages`. Редактор
   пайплайнов (T-20) может его удалить — формирование уроков молча
   исчезает. Отдельно: distill стоит в линейной цепочке успеха, поэтому
   провальные/эскалированные раны (самый богатый источник уроков) до
   него не доходят.

## Дизайн

### 1. Трейс поведения рана (детерминированный, ядро)

Новый сборщик `BehaviorTrace` (service/lessons, вызывается supervisor'ом
перед distill-этапом вместо `lessonSignals`). Источники — только факты,
без LLM:

- **Гейты**: ответы/комментарии/редакты пользователя (как сейчас).
- **Findings ревьюера**: парсинг `verdict.json` каждой итерации —
  severity, file/line, `required_fix`, `forbidden_fix`, DISPUTED-споры
  фиксера (машиночитаемо, это наш self-judged сигнал à la ReasoningBank).
- **Fix-петля**: какие findings исправлены за какую итерацию, сколько
  итераций ушло, что осталось (пары «провал → исправление» à la ExpeL).
- **Проверки**: упавшие команды из receipts/журнала этапов (тесты, build,
  lint) — с exit code и выжимкой stderr.
- **Исход рана**: финальный verdict, количество итераций, approve/reject
  на гейтах, длительность этапов (метрики T-24).

Результат — `run_facts.json` в run_dir (артефакт, виден в UI) + строка
для промпта distill. Трейс — факт, не требует подтверждения; уроки из
него — требуют.

### 2. Distill v2 (промпт + операции над карточками)

Вход промпта `prompts/distill.md`:
- `{{behavior_trace}}` (вместо/над `{{gate_answers}}`),
- `{{existing_lessons}}` — id + title + триггеры **confirmed**-уроков
  (раньше distill видел только rejected — это ключевое изменение для
  консолидации),
- `{{rejected_lessons}}` — как сейчас.

Выход — операции (JSON-массив в `lessons.md` рядом с карточками либо
карточки с полем `op:` — выбрать при реализации, формат должен быть
устойчив к мусору как ParseCards):

- `NEW` — новая карточка (как сейчас, секции Ситуация/Симптом/Причина/
  Правило + **Доказательства**: run, stage, finding id, файлы).
- `REFINE <lesson_id>` — уточнение существующего confirmed-урока (новая
  формулировка; дельта à la ACE, не переписывание с нуля).
- `SUPERSEDE <lesson_id>` — новая карточка вытесняет старую (старая →
  `superseded`, ссылка `superseded_by`).
- `LINK <id1> <id2>` — связь «уроки про одно и то же» (à la A-MEM).
- `QUESTION` — вопрос пользователю (как сейчас, «ВОПРОС:» в Причине).
- `NO_LESSONS` — как сейчас.

Правила промпта: урок только при реальной причинно-следственной связи;
уроки из успехов («паттерн X сработал, закрепить») допустимы наравне с
уроками из провалов; каждый урок обязан ссылаться на факт из трейса
(запрет на «пересказ рана»); UNVERIFIED-конвенция сохраняется.

Гейт `lesson_review` расширяется: показывает не только новые карточки, но
и предложенные REFINE/SUPERSEDE с diff'ом формулировок. Approve применяет
операции атомарно (одна tx: файл + БД + FTS-реиндекс). Reject по операции,
не «всё или ничего» — пользователь может принять часть (per-card resolve;
если UX не тянет в этом батче — fallback «всё или ничего», зафиксировать
отклонение в Итоге таски).

### 3. Vendor-уроки (новый kind)

Карточка вида `vendor` с обязательным заголовком (формат — заказанный
пользователем):

```markdown
---
id: lesson-<ulid>
kind: vendor
vendor: <название вендора>            # обязательно
vendor_version: <точная версия>       # обязательно, не "latest"
area: <описание области изучения>     # обязательно: миграции / API X / ...
status: proposed | confirmed | superseded | outdated
triggers: [вендор, технология, паттерн]
source: <URL/путь к doc/vendor-памяти>
---

## Урок
<сам полученный урок: поведение вендора этой версии, грабли, инварианты>

## Доказательства
<run, этап, ссылка на код зависимости или официальную доку версии>
```

- **Точка захвата — ресёрч планировщика**: в промпт planner'а (шаг
  «внешние границы») добавляется инструкция: каждый подтверждённый факт о
  поведении вендора оформлять черновиком vendor-урока в
  `{run_dir}/vendor-lessons.md` (версия — из go.mod/lockfile/доки версии,
  не из памяти модели). Distill добирает vendor-карточки из spec.md и
  vendor-lessons.md и несёт на тот же гейт lesson_review.
- **Хранение**: та же таблица `lessons` + колонки `kind`, `vendor`,
  `vendor_version`, `area`; файлы — `~/.glamor/lessons/vendor/<name>/`
  (глобальные по природе; project-scope vendor-урок не имеет смысла —
  версия вендора общая, но если у проекта свой форк/pin — scope=project
  допустим, зафиксировать решение в Итоге).
- **Версионная деградация**: детерминированная проверка при старте рана —
  если `vendor_version` урока ≠ версии в lockfile проекта, урок помечается
  `outdated` и инжектится с префиксом «ВЕРСИЯ ИЗМЕНИЛАСЬ — ПЕРЕПРОВЕРИТЬ»
  (не выбрасываем: поведение могло не измениться).
- **Инъекция**: триггер по упоминанию вендора в задаче/spec (FTS), в
  промпт planner'а секцией «Уроки по вендорам» отдельно от behavior-уроков.

### 4. Консолидация (management)

- Distill предлагает REFINE/SUPERSEDE/LINK — основной механизм (см. п.2).
- Периодический prune: ручной из UI («Память → Уроки → Консолидация»):
  кандидаты в дубли (overlap триггеров/FTS-схожесть title), список
  superseded старше N дней, уроки с relapse ≥ applied. Никакой авто-
  консолидации без человека (D-52 неизменен).
- Supersede-цепочки храним: `superseded_by` в БД, старый файл не
  удаляется (история), из инъекции исключается.

### 5. Ретрив v2 (скоринг вместо top-10)

`RelevantLessons` → скоринг по Generative Agents:

```
score = w_rel * fts_rank + w_imp * importance + w_rec * recency_decay
```

- `fts_rank` — нормированный ранг FTS5 (как сейчас).
- `importance` — производная: источник (answer/comment пользователя >
  auto из трейса), `applied_success_count` ↑, `relapse_count` ↓. Хранится
  в БД, пересчитывается детерминированно при изменении счётчиков.
- `recency_decay` — экспонента от `last_applied_at`/`created_at` (уроки,
  которые давно не понадобились, тонут).

Прочее: инъекция не только в planner, но и в coder (секция «Уроки»);
бюджет `LessonTokenBudget` — из константы в конфиг демона; фильтр по
триггерам этапа. Веса — константы в коде с комментарием-обоснованием,
тюнинг — отдельной задачей.

### 6. Петля качества (relapse + outcome)

- **Relapse-детекция** (детерминированная эвристика, после этапа
  reviewer): если finding blocking/major матчится по FTS с телом/триггерами
  урока, который **был инжектирован в этот ран**, — `IncrementRelapse`
  (наконец подключаем) + событие в журнал + флаг в UI «урок не работает —
  уточнить формулировку?». Эвристика может ошибаться → статус урока не
  меняется автоматически, только счётчик и подсветка.
- **Outcome**: ран завершился approve'ом финального гейта без
  blocking-findings в области инжектированных уроков →
  `applied_success_count++` (новая колонка). Отношение applied/relapse —
  «здоровье» урока в UI (сортировка, фильтр «требуют внимания»).

### 7. Обязательный distill (настройка пайплайна «Формировать уроки»)

Distill перестаёт быть рядовым этапом шаблона и становится
**гарантией ядра**, управляемой настройкой пайплайна:

- **Spec**: плоское поле `lessons` (`"on"` | `"off"`) в
  `runsmachine.Spec` — по прецеденту `review_policy`/`memory_scope`
  (`spec.go`). Живёт в `spec_json` → версионируется вместе с пайплайном
  (T-21). Дефолтный пайплайн — `lessons: "on"`.
- **UI**: чекбокс «Формировать уроки» в настройках пайплайна (редактор
  T-20). Состояние чекбокса отражает гарантию, а не наличие ноды в графе.
- **Гарантия включено** (`lessons: true`): supervisor при построении
  плана рана проверяет наличие distill-этапа в spec; отсутствует —
  детерминированно дописывает встроенный distill (промпт
  `prompts/distill.md`, артефакт `lessons.md`, `gate_after:
  lesson_review`) перед финальным гейтом. Пользовательский distill-этап
  (свой промпт/модель), если определён, используется как есть —
  гарантия = «хотя бы один distill в плане», а не «наш шаблон».
- **Гарантия выключено** (`lessons: false`): мастер-выключатель —
  distill-этапы пропускаются (событие skip в журнале), гейт
  `lesson_review` не открывается, даже если этап остался в YAML.
  `BehaviorTrace` не собирается.
- **Distill на любом исходе**: при `lessons: true` distill запускается
  и на терминальных неуспешных исходах (failed, escalation-rejected) —
  провальные раны дают самые ценные уроки. Тривиальные трейсы (упал
  harness, инфраструктурный сбой) отсекаются правилом `NO_LESSONS`.
  Реализация: хук supervisor'а на терминальные переходы стейт-машины,
  не этап в линейной цепочке успеха.
- **Per-run override (опционально)**: флаг «не формировать уроки в этом
  ране» при создании рана (аналог per-run mute TG, D-73) — для
  NDA/разовых задач.
- `validate.go`: `settings.lessons` — валидное поле; distill-плейсхолдеры
  (`behavior_trace`, `existing_lessons`) регистрируются там же.

## Модель данных (миграция, goose Go-файлом по D-05)

`ALTER TABLE lessons` (+ новые индексы):

- `kind TEXT NOT NULL DEFAULT 'behavior' CHECK (kind IN
  ('behavior','vendor'))`
- `vendor TEXT NULL`, `vendor_version TEXT NULL`, `area TEXT NULL`
- `importance REAL NOT NULL DEFAULT 0.5`
- `applied_success_count INTEGER NOT NULL DEFAULT 0`
- `last_applied_at TIMESTAMP NULL`
- `superseded_by TEXT NULL REFERENCES lessons(id)`
- `related_json TEXT NOT NULL DEFAULT '[]'`
- статус `outdated` в CHECK (пересоздание таблицы, как в миграции T-29).

Обратная совместимость: существующие уроки → kind=behavior, importance
дефолтная, пересчёт при первом изменении счётчиков.

## Жизненный цикл (итоговый)

1. Ран идёт → ядро пишет факты в трейс (бесплатно).
2. Planner при ресёрче пишет черновики vendor-уроков с точными версиями.
3. Ран завершён (любой исход) → при `lessons: true` `BehaviorTrace` →
   `run_facts.json`; supervisor гарантирует наличие distill в плане
   (дописывает встроенный, если этап удалили из пайплайна).
4. Distill v2: трейс + существующие уроки → операции NEW/REFINE/SUPERSEDE/
   LINK/QUESTION.
5. Гейт lesson_review: пользователь утверждает/редактирует/отклоняет
   (per-card, с diff'ом для REFINE/SUPERSEDE).
6. Применение операций атомарно: файлы + БД + FTS.
7. Следующие раны: скоринг-ретрив в planner и coder; версии vendor-уроков
   сверяются с lockfile.
8. Reviewer нашёл нарушение инжектированного урока → relapse++; approve
   без находок в области → success++. Здоровье уроков видно в UI.

## Принципы (неизменные от D-52 + новые)

- Ничто не попадает в память без подтверждения пользователя. Автоматичны
  только факты: трейс, счётчики, outdated-флаги.
- Уроки — контекст, не команды: ядро ничего не исполняет из уроков.
- Каждый урок имеет доказательства (run/stage/finding/URL) — урок без
  источника не принимается на гейте.
- Vendor-урок без точной версии — не урок: draft отклоняется distill'ом
  ещё до гейта.
- FTS5, файлы, локальность — никаких векторных БД и внешних сервисов.

## Acceptance

- **Поведенческий урок без пользователя**: ран с fix-петлей (reviewer
  нашёл blocking, fixer исправил, пользователь молчал) → distill предложил
  карточку с Причиной из finding и Доказательствами (finding id, итерация)
  → гейт → confirmed → следующий похожий ран получил урок в промпте
  planner'а (проверить по prompt-*.md).
- **REFINE**: distill увидел confirmed-урок и предложил уточнение → гейт
  показал diff → approve → файл обновлён, старая версия в истории,
  FTS переиндексирован.
- **SUPERSEDE**: старый урок → superseded, исключён из инъекции, новый
  инжектится; `superseded_by` проставлен.
- **Vendor-урок**: planner при ресёрче зависимости записал черновик с
  версией из go.mod → гейт → сохранён в `vendor/<name>/` → ран с
  упоминанием вендора получает секцию «Уроки по вендорам». Bump версии в
  lockfile → урок `outdated` с префиксом-предупреждением в промпте.
- **Relapse**: урок инжектирован, reviewer снова нашёл нарушение той же
  темы → `relapse_count++`, событие в журнале, подсветка в UI.
- **Outcome**: чистый approve → `applied_success_count++`.
- **Скоринг**: свежий важный урок обгоняет старый FTS-совпадающий при
  равном ранге; бюджет из конфига.
- **Обязательный distill**: пайплайн с `lessons: true`, из которого в
  редакторе удалили distill-ноду → ран всё равно проходит distill и
  открывает гейт lesson_review (в prompt-*.md виден встроенный промпт).
- **Distill на провале**: ран, завершившийся failed/эскалацией, при
  `lessons: true` тоже проходит distill; инфраструктурный сбой (harness
  упал) → `NO_LESSONS`, гейт не донимает.
- **Мастер-выключатель**: `lessons: false` → distill пропущен (событие в
  журнале), гейт не открыт, даже если нода осталась в YAML пайплайна.
- Юнит-тесты рядом с кодом: трейс-сборщик (парсинг verdict.json, receipts),
  парсер операций distill (валидные/мусор), атомарное применение
  REFINE/SUPERSEDE, скоринг-ретрив, версионная деградация vendor-уроков,
  relapse-эвристика (позитив/негатив). `make build && make test && make
  lint` зелёные.

## Итог

### Слайс 1: миграция, репозиторий, ядро lessons (2026-08-20)

**Сделано:**

- **Миграция** `backend/migrations/20260819120000_lessons_v2.go` (goose,
  пересоздание таблицы по прецеденту T-29): kind (CHECK behavior/vendor),
  vendor/vendor_version/area, importance REAL DEFAULT 0.5,
  applied_success_count, last_applied_at, superseded_by REFERENCES
  lessons(id), related_json DEFAULT '[]', статус outdated в CHECK, индексы
  по status/kind/vendor. Существующие уроки → kind=behavior, дефолты.
  Down-миграция отыгрывает схему T-29 (outdated→confirmed).
- **dtorep.Lesson + repository/lessons**: новые поля во всех
  scan/select/insert; методы GetLessonByPath, GetByIDs, ListByKind,
  ListVendor, SetSuperseded, UpdateRelated, UpdateImportance,
  IncrementAppliedSuccess. IncrementRelapse/IncrementAppliedSuccess двигают
  importance прямо в SQL (детерминированные дельты).
- **trace.go** — BehaviorTrace: гейты/notes (через входные DTO, пакет не
  импортирует supervisor), парсинг verdict.json (+ архивы verdict-N.json),
  выжимка упавших проверок из логов, fix-петля, исход рана → run_facts.json
  + PromptText для {{behavior_trace}}.
- **operations.go** — парсер операций distill v2 + атомарное per-card
  применение (OpResult на операцию, битая не ломает соседние).
- **scoring.go** — ретрив v2 (скоринг), отдельный RelevantVendorLessons,
  SetTokenBudget, список инжектированных уроков в результате.
- **vendor.go** — ParseVendorCards, ValidateVendorDraft (без точной версии —
  отказ до гейта), SaveVendorCard, CheckVendorVersions, ParseGoMod.
- **quality.go** — RelapseCheck, OutcomeApprove (только счётчики, статусы
  автоматически не меняются).

**Решения открытых вопросов (п.6 анализа):**

1. **Формат операций**: поле `op:` в frontmatter карточки +
   `target:`/`target2:`; карточка без op: = NEW (обратная совместимость
   T-29); «ВОПРОС:» в теле = QUESTION. JSON-массив рядом отвергнут: одна
   битая запятая ломала бы весь вывод, frontmatter устойчив как ParseCards.
2. **Vendor scope**: глобальные по умолчанию
   (`~/.glamor/lessons/vendor/<name>/`); scope=project допустим при явной
   передаче (форк/pin) — SaveVendorCard маппит не-project в global.
3. **Per-card resolve**: реализован полно (ядро per-card + UI), fallback
   «всё или ничего» сохранён для обратной совместимости.
4. **Владение BehaviorTrace**: `service/lessons` (входные DTO свои,
   supervisor маппит данные) — циклов импортов нет.
5. **Инжектированные уроки рана**: артефакт в run_dir (см. слайс 2).
6. **Веса скоринга**: w_rel=1.0, w_imp=0.5, w_rec=0.5 (релевантность
   доминирует, важность/свежесть — тай-брейкеры); recency =
   0.5^(дни/30) от last_applied_at/created_at; fts_rank нормируется на
   min bm25 выборки. Тюнинг — отдельной задачей (вне скоупа).
7. **Importance**: стартовая по источнику (user=0.7 > auto=0.5), дальше
   дельты в SQL: +0.05 за applied_success, −0.10 за relapse, зажато в
   [0.1, 1.0].
8. **История REFINE**: `<id>.md.<timestamp>.bak` рядом с файлом.

### Слайс 2: wiring ядра (2026-08-20)

Подключено ядро `service/lessons` (слайс 1) к supervisor, pipeline,
runsmachine, runsapi, HTTP API и конфигу демона.

**Ключевые решения:**

- **Признак distill-этапа**: `gate_after == "lesson_review"`
  (`runsmachine.IsDistillStage`). По нему собирается трейс, работает
  мастер-выключатель и находится этап для гарантии.
- **Гарантия distill**: `Machine.SetSpecAugmenter` — спека дочитывается
  аугментером при каждом чтении (`Machine.SpecFor`, `NextAction`,
  `stageHarness`): при `lessons:on` без distill-этапа дописывается
  встроенный (`pipeline.BuiltinDistillAugmenter`, embedded
  `prompts/distill.md`, `origin=builtin` — виден в context гейта).
  spec_json при этом неизменен (T-21). Пользовательский distill уважается
  (гарантия = «хотя бы один»).
- **Терминальный distill** (отклонение от «перевести ран в состояние
  distill»): выбран вариант out-of-band — тик supervisor'а
  `processTerminalDistills` подбирает failed-раны (завершившиеся после
  подъёма демона, grace 5s — окно ручного resume), собирает полный трейс
  с исходом и запускает distill-этап обычным `launchStage` с тем же
  run_dir. Гейт lesson_review для failed-рана открывается напрямую
  (`openTerminalLessonGate`, идемпотентно по ключу), БЕЗ перевода рана в
  waiting_gate (таблица переходов ADR-001 не расширялась). Резолв гейта
  штатный (ResolveGateAPI → finalizer). Маркер «обработан» — строка этапа
  distill в БД + in-memory кеш. NO_LESSONS → гейт не открывается (старая
  проверка «title:» в gates.go).
- **Мастер-выключатель**: `lessons:off` → distill-этап пропускается
  (`SkipStage` + событие `stage.skipped{reason:lessons_off}`), гейт не
  открывается, трейс не собирается. Пустое `lessons` = on (старые
  spec_json).
- **P0 (вход distill не был подключён)**: закрыт — `spawnStage` для
  distill-этапа вызывает `attachBehaviorTrace`: `CollectTrace` →
  `run_facts.json` (артефакт) + `{{behavior_trace}}`; `LessonSignals`
  оставлен как fallback-плейсхолдер `{{gate_answers}}` (теперь тоже
  заполняется) для пользовательских пайплайнов.
- **Инжектированные уроки**: артефакт `run_dir/injected-lessons.json`
  (аккумулятивно, дедуп по lesson_id; решение открытого вопроса №5 —
  артефакт, а не контекст рана в БД). Пишет рендер промптов
  (`AppendInjectedRecords`), читают relapse/outcome (`LoadRunInjections`).
- **Петля качества**: relapse — после reviewer (событие `lessons.relapse`);
  outcome — approve финального гейта → `runsapi.SetOutcomeHook` →
  `OutcomeApprove` (best-effort, резолв не отменяется). Только по урокам,
  реально инжектированным в ран.
- **Версионная деградация**: при старте рана `CheckVendorVersions` →
  событие `lessons.vendor_outdated`.
- **Per-card резолв**: `ResolveGateRequest.lesson_ops {accept, reject}`
  (индексы операций ParseOperations); отсутствует — «всё или ничего»
  (обратная совместимость). Rejected NEW сохраняются rejected (dedup),
  отклонённые дельты просто не применяются.
- **Архивация verdict-<iter>.json** на каждой итерации reviewer (полная
  fix-петля в трейсе).
- **Заодно починено**: `FinalizeLessonGate` читал `lessons_path` из
  контекста гейта, но supervisor его не записывал (в проде эффекты гейта
  падали) — теперь пишется абсолютный путь.
- **Конфиг**: `lessons.token_budget` (дефолт 8000), env
  `GLAMOR_LESSON_TOKEN_BUDGET`, проброс в `SetTokenBudget`.

**Отклонения/ограничения слайса:**

- Ядро `Operation` расширено vendor-полями (kind/vendor/vendor_version/
  area) — без этого vendor-карточки теряли вид при прохождении гейта
  (ParseOperations их дропал). Vendor-карточки через гейт сохраняются
  глобально (как SaveVendorCard).
- Diff для REFINE/SUPERSEDE в UI гейта — следующий слайс (web);
  backend отдаёт операции и данные для него (per-card resolve готов).
- `web/src/api/schema.d.ts` не регенерирован (make gen-ts) — web/ вне
  скоупа слайса.
- Остановленный пользователем ран (stopped) терминальный distill не
  проходит — по таске только failed/эскалация.

**Проверка**: `go build ./...`, `go test -race ./...` (все пакеты ok),
`golangci-lint run` — 0 issues, gofumpt чисто, `make gen-go` идемпотентен.
Новые тесты: гарантия distill (встроенный дописан и выполнен, origin в
гейте), мастер-выключатель (skip + событие, гейт не открыт), терминальный
distill на failed-ране (out-of-band гейт, трейс с исходом), per-card
резолв, vendor-операции, журнал инъекций, сводка existing, кандидаты
консолидации, аугментер, валидация lessons.

### Слайс 3: web UI (2026-08-20)

**Сделано:**

- **Типы**: `web/src/api/schema.d.ts` регенерирован штатным
  `npm run gen:api` (openapi-typescript из ../api/openapi.yaml): поля
  Lesson (kind/vendor/vendor_version/area/importance/applied_success_count/
  superseded_by/related, статус outdated), lesson_ops в
  ResolveGateRequest, GET /lessons/consolidation. В `client.ts` добавлены
  типы LessonConsolidation/LessonDuplicatePair и функция
  `lessonConsolidation(supersededDays)`.
- **LessonsPanel** (Память → Уроки): фильтры по kind (behavior/vendor) и
  «требуют внимания» (attention=true), статус-фильтр дополнен outdated
  (бейдж с title «версия изменилась…»); бейдж kind; vendor-уроки показывают
  vendor@version + area; колонка «здоровье» ✓applied_success/✗relapse
  (relapse ≥ success и relapse > 0 — красная подсветка); секция
  «Консолидация» (переключатель в тулбаре): GET /lessons/consolidation —
  дубли (с reason и похожим уроком), superseded старше N дней (N —
  поле ввода, дефолт 30), нездоровые; действия только ручные — переход
  к уроку и смена статуса через существующий PATCH (без авто-консолидации).
- **Гейт lesson_review** (GateView): новый LessonReviewView. Полный
  черновик lessons.md читается артефактом рана (question несёт усечённый
  excerpt 1500 символов — по нему индексы lesson_ops не восстановить);
  парсер операций `web/src/lib/lessonOps.ts` — точное зеркало backend
  ParseOperations/splitCards (правила пропуска битых карточек 1-в-1, иначе
  индексы per-card разъезжаются). NEW/QUESTION — карточка с markdown;
  REFINE/SUPERSEDE — line-diff (свой LCS, без зависимостей) против текущей
  версии урока (GET /lessons/{id}, тело без frontmatter); LINK — пара id.
  Per-card resolve: каждая операция принять/отклонить (дефолт — принять),
  «Применить выбор» шлёт approve + lesson_ops {accept, reject}; fallback —
  «Принять всё»/«Отклонить всё» без lesson_ops (старое поведение).
  GateChat не тронут: его кнопки остаются fallback «всё или ничего».
- **Редактор пайплайнов**: PipelineSpec.lessons (boolean, дефолт on);
  parse: `lessons !== 'off'` (отсутствие ключа = on, как на backend);
  serialize: ключ пишется только при выключении (round-trip стабилен);
  чекбокс «формировать уроки (distill + гейт lesson_review)» в LoopPanel —
  отражает настройку spec, а не наличие distill-ноды в графе.
- **run_facts.json**: backend регистрирует его артефактом рана
  (kind=run_facts, supervisor/lessons.go) — показывается штатным табом
  «Артефакты», в UI ничего не потребовалось.

**Отклонения/ограничения слайса:** нет по скоупу. TG-UX — вне скоупа
(таска). Per-card резолв живёт только в гейт-вью панели этапа; из чата
доступен fallback + кнопка «Открыть» (появляется на длинных гейтах,
excerpt lessons.md практически всегда > 280 символов).

**Проверка**: `npm run build` (tsc + vite) зелёный, `npm run lint`
(oxlint) — 0 warnings/errors, `npm test` (vitest) — 78/78 (новые тесты:
lessonOps — зеркало парсера операций + lineDiff; pipelineModel —
lessons round-trip).

### Ревью и фиксы (2026-08-20)

Независимый семантический ревью diff'а: 1 critical, 3 major, 10 minor.
Все исправлены, на каждый — регрессионный тест:

- **C1 (critical, D-52)**: полный reject гейта lesson_review применял
  REFINE/SUPERSEDE/LINK к confirmed-урокам. Теперь reject обрабатывает
  только NEW/QUESTION (rejected для dedup), дельты пропускаются.
- **M2**: терминальный distill терялся при рестарте/сбое запуска
  (handled по наличию строки этапа). Теперь handled = этап в терминальном
  состоянии ИЛИ гейт открыт; pending/interrupted distill failed-ранов
  перезапускается при подъёме демона (recovery для ранов любой давности).
- **M3**: ошибка финализатора больше не валит резолв гейта (гейт уже
  CAS-резолвнут, повтор опасен дублями): per-op изоляция сохранена,
  ошибка журналируется событием `lessons.finalize_error`, ответ API
  успешен.
- **M4**: relapse дедуплицирован по (run, finding id) через
  `run_dir/relapse-hits.jsonl` (finding на N итерациях → relapse×1);
  outcome-хук исключает уроки с relapse в этом ране
  (`LoadSuccessfulInjections`).
- **m5**: web-зеркало парсера — finishCard() по EOF только из stBody
  (индексы lesson_ops больше не съезжают на транкейтнутом выводе).
- **m6**: lesson_ops только с action=approve (400 иначе).
- **m7**: applyRefine сохраняет vendor-метаданные в frontmatter файла.
- **m8**: IncrementApplied обновляет last_applied_at (recency по
  контракту).
- **m9**: токен-бюджет считается в рунах, не байтах.
- **m10**: REFINE outdated разрешён и восстанавливает статус в confirmed
  (сводка existing для distill больше не предлагает гарантированно
  падающие операции).
- **m11**: approve гейта человеком = SourceUser (0.7) во всех путях.
- **m12**: журналы рана — JSONL с O_APPEND (`injected-lessons.jsonl`,
  `relapse-hits.jsonl`), read-modify-write гонки fan-out устранены.
- **m13**: CheckVendorVersions двусторонний — обратное совпадение версии
  восстанавливает outdated→confirmed (событие lessons.vendor_restored).
- **m14**: открытие гейта — через ParseOperations (LINK-only черновик
  открывает гейт), эвристика «title:» удалена.
- **m15**: run_facts.json регистрируется артефактом один раз (дедуп по
  пути).

**Финальная проверка**: `make build` — 0, `make test` — 0 (весь backend
с -race, web 79/79), `make lint` — 0. Diff перечитан ревьюером
(архитектурные grep-проверки скилла чисто, D-10..D-16 соблюдены).

## Вне скоупа

- Эмбеддинги/векторный ретрив, внешние memory-сервисы (противоречат D-52).
- Автоматическое подтверждение/отклонение уроков.
- Procedural-память (скиллы/плейбуки действий) — отдельная линия.
- Тюнинг весов скоринга на статистике ранов (нужна накопленная база).
- TG-UX для per-card resolve гейта (в M4 — только web UI; TG получает
  сводку).
