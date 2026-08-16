# T-12 Жизненный цикл демона: конфиг, auth, graceful restart

Статус: done (2026-08-16) · M1 · зависимости: T-03, T-05

## Цель

`glamord` как надёжный локальный сервис (D-08/15/16).

## Scope

- **Конфиг**: `~/.glamor/config.yaml` (порты, лимит процессов, watchdog
  таймауты, дефолты harness'ов и моделей, TG-токен — M2). Первый запуск:
  создать конфиг с дефолтами + сгенерировать `token` (perms 600).
  `daemon.json` (активный порт, pid) — для клиентов (CLI, Tauri).
- **Auth middleware**: Bearer-токен; `/healthz` без auth (для Tauri-пинга
  — без чувствительных данных).
- **Graceful shutdown** (D-15): SIGTERM/SIGINT → перестать принимать новые
  раны (POST /runs → 503 `draining`) → supervisors живых этапов:
  `stop_requested_by=daemon` (отдельное значение! НЕ user — демон-рестарт
  ДОЛЖЕН auto-resume'ить при подъёме) → SIGTERM процессам, grace из конфига
  (дефолт 15s) → SIGKILL → закрыть WS/HTTP → checkpoint → exit 0.
- **Startup recovery** (D-15): скан `run_stages` в `running` → процесс
  мёртв/сирота (pid не существует или не наш дочерний) → `interrupted` →
  auto-resume по политике T-09. Открытые гейты переживают рестарт
  тривиально (они в БД).
- **Логирование**: slog, файл `~/.glamor/glamord.log` с ротацией (или
  усечением по размеру), уровень из конфига; per-run логи — в каталоге рана.
- **Единственный инстанс**: lock-файл `~/.glamor/glamord.lock`; второй
  запуск → сообщение «уже запущен на порту X» и exit 1.
- **CLI `glamor`**: `status`, `stop`, `runs` — тонкий клиент REST
  (бонус-фронтенд, дёшево из openapi).

## Acceptance

- Тест «рестарт посреди рана» (fake-harness): демон убит SIGKILL → старт →
  ран продолжается с interrupted-стадии, без дублей событий.
- Тест graceful: SIGTERM → активный этап прерван корректно, БД консистентна.
- Второй инстанс не стартует; auth отклоняет запросы без токена (401).

## Итог (2026-08-16)

Сделано:
- `internal/daemon`: AcquireLock (pid-lock, живой → ErrAlreadyRunning,
  протухший → снимается; InspectLock для сообщения «уже запущен на порту
  X»), LoadOrCreateToken (D-08, 32B hex, perms 600), daemon.json
  (atomic write, RemoveInfo при shutdown), RotateWriter (ротация лога по
  размеру, один бэкап).
- main (cmd/glamord): lock → config (первый запуск создаёт
  ~/.glamor/config.yaml с дефолтами) → токен → лог stderr+файл →
  миграции → **startup recovery** (RecoverInterrupted: сироты →
  interrupted; stop_requested_by=daemon очищается → auto-resume, D-15) →
  supervisor/REST/WS → daemon.json → SIGTERM: draining.Store(true)
  (POST /runs → 503 draining) → sup.Drain (stop_requested_by=daemon +
  kill с grace 15s + финальная классификация в interrupted — checkpoint)
  → http.Shutdown → RemoveInfo/Release.
- Supervisor.Drain теперь добивает классификацию (interrupted в БД до
  выхода).
- CLI `glamor`: status / runs / stop (daemon.json + token, Bearer).
- httpctrl: Deps.Draining (*atomic.Bool) → CreateRun 503 draining.

Проверка:
- Юнит: lock (двойной инстанс, stale), token (пересоздание/600), ротация,
  daemon.json round-trip.
- Интеграция (fake-harness, -race): «SIGKILL демона» посреди рана →
  рестарт → recovery → ран доезжает до succeeded, без дублей событий
  (ровно один переход в running на попытку); graceful Drain → interrupted
  by=daemon → recovery очищает → auto-resume → succeeded.
- Живой смоук: демон создал ~/.glamor (config/token 600/daemon.json/
  lock/log), /healthz=ok, /version отдаёт harnesses [kimi qwen], CLI
  `glamor status` работает, второй инстанс отклонён («already running»),
  SIGTERM → graceful shutdown, daemon.json снят.
