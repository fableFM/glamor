# T-09 Supervisor этапа: запуск, watchdog, auto-resume

Статус: done (2026-08-16) · M1 · зависимости: T-03, T-06, T-07

## Цель

Процессный контур этапа (D-13/14/15/16): запустить harness, следить,
классифицировать завершение, возобновлять.

## Механика

- **Spawn**: `os/exec`, процессная группа (чтобы убивать с детьми),
  stdout — построчный сканер → адаптер.ParseStream → события в журнал
  (батчами, T-04). Сырой stdout/stderr дублируется в файл
  `runs/<id>/stage-<key>-<iter>.log` (receipts, D-13).
- **Завершение этапа**: exit code + валидация артефакта (ArtifactContract
  из LaunchSpec: путь, обязательность, для verdict — JSON-схема).
  exit 0 + артефакт → `succeeded`; иначе → классификация.
- **Stall-watchdog**: нет событий стрима N сек (дефолт 120, конфиг на
  пайплайн) при живом процессе → SIGTERM → grace 10s → SIGKILL →
  `interrupted`.
- **Классификация остановки**: `stop_requested_by` в БД до убийства (D-14).
  Нет записи → auto-resume допустим.
- **Auto-resume**: политика `max_auto_resumes` (дефолт 3), backoff
  30s→2m→5m. Resume: `BuildCommand` с `SessionID` + системное сообщение
  «ты был прерван, продолжи; обязательный артефакт: X». Каждая попытка —
  события `stage.interrupted`/`stage.resumed` (+уведомление). Исчерпание →
  эскалация-гейт (T-11).
- **Retriable-события** стрима (сеть отвалилась, но процесс ещё жив) —
  ускоряют watchdog: после K подряд retriable-ошибок прерываем раньше
  таймаута (K=3, конфиг).
- **Таймаут этапа**: общий лимит времени на этап (конфиг, дефолт 60m) →
  interrupted → auto-resume.

## Границы

- Планировщик пула процессов (лимит параллельных этапов, D-33) — здесь же:
  семафор, очередь FIFO с событием `stage.queued`.
- Пользовательский stop/resume/interrupt приходят из API (T-05) как команды
  в supervisor через канал/БД-флаг — зафиксировать механизм в коде
  (рекомендация: БД-флаг + tick, устойчиво к рестарту).

## Acceptance

- Тесты с fake-harness (скрипт-эмулятор: спит, печатает NDJSON, падает,
  молчит): все ветки классификации.
- Тест: kill процесса посреди этапа → interrupted → auto-resume с
  увеличением resume_count → события в журнале.
- Тест: stop пользователем → auto-resume НЕ срабатывает.
- Лог рана содержит полный сырой выхлоп каждой попытки.

## Итог (2026-08-16)

Статус: done (2026-08-16)

Сделано: `backend/internal/service/supervisor/` — контур этапов:
- Tick-модель (event-driven, ADR-001): drainCompletions → due resumes
  (backoff) → reconcileProcs (стадия не running, а процесс жив → kill) →
  активные раны → NextAction → действие (start_run/start_stage/resume/
  escalate→escalation-гейт/finish).
- Spawn: process group (Setpgid), stdout→ParseStream→StreamBatcher в
  журнал + сырой stdout/stderr в `runs/<id>/stage-<key>-<iter>.log`
  (receipts, D-13); session id из стрима → UpdateRunningStage; usage →
  tokens в стадию. Пул процессов — семафор MaxParallel (stage.queued).
- Watchdog: stall (нет событий N сек) / stage timeout / K=3 подряд
  retriable → SIGTERM → grace → SIGKILL → interrupted.
- Классификация (D-13/14): stop_requested_by (user → interrupted БЕЗ
  auto-resume; daemon → interrupted с auto-resume при подъёме);
  exit 0 + артефакт → succeeded; exit 0 без обязательного артефакта →
  failed; non-zero → interrupted → auto-resume (backoff 30s→2m→5m,
  исчерпание → эскалация-гейт).
- Drain (graceful shutdown): stop_requested_by=daemon + kill живых.
- Хуки: PromptBuilder, PreStageHook (T-10 branch_mismatch), PostRunHook
  (T-10 PrepareBranch), OnStageSucceeded (T-11 гейты).
- Machine: +UpdateRunningStage (session_id/pid без смены состояния);
  +stages.GetStageByIteration (session id предыдущей попытки);
  StageSpec.+Artifact{Path,Required}.
- Защита от двойного resume (backoff-очередь vs NextAction-путь) и от
  гонки чтения потоков (scanners join до cmd.Wait).

Проверка (`go test -race`, fake-harness bash-скрипты): успех (артефакт,
session_id, tokens, лог), auto-resume (iteration=2, resume_count=1,
наследование session id, события interrupted/resumed), stall-watchdog →
эскалация-гейт, stop пользователем → НЕТ auto-resume, exit 0 без
артефакта → failed, пул MaxParallel=1 → stage.queued. Всё зелёное.
