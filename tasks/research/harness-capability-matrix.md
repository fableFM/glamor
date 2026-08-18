# Capability-матрица AI coding CLI (harness'ов)

> Примечание 2026-08-14: pi (pi-mono) исключён из скоупа glamor — секция 6
> оставлена как архив ресёрча, адаптер не реализуем.

Дата сбора: 2026-08-14. Версии (проверено локально, `--version`):
kimi 0.36.0, qwen 0.21.3 (Qwen Code), claude 2.1.81 (Claude Code),
opencode 1.2.27. codex и pi локально не установлены — данные только
по официальной документации (помечено).

«pi» — это **badlogic/pi-mono** (`@earendil-works/pi-coding-agent`, Mario
Zechner), подтверждено: https://github.com/badlogic/pi-mono (редирект на
earendil-works/pi-mono), https://pi.dev. Другого известного coding-agent «pi»
не найдено; альтернативный кандидат — `pi` из Inflection AI к кодингу
отношения не имеет.

## Сводная таблица

Колонки: 1 headless, 2 stream-json, 3 resume+prompt headless, 4 session id,
5 выживание сессии при SIGINT/SIGTERM, 6 модель/effort, 7 usage в потоке,
8 auto-approve, 9 ACP, 10 structured output.

| # | kimi | qwen | claude | codex | opencode | pi |
|---|------|------|--------|-------|----------|----|
| 1 | `kimi -p "…"` (арг.; stdin — UNVERIFIED) | `qwen -p "…"`, stdin склеивается с `-p` | `claude -p "…"`, stdin до 10 МБ | `codex exec "…"` или `codex exec -` (stdin) | `opencode run "…"` (+ `--prompt`, stdin — UNVERIFIED) | `pi -p "…"`, stdin мержится в промпт |
| 2 | `--output-format stream-json` (NDJSON, OpenAI-подобные сообщения) | `-o stream-json` (+`--include-partial-messages`); `-o json` = буфериз. массив | `--output-format stream-json` (требует `--verbose`; NDJSON) | `codex exec --json` (JSONL) | `opencode run --format json` (JSONL) | `pi --mode json` (JSONL) |
| 3 | ДА: `kimi -S <id> -p "…"` (production в Kent) | ДА: `qwen -r <id> -p "…"` (доки + production в Kent) | ДА: `claude -r <id> -p "…"` (офиц. доки) | ДА: `codex exec resume <id> "…"` / `--last` | ДА: `opencode run -s <id> "…"` (help); UNVERIFIED end-to-end | ЧАСТИЧНО: `pi --session <id> -p "…"` (UNVERIFIED) |
| 4 | meta-событие `session.resume_hint` в потоке + `~/.kimi-code/session_index.jsonl` | `system/init` и `result` несут `session_id`; файлы `~/.qwen/projects/<cwd>/chats/<id>.jsonl` | `system/init.session_id`, `result.session_id`; `~/.claude/projects/<cwd>/<uuid>.jsonl` | `thread.started.thread_id`; rollout-файлы в `~/.codex/sessions/` | `sessionID` в каждом событии; `opencode session list --format json`; SQLite | первая строка `{"type":"session","id":…}`; `~/.pi/agent/sessions/` |
| 5 | UNVERIFIED (wire.jsonl пишется инкрементально → вероятно resume возможен) | SIGINT → exit 130, сессия в JSONL на диске (доки) | SIGTERM → exit 143, abort turn, SessionEnd hooks; сессия на диске инкрементально (проверено локально) | UNVERIFIED (rollout пишется инкрементально) | UNVERIFIED (сессия в БД) | UNVERIFIED (JSONL auto-save) |
| 6 | `-m <alias>`; env `KIMI_MODEL_THINKING_EFFORT=low/medium/high/xhigh/max`, `KIMI_MODEL_THINKING_KEEP` | `-m`, `--fallback-model`; settings `model.reasoningEffort` (high/xhigh и др.) | `--model`, `--effort low/medium/high/max`, `--fallback-model` | `-m/--model`; `-c model_reasoning_effort=…` (minimal…xhigh), config.toml | `-m provider/model`; `--variant <effort>` | `--provider/--model`, `--model id:<thinking>`, `--thinking off/minimal/low/medium/high/xhigh/max` |
| 7 | НЕТ в stdout-потоке; usage в `wire.jsonl` (`usage.record`) на диске | ДА: `result.usage` + `result.stats` (токены, tool calls) | ДА: `result` (usage, `total_cost_usd`, per-model) | ДА: `turn.completed.usage` | ДА: `step_finish` (tokens + cost); известный баг: финальный `step_finish` может не эмититься | ДА: кумулятивный `usage` в `message_update`; итог в сообщениях |
| 8 | `-p` всегда auto; `--yolo`/`--auto` несовместимы с `-p` | `--yolo` / `--approval-mode plan/default/auto-edit/auto/yolo` | `--permission-mode …/bypassPermissions/auto`, `--dangerously-skip-permissions`, `--allowedTools` | `--sandbox read-only/workspace-write/danger-full-access`; `--full-auto` deprecated | `--auto` (auto-approve не-denied); `--dangerously-skip-permissions` отсутствует в 1.2.27 | Встроенной системы пермишенов НЕТ (философия; gates через extensions) |
| 9 | ДА: `kimi acp` (stdio) | ДА: `--experimental-acp`; `qwen serve` — HTTP-демон поверх ACP-child | Нет нативно; адаптер `@agentclientprotocol/claude-agent-acp` | Нет нативно; адаптер `@zed-industries/codex-acp` (aka `@agentclientprotocol/codex-acp`); свой app-server | ДА: `opencode acp` (stdio nd-json) | НЕТ; вместо этого `--mode rpc` (JSONL stdin/stdout) и SDK |
| 10 | НЕТ (нет флага; Kent валидирует финальный JSON + retry-resume) | ДА: `--json-schema '{…}'` / `@file` → `structured_result` | ДА: `--json-schema` → `structured_output` в json | ДА: `--output-schema file` + `-o` | UNVERIFIED (нет флага в help/docs) | НЕТ (нет флага в CLI reference) |

## 1. kimi (Kimi Code CLI 0.36.0)

Источники: `kimi --help` (локально);
https://moonshotai.github.io/kimi-code/en/reference/kimi-command.md ;
…/en/guides/sessions.md ; …/en/configuration/env-vars.md ; production-код
Kent `/Users/user/.kent/scripts/kimi_harness.py`.

**Headless.** `kimi -p "<prompt>" [--output-format stream-json]`.
В `-p` режиме пермишен-политика всегда `auto` (без вопросов к пользователю;
static deny-rules действуют). Флаги `--yolo/--auto/--plan` с `-p`
**несовместимы** (отклоняются на старте) — это подтверждено доками и
аттестацией в Kent (`permission.set_mode == "auto"` в wire.jsonl).
Чтение промпта из stdin в `-p` режиме доками явно не описано — UNVERIFIED.

**stream-json.** NDJSON, одна строка = одно сообщение, OpenAI-подобный формат:
- `{"role":"meta","type":"system.version","version":…}` — версия стрима;
- `{"role":"meta","type":"session.resume_hint","session_id":"session_<uuid>",…}` —
  **ключевое событие: resumable session id** (добавлено в 0.23.4, changelog);
- `{"role":"assistant","content":"…","tool_calls":[{"function":{"name":…,"arguments":…}}]}`;
- tool-сообщения после tool_calls;
- thinking в JSONL **не пишется** (идёт в stderr); tool-progress и нотисы
  «resuming session» — тоже в stderr.
- ВАЖНО (Kent production): в stdout могут вклиниваться **не-JSON строки**
  (зеркалирование вывода foreground Bash) — парсер обязан пропускать
  невалидные строки, а не падать.
- Usage/токены в stdout-потоке **отсутствуют**; Kent читает их из
  `<sessionDir>/agents/main/wire.jsonl` (события `usage.record` с полями
  `inputOther/output/inputCacheRead/inputCacheCreation`).

**Resume с новым промптом.** РАБОТАЕТ: `kimi -S <session_id> -p "<новое>"`
(проверено production-кодом Kent; `--session` несовместим только с
`--agent`/`--agent-file`, т.к. агент привязан при создании сессии).
Fallback: если `session.resume_hint` не эмитнут (ранний выход, сбой
провайдера), Kent берёт любой `session_id` из событий потока.

**Session id / хранилище.** `~/.kimi-code/session_index.jsonl`
(`{"sessionId","sessionDir","workDir"}`), сессии в
`~/.kimi-code/sessions/<workDirKey>/<sessionId>/` (`state.json` +
`agents/main/wire.jsonl`). Проверено локально.

**SIGINT/SIGTERM.** UNVERIFIED. Косвенно: wire.jsonl пишется инкрементально
(проверено локально чтением живых сессий) → resume после убийства вероятен.

**Модель/effort.** `-m <alias>`; env `KIMI_MODEL_THINKING_EFFORT`
(low/medium/high/xhigh/max — по env-vars.md; пользовательский опыт: low/high/max),
`KIMI_MODEL_THINKING_KEEP=all`. Аттестация через wire.jsonl `llm.request`
(`modelAlias`, `thinkingEffort`, `thinkingKeep`).

**Auto-approve.** В headless всегда `auto` (см. выше); интерактивно
`--yolo` / `--auto`.

**ACP.** `kimi acp` — нативный ACP-сервер по stdio (проверено `kimi acp --help`).
Также `kimi web` — REST+WebSocket сервер с OpenAPI (`/openapi.json`).

**Structured output.** Нет `--json-schema`. Паттерн Kent: финальный
assistant-content парсится как JSON (со strip ```-fence), при невалидности —
до 3 retry через resume той же сессии с промптом «верни исправленный JSON».

## 2. qwen (Qwen Code 0.21.3)

Источники: встроенная документация пакета
`/opt/homebrew/opt/qwen-code/.../bundled/qc-helper/docs/features/headless.md`,
`structured-output.md`, `approval-mode.md`, `qwen-serve.md`; `qwen --help`,
`qwen serve --help`, `qwen sessions --help` (локально); production-код Kent
`/Users/user/.kent/scripts/qwen_code_worker.py`;
`~/.kent/qwen-template.md`, `~/.kent/qwen-code/settings.base.json`.

**Headless.** `qwen -p "<prompt>"` или pipe в stdin; `-p` **склеивается со
stdin** («Appended to input on stdin (if any)» — help). Kent реально подаёт
короткий `-p` + полный промпт в stdin.

**stream-json / json.**
- `-o stream-json`: NDJSON; с `--include-partial-messages` добавляются
  `message_start`, `content_block_delta` и т.п.
- `-o json`: один JSON-**массив** всех событий в конце (не NDJSON!).
- Типы: `{"type":"system","subtype":"init"|"session_start", "session_id",
  "model","cwd","permission_mode","qwen_code_version","tools":[…]}`,
  `{"type":"assistant","message":{"content":[{"type":"text"|"tool_use",…}],
  "usage":{…}},"parent_tool_use_id":null}`,
  финальный `{"type":"result","subtype":"success","is_error":false,
  "session_id","result","structured_result","usage","stats","duration_ms",
  "error":{"message"}}`.
- `stats.models.<m>.tokens.total`, `stats.tools.totalCalls/byName` — для
  учёта (пример в headless.md).
- Exit codes: `53` = max-session-turns, `55` = budget exceeded
  (`--max-wall-time`/`--max-tool-calls`), `130` = SIGINT; при 130/53
  result-события в stdout может НЕ быть — источник истины = exit code.

**Resume с новым промптом.** РАБОТАЕТ и battle-tested: Kent делает
`qwen --resume <session_id> … --prompt "…"` (prompt в stdin), включая
salvage-resume для досылки structured output. Доки:
`qwen --resume <id> -p "…"`, `qwen --continue -p "…"`. Сессии project-scoped:
`~/.qwen/projects/<sanitized-cwd>/chats/<sessionId>.jsonl` (проверено локально).
`--json-schema` нужно передавать заново на каждый resume (per-run флаг).

**Session id.** В событиях `system/init` и `result` (`session_id`), плюс
`qwen sessions list`. Kent: `result.session_id or init.session_id`.

**SIGINT.** Exit 130, сессия на диске инкрементально (chat-recording JSONL);
`--chat-recording=false` отключает resume-возможность (help).

**Модель/effort.** `-m`, `--fallback-model` (до 3). Effort — через
settings.json `model.reasoningEffort` (Kent пинит `xhigh`, downgrade до
`high` для fix-циклов); CLI-флага effort нет. Полезные env:
`QWEN_CODE_UNATTENDED_RETRY=1` (бесконечный retry 429/529, heartbeat в
stderr), `QWEN_CODE_SUPPRESS_YOLO_WARNING=1`, `QWEN_HOME`,
`QWEN_CODE_SYSTEM_SETTINGS_PATH`.

**Auto-approve.** `--yolo` / `--approval-mode plan|default|auto-edit|auto|yolo`
(headless.md). YOLO **не включает sandbox** — предупреждение в stderr.

**ACP.** `--experimental-acp` (строки в бандле + упоминания в
structured-output.md: несовместим с `--json-schema`). `qwen serve` —
HTTP-демон (порт 4170, bearer `--token`/`QWEN_SERVER_TOKEN`), бриджующий
ACP-child: `POST /session`, `POST /session/:id/prompt`,
`GET /session/:id/transcript`, SSE-реплей.

**Structured output.** `--json-schema '<json>'` или `@file`: синтетический
терминальный tool `structured_output`, Ajv-валидация, первый валидный вызов
завершает ран; в `json`/`stream-json` результат в `result.structured_result`,
в `text` — чистый JSON в stdout. Ограничения: не в субагентах, не с
`-i`/stream-json-input/ACP; каждая валидационная неудача = полный ход модели.

## 3. claude (Claude Code 2.1.81)

Источники: `claude --help` (локально); живой прогон
`claude -p "…" --output-format stream-json --verbose` в /tmp/harness-probe
(события system/*; финальный result не получен — endpoint отдавал 500,
9× api_retry); https://code.claude.com/docs/en/headless .

**Headless.** `claude -p "<prompt>"` (stdin читается, лимит 10 МБ;
prompt+stdin = инструкция+контекст). Exit 0 успех / non-zero ошибка.
`--bare` рекомендован для CI (без hooks/MCP/CLAUDE.md; антропик-авторизация
только `ANTHROPIC_API_KEY`/apiKeyHelper).

**stream-json.** Требует `--verbose`. NDJSON-события (живой прогон + доки):
- `{"type":"system","subtype":"hook_started"|"hook_response"|"init"|
  "api_retry"|"plugin_install", …}` — init несёт `session_id, cwd, model,
  permissionMode, tools, mcp_servers, claude_code_version, apiKeySource`
  (все поля видны в живом прогоне);
- `api_retry` (живой образец): `attempt, max_retries, retry_delay_ms,
  error_status, error` (категории: rate_limit, overloaded, server_error, …);
- `{"type":"assistant","message":{…content:[text|tool_use|thinking]…},
  "parent_tool_use_id":…}`, `{"type":"user","message":{…tool_result…}}`;
- `--include-partial-messages` → `{"type":"stream_event","event":{…
  "delta":{"type":"text_delta","text":…}}}` (Anthropic SSE-подобные);
- финальная строка `{"type":"result","subtype":"success","is_error",
  "result","session_id","total_cost_usd","usage",…}` (доки; в живом прогоне
  не получен — UNVERIFIED локально, подтверждён доками);
- сабагенты: `parent_tool_use_id` != null; текст/think­ing сабагентов только
  с `--forward-subagent-text` (≥2.1.211).

**Resume с новым промптом.** РАБОТАЕТ (офиц. доки):
`session_id=$(claude -p "…" --output-format json | jq -r .session_id)` затем
`claude -p "<новое>" --resume "$session_id"`. С ≥2.1.223 сессия ищется по id
в любом проекте машины. `--fork-session` при resume создаёт новый id;
`--session-id <uuid>` задаёт id заранее; `--no-session-persistence` отключает
сохранение.

**Session id / хранилище.** `~/.claude/projects/<sanitized-cwd>/<uuid>.jsonl`.
Проверено локально: файл создан инкрементально даже для незавершившегося
(9 retries, убит по таймауту) рана → **сессия переживает прерывание**.

**SIGINT/SIGTERM.** SIGTERM → abort текущего хода, убийство дерева Bash,
SessionEnd hooks, exit 143 (доки). Фоновые Bash-задачи убиваются ~5 сек после
финального результата; ожидание фоновых субагентов capped 10 мин
(`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`).

**Модель/effort.** `--model` (alias или полное имя), `--effort
low|medium|high|max`, `--fallback-model` (только с `-p`).

**Auto-approve.** `--permission-mode acceptEdits|bypassPermissions|default|
dontAsk|plan|auto`, `--dangerously-skip-permissions`, гранулярные
`--allowedTools "Bash(git:*) Edit"` / `--disallowedTools`.

**ACP.** Нативно нет. Адаптер `@agentclientprotocol/claude-agent-acp`
(https://github.com/agentclientprotocol/claude-agent-acp).

**Structured output.** `--json-schema '<inline-json-schema>'` +
`--output-format json` → поле `structured_output` (валидация с диагностикой,
`format` keyword — аннотация). Несовместимостей с resume в доках не описано —
UNVERIFIED, надо ли передавать схему на каждый resume (у qwen — надо).

### Реализация (2026-08-17)

Адаптер `backend/internal/harness/claude` (T-25). Факты уточнены при
написании адаптера, CLI НЕ запускался (юнит-тесты на синтезированных
golden NDJSON):

- Команда: `claude -p <prompt> --output-format stream-json --verbose
  --include-partial-messages --bare --permission-mode bypassPermissions`
  (+`--model`, `--effort`, `-r <id>`, `--json-schema`). `--verbose`
  всегда в паре со stream-json (риск 5); `--include-partial-messages`
  включён — даёт `stream_event`/`content_block_delta` → живой стрим
  `assistant.text_delta`/`thinking_delta`.
- Длинный промпт (>100 КБ): короткий `-p`-stub + полный промпт в stdin
  (stdin до 10 МБ, prompt+stdin = инструкция+контекст).
- `system/api_retry` → нормализованное `error.retry` с Retriable=true —
  «признак жизни» для stall-watchdog'а supervisor'а (T-09); верхний лимит
  retry-лупы — конфиг supervisor'а, не адаптера.
- `--json-schema` передаём заново на каждый resume (UNVERIFIED у claude,
  обязательно у qwen; повторная передача безвредна).
- `structured_output` парсим из stream-json `result` best-effort: в доках
  поле описано для `--output-format json`, наличие в stream-json
  result — UNVERIFIED.
- `result.usage` — Anthropic-поля (`input_tokens/output_tokens/
  cache_read_input_tokens/cache_creation_input_tokens`) +
  `total_cost_usd` верхнего уровня.
- `--permission-mode` — параметр конструктора адаптера (дефолт
  bypassPermissions: этапы авто-одобрены); ACP-нативной поддержки нет
  (Capabilities.ACP=false, адаптер внешний).

## 4. codex (OpenAI Codex CLI) — ТОЛЬКО ДОКИ (не установлен)

Источники: https://developers.openai.com/codex/noninteractive ;
https://raw.githubusercontent.com/openai/codex/main/docs/exec.md ;
https://github.com/openai/codex/issues/11750 , #32061.

**Headless.** `codex exec "<prompt>"` (аргумент), `codex exec -` (весь промпт
из stdin), prompt-arg + stdin = инструкция + контекст. Прогресс в stderr,
финальное сообщение в stdout. `--skip-git-repo-check` вне git-репо.
`--ephemeral` — без записи rollout-файлов. `CODEX_API_KEY` работает только
для `codex exec`.

**JSON.** `codex exec --json` → JSONL. События: `thread.started`
(`thread_id` — это и есть session id для resume), `turn.started`,
`item.started|item.updated|item.completed`, `turn.completed`
(`usage:{input_tokens,cached_input_tokens,output_tokens,
reasoning_output_tokens}`), `turn.failed`, `error`. Item types:
`agent_message, reasoning, command_execution, file_change, mcp_tool_call,
web_search, todo_list`. Пример из доков:
```jsonl
{"type":"thread.started","thread_id":"0199a213-81c0-7800-8aa1-bbab2a035a53"}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"…","aggregated_output":"…","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"…"}}
{"type":"turn.completed","usage":{"input_tokens":24763,"cached_input_tokens":24448,"output_tokens":122}}
```
Нет text-delta стрима: agent_message приходит целиком (`item.completed`).

**Resume с новым промптом.** РАБОТАЕТ: `codex exec resume <SESSION_ID>
"<новое>"` или `codex exec resume --last "…"`. ВАЖНО: сохраняется только
контекст диалога — **все флаги (model, sandbox, json) надо передавать
заново** (exec.md). `codex exec fork` для headless-форка отсутствует —
открытый issue #11750 (2026-02). Resume не восстанавливает model/effort
сессии (issue #32061).

**Session id.** `thread.started.thread_id`; rollout-файлы
`~/.codex/sessions/…` (по wiki/third-party — UNVERIFIED для текущих версий
точная раскладка).

**SIGINT/SIGTERM.** UNVERIFIED (rollout пишется инкрементально — resume после
убийства вероятен, но не подтверждено доками).

**Модель/effort.** `-m/--model`; `-c model_reasoning_effort="high"`
(override config.toml; уровни minimal/low/medium/high/xhigh по
third-party источникам, точный enum для текущей версии UNVERIFIED);
`--profile`. Конфиг `~/.codex/config.toml`.

**Auto-approve.** В exec аппрувов нет вообще; уровень доступа = sandbox:
`--sandbox read-only` (дефолт) | `workspace-write` | `danger-full-access`.
`--full-auto` deprecated (warning). `--ignore-user-config`, `--ignore-rules`
для контролируемого CI.

**ACP.** Нативно нет (на момент проверки). Адаптер
`@zed-industries/codex-acp` / `@agentclientprotocol/codex-acp`. У codex есть
свои `codex mcp` (MCP-сервер) и app-server (JSON-RPC) — UNVERIFIED детали.

**Structured output.** `--output-schema ./schema.json` (strict JSON Schema
по правилам OpenAI Structured Outputs) + `-o/--output-last-message <file>` —
финальный JSON и в stdout, и в файл.

### Реализация (2026-08-17)

Адаптер `backend/internal/harness/codex` (T-25). CLI локально НЕ
установлен — адаптер написан ТОЛЬКО по докам, CLI не запускался
(юнит-тесты на синтезированных golden JSONL из примеров exec.md); e2e
отложен до установки. Всё ниже — UNVERIFIED до живой проверки:

- Команда: `codex exec "<prompt>" --json --sandbox workspace-write
  --skip-git-repo-check` (+`-m <model>`); resume: `codex exec resume <id>
  "<новое>"` — ВСЕ флаги (model/sandbox/json/schema) передаются заново
  (доки: персистится только контекст диалога).
- Длинный промпт (>100 КБ): `codex exec -` + stdin; форма resume+stdin
  (`codex exec resume <id> -`) — UNVERIFIED, используем тот же `-`.
- effort: `-c model_reasoning_effort=<effort>` пробрасываем, но enum
  уровней UNVERIFIED → Capabilities.EffortLevels=nil (ручка disabled
  в UI, D-43) до подтверждения на установленном CLI.
- `--output-schema` принимает ПУТЬ к файлу схемы (не inline JSON, в
  отличие от claude/qwen) → адаптер пишет схему во временный файл;
  `-o` добавлен по докам (финальный JSON при этом остаётся и в stdout).
- Маппинг событий: `thread.started.thread_id` → session.init;
  `item.started command_execution` → tool_call.start; `item.completed`:
  command_execution → tool_result (exit_code, aggregated_output с
  усечением), agent_message → assistant.text ЦЕЛИКОМ (deltas нет),
  reasoning → assistant.thinking; `turn.completed.usage` → usage;
  `turn.failed`/`error` → error с retriable-классификацией.
  `reasoning_output_tokens` отдельно НЕ суммируем в output (по семантике
  OpenAI обычно уже входит в output_tokens — UNVERIFIED).
- Терминального result-события у codex НЕТ: финальный ответ = последний
  agent_message, usage = turn.completed (матрица, «Рекомендации»);
  supervisor опирается на exit code.

## 5. opencode (1.2.27)

Источники: `opencode --help`, `opencode run --help`, `opencode session
--help` (локально); https://opencode.ai/docs/cli/ ;
https://github.com/yankeeinlondon/rusty-biscuit/blob/main/claudine/docs/topics/stream-parsing.md ;
https://takopi.dev/reference/runners/opencode/stream-json-cheatsheet/ ;
issues anomalyco/opencode #26855, #40544.

**Headless.** `opencode run "<message>"` (позиционный массив, склеивается),
`--prompt`, `-f/--file` для аттачей. Может поднять эфемерный локальный
сервер или `--attach http://localhost:4096` к живому `opencode serve`
(экономит MCP cold-boot). Stdin в run — UNVERIFIED.

**JSON.** `opencode run --format json` → NDJSON. Конверт:
`{type, timestamp(epoch ms), sessionID, part? | error?}`.
Типы: `step_start`, `text`, `reasoning`, `tool_use`, `step_finish`.
- `step_start` несёт `sessionID` (`ses_…`) — способ получить id;
- `tool_use` эмитится **только по завершении** (`status=="completed"`) —
  стрима «tool started» нет (takopi.dev);
- `step_finish` несёт `tokens` и `cost` (ключи: cost,id,messageID,reason,
  sessionID,tokens,type — issue #40544);
- `--thinking` добавляет показ thinking-блоков.
ИЗВЕСТНЫЕ БАГИ: финальный `step_finish` может не эмититься (#26855) → учёт
usage надо дублировать через `opencode export <sessionID>`; модель в
событиях отсутствует (#40544).

**Resume с новым промптом.** `opencode run -s <sessionID> "<новое>"` /
`-c` (последняя), `--fork` — форк при продолжении. Флаги есть в help;
end-to-end поведение (порядок сообщений, контекст) UNVERIFIED.
Управление: `opencode session list --format json`, `opencode session delete`.

**Session id / хранилище.** `sessionID` в каждом событии; локально —
SQLite `~/.local/share/opencode/opencode.db` + `storage/`
(проверено локально). `opencode export <sessionID>` → полный JSON сессии.

**SIGINT/SIGTERM.** UNVERIFIED (сессия в БД — вероятно переживает).

**Модель/effort.** `-m provider/model`; `--variant <строка>` —
provider-specific reasoning effort (high, max, minimal…; точные значения
зависят от провайдера). `opencode models` — список.

**Auto-approve.** `--auto` — авто-аппрув всего, что не запрещено
permissions-конфигом (help + docs). `--dangerously-skip-permissions` в
локальной 1.2.27 НЕ найден (ни в help, ни в strings бинаря) — встречается в
issue от сторонних версий; считать ОТСУТСТВУЮЩИМ/UNVERIFIED.

**ACP.** `opencode acp` — нативный ACP по stdio nd-JSON (help + docs).
`opencode serve` — headless HTTP API (порт по умолчанию 4096 в примерах
доков; help: random), basic-auth `OPENCODE_SERVER_PASSWORD`.

**Structured output.** Отдельного флага нет — UNVERIFIED. Прагматично:
промпт-контракт + валидация, либо HTTP API `opencode serve` (возможно,
есть structured options — не проверено).

### Реализация (2026-08-17)

Адаптер `backend/internal/harness/opencode` (T-26). Что уточнено при
написании:

- Команда: `opencode run [-s id] [-m provider/model] [--variant effort]
  "<prompt>" --format json --auto`. `--dangerously-skip-permissions` в
  1.2.27 отсутствует — подтверждено, не используем.
- Длинный промпт (>100KB): stdin в `run` UNVERIFIED → временный файл +
  короткая инструкция «прочитай файл X» (паттерн kimi-адаптера).
- `text` мапится в `assistant.text_delta`, а не `assistant.text`:
  гранулярность частичных text-событий UNVERIFIED, выбран безопасный
  вариант (supervisor склеивает дельты). Если e2e покажет, что text —
  всегда полный блок, сменить маппинг на `assistant.text`.
- `tool_use` (только completed/error, стрима «tool started» нет) → пара
  `tool_call.start` + `tool_result` с одинаковыми TS/CallID (start=end) —
  зафиксированное отступление от семантики tool_call.*. При
  `state.status=="error"` вывод берётся из `state.error`, ToolIsErr=true.
- `step_finish` → только `usage` (tokens: input, output+reasoning
  [reasoning складывается в output — отдельного поля в нормализованном
  Usage нет], cache.read/write; cost → CostUSD). Терминальный `result`
  адаптер НЕ синтезирует: ParseStream построчный/stateless, статус
  завершения — из exit code процесса (supervisor). step-finish без
  tokens/cost → raw (не выдумываем usage).
- TS событий — из `timestamp` конверта (epoch ms); у kimi/qwen его нет и
  берётся время парсинга, здесь CLI время шлёт.
- `sessionID` (`ses_…`) есть в каждом событии конверта — ExtractSessionID
  берёт первое непустое; модель в событиях отсутствует (#40544) —
  Event.Model не заполняется.
- error-конверт: `{type:"error", error:{name, data:{message, …}}}`;
  retriable — по подстрокам сообщения (как у kimi/qwen), поле
  `data.isRetryable` осознанно не используем (единообразие эвристики).
- Транспорт `opencode serve` (headless HTTP API) НЕ реализован: для M1
  достаточно CLI-стрима; serve — кандидат на будущее (экономия MCP
  cold-boot через `--attach`, возможный structured output). Выбор
  транспорта инкапсулирован в адаптере.
- EffortLevels=nil: значения `--variant` provider-specific, фиксированного
  enum нет → ручка disabled в UI (D-43).
- Golden-файлы `testdata/*.ndjson` синтезированы по этой матрице +
  takopi.dev cheatsheet (1.2.27); живой CLI в юнит-тестах не запускается.
  E2E-каркас (`opencode_e2e_test.go`, build tag e2e) — точка проверки
  UNVERIFIED: resume `-s` end-to-end (риск 7) и проявление #26855.

## 6. pi (@earendil-works/pi-coding-agent) — ТОЛЬКО ДОКИ (не установлен)

Источники: https://github.com/badlogic/pi-mono (README
packages/coding-agent), docs/json.md, docs/session-format.md (ссылки из
README). Локально не установлен.

**Headless.** `pi -p "<prompt>"` (print-режим); piped stdin мержится в
начальный промпт (`cat README.md | pi -p "Summarize"`). `@file` — аттачи.

**JSON.** `pi --mode json "<prompt>"` → JSONL всех событий сессии.
Первая строка — заголовок: `{"type":"session","version":3,"id":"<uuid>",
"timestamp":"…","cwd":"…"}` — **session id в первой строке**.
События (docs/json.md, точные типы из исходников):
`agent_start`, `agent_end{messages}`, `turn_start`, `turn_end{message,
toolResults}`, `message_start{message}`, `message_update{usage,
assistantMessageEvent}` (delta-only: `{"type":"text_delta","contentIndex":0,
"delta":"Hello"}`; верхний `usage` — кумулятивный provider-reported, может
быть нулевым до конца), `message_end{message}` (финальное авторитетное),
`tool_execution_start{toolCallId,toolName,args}`,
`tool_execution_update{…,partialResult}`,
`tool_execution_end{toolCallId,toolName,result,isError}`,
`queue_update`, `compaction_start/end`. Пример:
```jsonl
{"type":"session","version":3,"id":"uuid","timestamp":"...","cwd":"/path"}
{"type":"agent_start"}
{"type":"message_update","usage":{...},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}
{"type":"tool_execution_start","toolCallId":"…","toolName":"bash","args":{…}}
{"type":"tool_execution_end","toolCallId":"…","toolName":"bash","result":{…},"isError":false}
{"type":"agent_end","messages":[…]}
```

**Resume с новым промптом.** UNVERIFIED в точной форме. CLI: `pi --session
<path|id>` (принимает и partial UUID), `-c/--continue`, `--fork <path|id>`.
Комбинация `--session <id> -p "<новое>"` синтаксически не запрещена (флаги
из разных групп), но документированного примера нет — проверить
экспериментально. Сессии — JSONL-дерево (id/parentId, in-place branching)
в `~/.pi/agent/sessions/` по cwd; `/export` HTML/JSONL, `/import` JSONL.

**SIGINT/SIGTERM.** UNVERIFIED. «Sessions auto-save» + JSONL append →
вероятно переживает; Escape=abort в TUI, для headless поведение не описано.

**Модель/effort.** `--provider`, `--model <provider/id[:thinking]>`
(`pi --model sonnet:high`), `--thinking
off|minimal|low|medium|high|xhigh|max`, `--api-key`. Env для bash-tool:
`PI_SESSION_ID`, `PI_SESSION_FILE`, `PI_PROVIDER`, `PI_MODEL`,
`PI_REASONING_LEVEL`.

**Auto-approve.** Встроенной permission-системы НЕТ (философия: «No
permission popups»). Ограничение через `--tools read,grep,find,ls`,
`--exclude-tools`, `--no-tools`, `--no-builtin-tools`; confirm-flow — через
extensions. Project trust для project-local ресурсов: `-a/--approve`,
`-na/--no-approve` (в `-p`/`--mode json/rpc` промпта нет — рулит
`defaultProjectTrust`).

**ACP.** НЕТ (и не планируется в core — даже MCP нет). Вместо: `--mode rpc`
(JSONL-протокол stdin/stdout, строгое LF-фреймирование) и SDK
(`createAgentSession`, `session.prompt/steer/followUp`, subscribe на
события) — фактически лучший programmatic-интерфейс из всех шести.

**Structured output.** Нет (в CLI reference флага нет) — UNVERIFIED, можно
ли получить строгий JSON через SDK/промпт-контракт. Для verdict ревьюера
придётся парсить `message_end`/`agent_end`.

## Риски и UNVERIFIED (что проверить экспериментально при реализации)

1. **kimi stdin в `-p`** — читается ли stdin как часть промпта (как у
   qwen/claude/codex/pi) — не описано. Kent всегда передаёт промпт аргументом.
2. **kimi structured output** — нет; нужен retry-протокол поверх resume
   (готовый паттерн в `kimi_harness.py::invoke_structured`).
3. **kimi usage** — не в stdout; или парсить wire.jsonl (хрупкая
   внутренняя схема), или жить без per-run метрик.
4. **qwen `-o json` vs `stream-json`** — это РАЗНЫЕ транспорты (буферный
   массив vs NDJSON); адаптер обязан различать. Exit codes 53/55/130
   обрабатывать отдельно: при 53/130 result-события может не быть.
5. **claude stream-json требует `--verbose`** — без него падает/молчит
   (UNVERIFIED точное поведение в 2.1.81). `--json-schema` при resume —
   передавать заново? (UNVERIFIED). api_retry-события: процесс может жить
   минутами в retry-лупе (наблюдено: 10 попыток с растущим backoff) —
   нужен внешний таймаут.
6. **codex: флаги не персистятся при resume** — model/sandbox/json передавать
   на каждый `codex exec resume`. Точный enum `model_reasoning_effort` и
   раскладка `~/.codex/sessions` — UNVERIFIED (нет локальной установки).
7. **opencode**: финальный `step_finish` может не прийти (#26855) → учёт
   токенов через `opencode export` post-hoc. Нет события tool-start,
   нет модели в событиях. `--dangerously-skip-permissions` отсутствует в
   1.2.27 — использовать `--auto`. Resume+новый промпт end-to-end
   UNVERIFIED.
8. **pi**: всё UNVERIFIED до установки (`npm i -g @earendil-works/
   pi-coding-agent`): resume+`-p`, поведение при сигналах, отсутствие
   structured output. Зато `--mode json` документирован лучше всех, а
   RPC/SDK может заменить resume-механику целиком (long-lived процесс,
   `session.prompt()` много раз).
9. **ACP-покрытие**: нативно kimi/opencode (+qwen experimental). claude/codex
   — только внешние адаптеры, pi — никогда. Если проект выберет ACP как
   единый транспорт — для claude/codex/pi он не сработает.
10. **Версии плавают**: claude меняет семантику minors (у доков примечания
    «Before v2.1.223…»), kimi — события появились в 0.23.4, opencode — баги
    в открытых issues. Адаптеры должны feature-detect'ить (claude уже имеет
    `capabilities` в init ≥2.1.205) и пиновать версии.

## Рекомендации: нормализованная схема событий

Общий enum типов событий (адаптер мапит нативные события в него):

- `session.init` — `{session_id, model, cwd, harness_version,
  permission_mode, tools[]}`. Источники: kimi `meta/system.version`+
  `session.resume_hint`; qwen `system/init`; claude `system/init`;
  codex `thread.started`; opencode первый `step_start`; pi `session`-header.
- `assistant.text` (полный блок) / `assistant.text_delta` (опционально;
  есть у claude+partial, qwen+partial, pi; НЕТ у codex (целиком), kimi
  (целиком в content), opencode (частичные `text`-события — это скорее
  дельты, UNVERIFIED гранулярность)).
- `assistant.thinking` / `assistant.thinking_delta` — claude thinking-блоки,
  qwen thinking (через `enable_thinking`), codex `reasoning` item,
  opencode `reasoning`, pi thinking-дельты по `contentIndex`; kimi — НЕТ в
  stdout (только stderr).
- `tool_call.start` — `{call_id, name, input}`. claude `tool_use`,
  qwen `tool_use`, pi `tool_execution_start`, codex `item.started`,
  kimi assistant-`tool_calls`; opencode — НЕТ (только completed) →
  эмитить start=end по `tool_use`.
- `tool_call.end` / `tool_result` — `{call_id, output, is_error}`.
  claude `tool_result`, qwen tool-сообщения, pi `tool_execution_end`
  (isError), codex `item.completed` (exit_code/aggregated_output),
  opencode `tool_use`(completed), kimi tool-сообщения.
- `usage` — `{input, output, cache_read, cache_write, cost_usd?}`.
  claude `result.usage`+`total_cost_usd`; qwen `result.usage`+`stats`;
  codex `turn.completed.usage`; opencode `step_finish`(tokens+cost,
  может не прийти); pi кумулятивный `usage` в `message_update`;
  kimi — отсутствует (опциональный best-effort из wire.jsonl).
- `result` (терминальный) — `{status: success|error, text,
  structured_output?, session_id, usage}`. claude/qwen — явный `result`;
  codex — последний `agent_message` + `turn.completed` (или
  `--output-schema` + `-o`); pi — `agent_end`; opencode — синтезировать
  (последний `text` + последний `step_finish`); kimi — синтезировать
  (последний assistant-content).
- `error` — claude `result.is_error`/`system/api_retry` (retry —
  отдельный подтип `error.retry`), qwen `result.is_error`+exit codes,
  codex `error`/`turn.failed`, opencode `{error:…}`, pi/kimi — exit code +
  stderr.
- `structured_output` — опциональное поле в `result`: claude
  `structured_output`, qwen `structured_result`, codex (по
  `--output-schema`); остальные — через промпт-контракт + валидацию.

Ключевые следствия для дизайна адаптеров: (а) session id извлекать из
первого подходящего события, а не из фиксированного типа; (б) парсер NDJSON
обязан пропускать не-JSON строки (kimi доказанно мусорит); (в) exit code —
равноправный канал истины (qwen 53/55/130, claude 143); (г) usage — поле
с `null`-семантикой, не гарантировано (kimi, opencode-баг); (д) resume
везде = новый процесс с id + промптом, кроме pi, где разумнее RPC/SDK
long-lived сессия.
