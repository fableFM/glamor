package codex

// ConfigValue is a value accepted by Codex CLI --config overrides.
type ConfigValue interface{}

// ConfigObject is flattened into repeated --config dotted.path=value flags.
type ConfigObject map[string]ConfigValue

// Options configure the Codex SDK client.
type Options struct {
	// CodexPath overrides the codex executable path. When empty, the SDK uses PATH.
	CodexPath string
	// BaseURL is passed as --config openai_base_url=...
	BaseURL string
	// APIKey is passed to the child process as CODEX_API_KEY.
	APIKey string
	// Env replaces the inherited process environment when non-nil.
	Env map[string]string
	// Config contains additional Codex CLI config overrides.
	Config ConfigObject
}

// ApprovalMode controls when Codex asks for approval before actions.
type ApprovalMode string

const (
	ApprovalModeNever     ApprovalMode = "never"
	ApprovalModeOnRequest ApprovalMode = "on-request"
	ApprovalModeOnFailure ApprovalMode = "on-failure"
	ApprovalModeUntrusted ApprovalMode = "untrusted"
)

// SandboxMode controls filesystem and command sandboxing.
type SandboxMode string

const (
	SandboxModeReadOnly         SandboxMode = "read-only"
	SandboxModeWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxModeDangerFullAccess SandboxMode = "danger-full-access"
)

// ModelReasoningEffort configures model reasoning effort.
type ModelReasoningEffort string

const (
	ModelReasoningEffortMinimal ModelReasoningEffort = "minimal"
	ModelReasoningEffortLow     ModelReasoningEffort = "low"
	ModelReasoningEffortMedium  ModelReasoningEffort = "medium"
	ModelReasoningEffortHigh    ModelReasoningEffort = "high"
	ModelReasoningEffortXHigh   ModelReasoningEffort = "xhigh"
)

// WebSearchMode configures Codex web search.
type WebSearchMode string

const (
	WebSearchModeDisabled WebSearchMode = "disabled"
	WebSearchModeCached   WebSearchMode = "cached"
	WebSearchModeLive     WebSearchMode = "live"
)

// ThreadOptions configure a Codex thread.
type ThreadOptions struct {
	Model                string
	SandboxMode          SandboxMode
	WorkingDirectory     string
	SkipGitRepoCheck     bool
	ModelReasoningEffort ModelReasoningEffort
	NetworkAccessEnabled *bool
	WebSearchMode        WebSearchMode
	WebSearchEnabled     *bool
	ApprovalPolicy       ApprovalMode
	AdditionalDirs       []string
}

// TurnOptions configure a single turn.
type TurnOptions struct {
	// OutputSchema is marshaled to a temporary JSON file and passed via --output-schema.
	OutputSchema any
}
