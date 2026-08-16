# T-06 Harness Adapter — интерфейс и нормализованные события

Статус: done (2026-08-16) · M1 · зависимости: T-01, research/harness-capability-matrix.md

## Цель

Абстракция harness'а (D-40/41): ядро работает с единым интерфейсом, форматы
конкретных CLI заперты в адаптерах.

## Интерфейс (проект, уточнить по ресёрчу)

```go
type Capabilities struct {
    StreamJSON      bool     // NDJSON-стрим событий
    Resume          bool     // resume сессии с новым промптом headless
    EffortLevels    []string // nil = ручка disabled в UI
    CostReporting   bool     // usage/стоимость в стриме
    StructuredJSON  bool     // строгий JSON-ответ (для verdict)
    ACP             bool     // запас на M4
}

type LaunchSpec struct {
    Prompt      string            // собранный промпт этапа
    WorkDir     string            // чекаут проекта
    Model       string            // алиас или "" = дефолт CLI
    Effort      string
    SessionID   string            // != "" → resume
    ExtraEnv    map[string]string
    ArtifactContract ArtifactSpec // какой файл обязан появиться (D-13)
}

type Harness interface {
    Name() string
    Capabilities() Capabilities
    BuildCommand(LaunchSpec) (argv []string, env []string, err error)
    // ParseStream: NDJSON-строка stdout → нормализованные события.
    // Нераспознанная строка → событие stream.raw (НЕ ошибка) — D-13.
    ParseStream(line []byte) []Event
    // ExtractSessionID: из событий/файлов — как ядро узнаёт session_id.
    ExtractSessionID(events []Event, workDir string) (string, error)
}
```

## Нормализованные события (D-41)

```
type Event struct {
    Kind    string // thinking|text|tool_call|tool_result|usage|error|system|raw
    TS      time.Time
    Text    string          // thinking/text/raw
    Tool    string          // tool_call/tool_result: имя тулзы
    ToolInput  json.RawMessage
    ToolOutput string       // усечённый (например 8КБ), полный — в логе рана
    Usage   *Usage          // tokens in/out, cost?, model
    Err     *StreamError    // error: сетевые/лимиты/прочее — с признаком retriable
}
```

- Классификация ошибок: `retriable` (сеть, rate limit, 5xx провайдера) —
  сигнал для auto-resume (T-09).
- Реестр адаптеров: `internal/harness/registry.go`, конфиг демона
  сопоставляет имя harness'а → путь бинаря (проверка `exec.LookPath` при
  старте, статус в `/healthz`).

## Acceptance

- Юнит-тесты ParseStream на золотых файлах (`testdata/*.ndjson`) для каждого
  реализованного адаптера: корректный парсинг, неизвестные строки → raw,
  битая строка (обрезок при краше) не паникует.
- Интерфейс покрывает kimi и qwen без спецкейсов в ядре (проверка T-07/T-08).

## Дополнение по ресёрчу (2026-08-14, research/harness-capability-matrix.md)

- Нормализованная схема событий берётся из раздела «Рекомендации» матрицы:
  `session.init / assistant.text(_delta) / assistant.thinking(_delta) /
  tool_call.start / tool_call.end|tool_result / usage / result / error /
  structured_output`. Enum в T-06 выше заменить на этот.
- Жёсткие правила парсеров: (а) пропускать не-JSON строки — kimi мусорит
  stdout выводом foreground-Bash (Kent production); (б) session id — из
  первого подходящего события, не из фиксированного типа; (в) exit code —
  равноправный канал истины (qwen 53/55/130, claude 143); (г) `usage` —
  nullable, не гарантирован (kimi — нет в stdout, opencode — баг #26855);
  (д) resume = новый процесс с id+промптом у всех поддерживаемых harness'ов.
- Capabilities дополнить полем `UsageSource: stream|wirefile|export|none`.

## Итог (2026-08-16)

Сделано: `backend/internal/harness/` — интерфейс `Harness` (Name/BinaryName/
Capabilities/BuildCommand→CommandSpec{Argv,Env,Stdin}/ParseStream/
ExtractSessionID), `Capabilities` (+UsageSource stream|wirefile|export|none,
+Thinking), `ArtifactSpec` (D-13), нормализованные события по рекомендациям
матрицы (session.init, assistant.text(_delta), assistant.thinking(_delta),
tool_call.start/end, tool_result, usage, result, error(.retry),
structured_output, raw), `JournalKind` (маппинг в EventKind журнала),
`Registry` (overrides путей из конфига, CheckBinaries через exec.LookPath).
Проверка: kimi/qwen реализованы без спецкейсов в ядре (T-07/T-08),
supervisor (T-09) работает с фейковым адаптером по тому же интерфейсу.
