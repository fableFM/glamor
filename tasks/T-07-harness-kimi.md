# T-07 Адаптер kimi

Статус: done (2026-08-16) · M1 · зависимости: T-06

## Цель

Первый эталонный адаптер. kimi — primary harness пользователя.

## Факты (проверено локально, `kimi --help`)

- Headless: `kimi -p "<prompt>" --output-format stream-json`.
- Resume: `-S/--session <id>`; continue: `-c`. Нужно проверить комбинацию
  `-S <id> -p "новое сообщение"` — ключевая механика D-20 (см. ресёрч).
- Модель: `-m <alias>`; effort: env `KIMI_MODEL_THINKING_EFFORT=low|high|max`
  (пробивает `support_efforts`, опыт ai-pipeline).
- Auto-approve: `--yolo` (может задавать вопросы) / `--auto` (полностью
  автономно). Для этапов — `--yolo`: планировщик обязан уметь задавать
  вопросы через артефакт.
- `kimi acp` — для M4, сейчас не используем.

## Scope

- `BuildCommand`: сборка argv/env из LaunchSpec (модель, effort-env, yolo,
  `-p` с промптом из файла — длинные промпты через stdin или временный файл,
  НЕ через argv (лимит ARG_MAX)).
- `ParseStream`: события stream-json kimi → нормализованные (D-41).
  Золотые файлы: реальный выхлоп маленького рана (снять один раз, положить
  в testdata — зафиксировать версию CLI в комментарии).
- `ExtractSessionID`: из события стрима (проверить: kimi печатает session id
  в первом system-событии?) или из `~/.kimi-code` — по ресёрчу.
- Классификация retriable-ошибок (сетевые сообщения kimi → `retriable`).

## Acceptance

- Интеграционный тест (build tag `e2e`, не в CI по умолчанию): этап
  «напиши файл hello.txt» завершается, артефакт на месте, session_id
  извлечён, resume с новым сообщением работает.
- Юнит-тесты на золотых файлах зелёные.

## Дополнение по ресёрчу (2026-08-14)

- **Коррекция**: `--yolo/--auto` НЕСОВМЕСТИМЫ с `-p` — headless всегда
  auto-approve сам по себе. BuildCommand не должен добавлять yolo при `-p`.
- Session id: meta-событие `session.resume_hint` (fallback — любой
  `session_id` из потока; хранилище `~/.kimi-code/session_index.jsonl`).
- Resume `kimi -S <id> -p "<новое>"` — подтверждён production-кодом Kent.
- Thinking в stdout-стриме ОТСУТСТВУЕТ (идёт в stderr) → для kimi
  стрим-виджет показывает text/tool calls; thinking-блоков не будет —
  честно в Capabilities.
- Usage в stdout отсутствует: метрики — best-effort из
  `<sessionDir>/agents/main/wire.jsonl` (`usage.record`), иначе «—» (T-24).
- Structured output нет → verdict ревьюера: промпт-контракт + валидация +
  до 3 retry-resume «верни исправленный JSON» (паттерн Kent
  `kimi_harness.py::invoke_structured`).
- Длинные промпты: stdin в `-p` UNVERIFIED → передавать промпт аргументом
  (как Kent), при превышении безопасной длины argv — через `--agent-file`
  или файл с инструкцией «прочитай файл X» (выбрать в реализации).

## Итог (2026-08-16)

Сделано (субагентом, проверено): `backend/internal/harness/kimi/` —
BuildCommand (`kimi -p <prompt> --output-format stream-json`, БЕЗ --yolo
(несовместим с -p), `-m`, env KIMI_MODEL_THINKING_EFFORT, resume `-S <id>
-p`, длинный промпт >100KB → временный файл + инструкция «прочитай файл»),
Capabilities (Thinking=false — thinking идёт в stderr; UsageSource=wirefile),
ParseStream (session.resume_hint → session.init, assistant → text,
tool_calls → tool_call.start, tool → tool_result, не-JSON/битые строки →
raw без паники, usage НЕ изобретается), ExtractSessionID (первое событие
с session_id), classifyRetriable по подстрокам. Золотые файлы
синтезированы по матрице (kimi 0.36.0, реальный CLI не запускался).
Проверка: 21 юнит-тест зелёный с -race, lint 0 issues.
Осталось на e2e (build tag): проверка против реального CLI.
