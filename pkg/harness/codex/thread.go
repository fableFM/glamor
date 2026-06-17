package codex

import (
	"context"
	"encoding/json"
	"os"
	"sync"
)

// Turn is a completed Codex turn.
type Turn struct {
	Items         []ThreadItem
	FinalResponse string
	Usage         *Usage
}

// Thread represents a conversation with the Codex agent.
type Thread struct {
	exec          *Exec
	options       Options
	threadOptions ThreadOptions

	mu sync.RWMutex
	id string
}

func newThread(execRunner *Exec, options Options, threadOptions ThreadOptions, id string) *Thread {
	return &Thread{
		exec:          execRunner,
		options:       options,
		threadOptions: threadOptions,
		id:            id,
	}
}

// ID returns the thread ID. It is populated after the first turn starts.
func (t *Thread) ID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.id
}

func (t *Thread) setID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.id = id
}

// Run sends a text prompt to Codex and waits for the completed turn.
func (t *Thread) Run(ctx context.Context, prompt string, options ...TurnOptions) (*Turn, error) {
	return t.RunInput(ctx, Text(prompt), options...)
}

// RunInput sends structured input to Codex and waits for the completed turn.
func (t *Thread) RunInput(ctx context.Context, input Input, options ...TurnOptions) (*Turn, error) {
	events, errorsCh := t.RunInputStreamed(ctx, input, options...)

	turn := &Turn{Items: []ThreadItem{}}
	var turnFailure *TurnFailedError
	var streamError error

	for event := range events {
		switch event.Type {
		case EventItemCompleted:
			if event.Item == nil {
				continue
			}
			turn.Items = append(turn.Items, *event.Item)
			if event.Item.Type == ItemAgentMessage {
				turn.FinalResponse = event.Item.Text
			}
		case EventTurnCompleted:
			turn.Usage = event.Usage
		case EventTurnFailed:
			turnFailure = &TurnFailedError{ThreadError: event.Error}
		case EventError:
			streamError = &ThreadEventError{Message: event.Message, Event: event}
		}
	}

	for err := range errorsCh {
		if err != nil {
			return nil, err
		}
	}
	if streamError != nil {
		return nil, streamError
	}
	if turnFailure != nil {
		return nil, turnFailure
	}

	return turn, nil
}

// RunStreamed sends a text prompt to Codex and streams JSONL events.
func (t *Thread) RunStreamed(
	ctx context.Context,
	prompt string,
	options ...TurnOptions,
) (<-chan ThreadEvent, <-chan error) {
	return t.RunInputStreamed(ctx, Text(prompt), options...)
}

// RunInputStreamed sends structured input to Codex and streams JSONL events.
func (t *Thread) RunInputStreamed(
	ctx context.Context,
	input Input,
	options ...TurnOptions,
) (<-chan ThreadEvent, <-chan error) {
	events := make(chan ThreadEvent)
	errorsCh := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errorsCh)

		turnOptions := firstTurnOptions(options)
		schemaPath, cleanup, err := createOutputSchemaFile(turnOptions.OutputSchema)
		if err != nil {
			errorsCh <- err
			return
		}
		defer cleanup()

		args := ExecArgs{
			Input:                input.Prompt,
			BaseURL:              t.options.BaseURL,
			APIKey:               t.options.APIKey,
			ThreadID:             t.ID(),
			Images:               input.Images,
			Model:                t.threadOptions.Model,
			SandboxMode:          t.threadOptions.SandboxMode,
			WorkingDirectory:     t.threadOptions.WorkingDirectory,
			AdditionalDirs:       t.threadOptions.AdditionalDirs,
			SkipGitRepoCheck:     t.threadOptions.SkipGitRepoCheck,
			OutputSchemaFile:     schemaPath,
			ModelReasoningEffort: t.threadOptions.ModelReasoningEffort,
			NetworkAccessEnabled: t.threadOptions.NetworkAccessEnabled,
			WebSearchMode:        t.threadOptions.WebSearchMode,
			WebSearchEnabled:     t.threadOptions.WebSearchEnabled,
			ApprovalPolicy:       t.threadOptions.ApprovalPolicy,
		}

		err = t.exec.Run(ctx, args, func(line []byte) error {
			event, err := DecodeThreadEvent(line)
			if err != nil {
				return err
			}
			if event.Type == EventThreadStarted {
				t.setID(event.ThreadID)
			}

			select {
			case events <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			errorsCh <- err
		}
	}()

	return events, errorsCh
}

func firstTurnOptions(options []TurnOptions) TurnOptions {
	if len(options) == 0 {
		return TurnOptions{}
	}
	return options[0]
}

func createOutputSchemaFile(schema any) (string, func(), error) {
	if schema == nil {
		return "", func() {}, nil
	}

	file, err := os.CreateTemp("", "codex-output-schema-*.json")
	if err != nil {
		return "", nil, &OutputSchemaError{Op: "create file", Err: err}
	}

	cleanup := func() {
		_ = os.Remove(file.Name())
	}

	if err := json.NewEncoder(file).Encode(schema); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, &OutputSchemaError{Op: "write file", Err: err}
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, &OutputSchemaError{Op: "close file", Err: err}
	}

	return file.Name(), cleanup, nil
}
