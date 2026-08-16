# T-11 Гейты и диалоговый протокол

Статус: done (2026-08-16) · M1 · зависимости: T-03, T-05

## Цель

Human-in-the-loop как first-class механика (D-20/21/22/23).

## Виды гейтов и их жизненный цикл

| kind | открывается когда | resolve-действия |
|---|---|---|
| `question` | этап завершился с артефактом `questions.md` | `answer` (текст → resume сессии этапа) |
| `plan_approval` | spec готов (итерация вопросов закрыта) | `approve` / `comment` (→ resume планировщика) / `reject` (остановить ран) |
| `escalation` | исчерпаны итерации петли ИЛИ auto-resume | `answer` (ответ → fix) / `stop` |
| `final_review` | review approved, пайплайн отработал | `approve` (→ succeeded) / `comment` (→ fix) |

- Контекст гейта (`context_json`): пути артефактов, выжимка verdict'а,
  номер итерации — чтобы UI/TG показали без лишних запросов.
- Resolve идемпотентен (T-05). Резолв → событие `gate.resolved` → движок
  будит ран (T-03).

## Диалог «вопросы как артефакт» (D-20)

- Промпт планировщика инструктирует: вопросы писать в `questions.md` и
  завершаться. Ядро видит артефакт → gate `question`.
- Ответ пользователя → resume сессии с сообщением-ответом → планировщик
  продолжает; цикл повторяется, пока не появится `spec.md` без
  `questions.md` → `plan_approval`.

## Interrupt & Steer + Queue Note (D-22)

- Interrupt (API T-05): supervisor (T-09) мягко прерывает процесс
  (SIGINT→SIGTERM→SIGKILL), стадия `interrupted` c `stop_requested_by=user`
  + флаг «steer»: ядро резюмит сессию с сообщением пользователя.
  Отличие от stop: stop не резюмит, steer резюмит.
- Queue note: `POST /runs/{id}/notes` → ядро прикрепляет текст к ближайшему
  событию: входу следующего этапа, следующему гейту или ответу на вопрос.
  Заметки видны в UI списком, consumed-флаг.

## Эскалация петли (D-23)

- Verdict `changes_required` при `iteration == max_iters` → гейт
  `escalation` с последним verdict и списком нерешённых findings.

## Acceptance

- E2E-сценарий на fake-harness: вопросы → ответ → spec → approve →
  петля с эскалацией → ответ → финальный гейт → succeeded. Все гейты
  резолвятся через API, события корректны.
- Interrupt во время этапа: процесс убит, сессия резюмлена с сообщением,
  стрим продолжился в той же стадии.

## Итог (2026-08-16)

Сделано:
- Диалог «вопросы как артефакт» (D-20): StageSpec.+`questions_path` (с
  плейсхолдером {run_id}) и `gate_after`. Supervisor после succeeded-этапа
  (handlePostStageGates): questions.md непуст → гейт question (context_json:
  путь/этап/итерация); иначе gate_after → plan_approval/final_review.
- Резолв-эффекты в Machine.ResolveGateAPI: answer/comment с текстом по
  гейту этапа → `ReenterStage` — диалоговый ре-вход: новая попытка
  (iteration+1) БЕЗ инкремента resume_count + steer-заметка с текстом
  (уйдёт в промпт). Из succeeded/interrupted/failed, только от последней
  попытки. reject → run failed (уже было в T-03).
- Interrupt & Steer (D-22): supervisor.processSteers на тике — interrupted
  со stop_requested_by=user + непотреблённая steer-заметка → ReenterStage
  (без auto-resume счётчика). **Багфикс по ходу**: NextAction больше не
  предлагает auto-resume для interrupted со stop_requested_by (D-14) —
  иначе auto-resume перехватывал steer (поймано тестом).
- Queue note (D-22): supervisor.consumeNotes — все непотреблённые заметки
  рана (note+steer) уходят в промпт ближайшей попытки и помечаются
  consumed (атомарность на уровне попытки).
- Эскалация: ActionEscalate → гейт escalation (T-09), резолв answer →
  тот же ReenterStage-контур; reject = stop → run failed.

Проверка (e2e на fake-harness, -race):
- вопросы → гейт question → answer (попытка 2 видит ответ в промпте) →
  чистый spec → plan_approval → approve → run succeeded, iteration=2,
  resume_count=0, события gate.opened/resolved;
- Interrupt&Steer: процесс убит, попытка 2 резюмлена с сообщением в
  промпте, run succeeded, resume_count=0;
- queue note: попала в промпт первого этапа, consumed=true.
- Полный прогон: все пакеты зелёные, lint 0 issues.
