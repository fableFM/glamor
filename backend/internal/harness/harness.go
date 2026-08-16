// Package harness — абстракция AI coding CLI (D-40/41): ядро работает с
// единым интерфейсом, форматы stream-json конкретных CLI заперты в
// адаптерах (internal/harness/<name>). Факты о CLI —
// tasks/research/harness-capability-matrix.md.
package harness

// Capabilities — возможности harness'а (D-43: видны в UI, нет effort —
// ручка disabled).
type Capabilities struct {
	StreamJSON     bool     // NDJSON-стрим событий
	Resume         bool     // resume сессии с новым промптом headless
	EffortLevels   []string // nil = ручка disabled в UI
	CostReporting  bool     // usage/стоимость в стриме
	StructuredJSON bool     // строгий JSON-ответ (для verdict)
	ACP            bool     // запас на M4
	Thinking       bool     // thinking-блоки в stdout-стриме (у kimi — нет)
	// UsageSource — откуда брать метрики токенов: stream | wirefile | export | none.
	UsageSource UsageSource
}

// UsageSource — источник метрик токенов (матрица, «Дополнение» T-06).
type UsageSource string

const (
	UsageSourceStream   UsageSource = "stream"
	UsageSourceWirefile UsageSource = "wirefile"
	UsageSourceExport   UsageSource = "export"
	UsageSourceNone     UsageSource = "none"
)

// ArtifactSpec — контракт артефакта этапа (D-13): источник истины о
// завершении — exit code + наличие/валидация файла.
type ArtifactSpec struct {
	Path     string // относительный путь от WorkDir (например ".glamor/runs/<id>/spec.md")
	Required bool   // без файла этап не считается успешным даже при exit 0
	// JSONSchema — опциональная схема (verdict ревьюера); валидация —
	// промпт-контракт + retry-resume (T-17), здесь только факт наличия.
	JSONSchema string
}

// LaunchSpec — спецификация запуска harness-процесса.
type LaunchSpec struct {
	Prompt    string // собранный промпт этапа
	WorkDir   string // чекаут проекта
	Model     string // алиас или "" = дефолт CLI
	Effort    string
	SessionID string // != "" → resume (D-16)
	// SchemaForStructured — JSON-схема для harness'ов со structured output
	// (qwen --json-schema); передаётся ЗАНОВО на каждый resume (матрица).
	SchemaForStructured string
	ExtraEnv            map[string]string
	ArtifactContract    ArtifactSpec
}

// CommandSpec — собранная команда запуска harness-процесса.
type CommandSpec struct {
	Argv []string
	Env  []string
	// Stdin — данные для stdin процесса (qwen склеивает короткий -p со
	// stdin — паттерн Kent; длинные промпты НЕ через argv, лимит ARG_MAX).
	Stdin string
}

// Harness — адаптер конкретного CLI.
type Harness interface {
	// Name — имя адаптера ("kimi", "qwen", ...).
	Name() string
	// BinaryName — имя бинаря для exec.LookPath (может отличаться от Name).
	BinaryName() string
	Capabilities() Capabilities
	// BuildCommand собирает команду из LaunchSpec. Длинные промпты — НЕ
	// через argv (лимит ARG_MAX): через Stdin или временный файл.
	BuildCommand(spec LaunchSpec) (CommandSpec, error)
	// ParseStream: одна строка stdout → нормализованные события.
	// Нераспознанная/не-JSON строка → событие raw (НЕ ошибка) — D-13 и
	// матрица (kimi доказанно мусорит stdout выводом foreground-Bash).
	// Битая строка (обрезок при краше) не должна паниковать.
	ParseStream(line []byte) []Event
	// ExtractSessionID: session id — из первого подходящего события,
	// а не из фиксированного типа (матрица, следствие «а»).
	ExtractSessionID(events []Event, workDir string) (string, error)
}
