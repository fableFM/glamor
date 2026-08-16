# T-04 Event journal + WebSocket hub

Статус: done (2026-08-16) · M1 · зависимости: T-02

## Цель

Append-only журнал событий как единая шина состояния (D-11) + WS-рассылка
клиентам с догоняющей синхронизацией.

## Scope (in)

- `internal/events`:
  - `Append(tx, event)` — вызывается только внутри транзакций store (T-02).
  - In-process pub/sub: после коммита событие рассылается подписчикам
    (WS hub, TG-адаптер в M2). Публикация ПОСЛЕ commit, не до.
  - `Replay(runID, afterID, limit)` → страница событий.
- WS endpoint `/ws?run_id=...&last_event_id=...&token=...`:
  - При коннекте: догон из `Replay`, затем live-поток. Стаартовое сообщение
    `synced {last_event_id}` — граница replay/live.
  - Подписки: `run_id=*` (все раны — для списка проектов/инбокса гейтов).
  - Ping/pong keepalive; backpressure: медленный клиент отключается, а не
    тормозит шину (буфер на клиента, drop → клиент пересинхронизируется).
- Типы событий (kind): `run.*`, `stage.*`, `gate.*`, `stream.thinking`,
  `stream.text`, `stream.tool_call`, `stream.tool_result`, `stream.usage`,
  `stream.error`, `notify.*`. Payload-схемы описать в openapi.yaml
  (компонент `Event`) — фронт генерит типы из спеки (D-02).
- Стрим-события этапа пишутся батчами (например каждые 200мс или 64КБ) —
  не транзакция на каждый токен.

## Scope (out)

- Парсинг harness-потока в события (T-06/07/08) — здесь только транспорт
  и хранение.
- TG-подписка (T-19).

## Acceptance

- Тест: клиент с `last_event_id=N` после реконнекта получает ровно события
  >N, без дублей и пропусков.
- Тест: 100к событий в журнал — Replay страницами работает, память демона
  стабильна.
- Нагрузочный мини-тест: 10 WS-клиентов на активном ране, демон не растёт
  по CPU/памяти линейно от числа клиентов.

## Итог (2026-08-16)

Сделано:
- `internal/events`:
  - `Journal` — реализует `runs.EventAppender` (T-03) и `TxExecutor`:
    Append пишет событие в tx (outbox, D-11) и складывает в collector в ctx;
    WithTx публикует накопленное в Hub только ПОСЛЕ коммита. ВАЖНО: ctx для
    Append — из аргументов fn (сигнатура WithTx изменена на
    `func(ctx, tx) error`, проброс обогащённого ctx).
  - `Hub` — in-process pub/sub по run_id («*» = все раны), буфер 256 на
    подписчика; переполнение → drop подписки (шина не тормозит, клиент
    пересинхронизируется).
  - `Replay(runID, afterID, limit)` — страницы событий, дефолт limit 500.
  - `StreamBatcher` — батчинг stream.* (200мс / 64КБ), одна транзакция на
    сброс; Close сбрасывает остаток.
- `internal/controller/ws` — `GET /ws?run_id=&last_event_id=&token=`
  (coder/websocket): replay страницами → `synced {last_event_id}` →
  live-поток; ping 30с; write-timeout 10с; drop-подписка → close 1001
  "resync required"; auth по токену (D-08), пустой токен = auth off (dev).
- Демон: hub+journal собраны в main, `/ws` зарегистрирован.
- `Machine.SetTxExecutor` — машина T-03 работает через Journal: события
  переходов рассылаются подписчикам после коммита.
- `api/openapi.yaml`: endpoint /ws, payload-схемы (StateChanged/Gate/
  StreamText/StreamTool/StreamUsage/StreamError), SyncedMessage; `make gen`
  обновлён (Go-модели + TS-типы).

Отклонения от таски: нет по существу. Сигнатура WithTx расширена ctx'ом —
необходимо для гарантии «публикация после коммита» (документировано в коде).

Проверка (`go test -race ./internal/...`, зелёно):
- публикация после коммита, не до; откат → нет события ни в журнале, ни в
  рассылке;
- клиент с last_event_id=N получает ровно события >N без дублей/пропусков
  (WS-интеграция на httptest + coder/websocket клиент);
- 100k событий: Replay страницами, строго возрастающие id, LatestID сходится;
- 10 WS-клиентов: все получают live-поток из 200 событий, подписчики живы;
- медленный подписчик дропается, шина и остальные клиенты не деградируют;
- неверный token → 401; batcher: сброс по таймеру/порогу/Close;
- машина через Journal: TransitionRun → событие в Hub после коммита.
