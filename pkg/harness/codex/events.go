package codex

import (
	"encoding/json"
)

const (
	EventThreadStarted = "thread.started"
	EventTurnStarted   = "turn.started"
	EventTurnCompleted = "turn.completed"
	EventTurnFailed    = "turn.failed"
	EventItemStarted   = "item.started"
	EventItemUpdated   = "item.updated"
	EventItemCompleted = "item.completed"
	EventError         = "error"
)

// Usage describes token usage during a turn.
type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// ThreadError is an error emitted by Codex in the JSONL event stream.
type ThreadError struct {
	Message string `json:"message"`
}

// Error returns the Codex-provided error message.
func (e *ThreadError) Error() string {
	if e == nil || e.Message == "" {
		return "codex thread error"
	}
	return e.Message
}

// ThreadEvent is one top-level JSONL event emitted by codex exec.
type ThreadEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
	Error    *ThreadError    `json:"error,omitempty"`
	Message  string          `json:"message,omitempty"`
	Item     *ThreadItem     `json:"item,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// DecodeThreadEvent decodes a single JSONL line from codex exec.
func DecodeThreadEvent(line []byte) (ThreadEvent, error) {
	var event ThreadEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return ThreadEvent{}, &DecodeEventError{
			Line: append([]byte(nil), line...),
			Err:  err,
		}
	}
	event.Raw = append(event.Raw[:0], line...)
	return event, nil
}
