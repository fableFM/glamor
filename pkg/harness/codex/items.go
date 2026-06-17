package codex

import "encoding/json"

const (
	ItemAgentMessage     = "agent_message"
	ItemReasoning        = "reasoning"
	ItemCommandExecution = "command_execution"
	ItemFileChange       = "file_change"
	ItemMCPToolCall      = "mcp_tool_call"
	ItemWebSearch        = "web_search"
	ItemTodoList         = "todo_list"
	ItemError            = "error"
)

// ThreadItem is a flexible representation of known Codex item payloads.
type ThreadItem struct {
	ID               string          `json:"id,omitempty"`
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Command          string          `json:"command,omitempty"`
	AggregatedOutput string          `json:"aggregated_output,omitempty"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Status           string          `json:"status,omitempty"`
	Changes          []FileChange    `json:"changes,omitempty"`
	Server           string          `json:"server,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            *ThreadError    `json:"error,omitempty"`
	Query            string          `json:"query,omitempty"`
	Items            []TodoItem      `json:"items,omitempty"`
	Message          string          `json:"message,omitempty"`
	Raw              json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the raw item payload while decoding known fields.
func (i *ThreadItem) UnmarshalJSON(data []byte) error {
	type threadItem ThreadItem
	var decoded threadItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*i = ThreadItem(decoded)
	i.Raw = append(i.Raw[:0], data...)
	return nil
}

// FileChange describes one file touched by a patch.
type FileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// TodoItem describes one item in Codex's todo list.
type TodoItem struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}
