# go-codex-sdk

Experimental Go wrapper for the Codex CLI JSONL protocol.

This mirrors the current TypeScript Codex SDK shape: it starts `codex exec --experimental-json`,
writes the prompt to stdin, reads JSONL events from stdout, and stores the `thread_id` emitted by
`thread.started`.

## Install

```bash
go get github.com/fable/go-codex-sdk
```

You also need the `codex` CLI installed and authenticated:

```bash
codex login
```

## Quickstart

```go
package main

import (
	"context"
	"fmt"

	codex "github.com/fable/go-codex-sdk"
)

func main() {
	client := codex.MustNew(codex.Options{})
	thread := client.StartThread(codex.ThreadOptions{
		WorkingDirectory: "/path/to/repo",
		SandboxMode:      codex.SandboxModeWorkspaceWrite,
	})

	turn, err := thread.Run(context.Background(), "Diagnose the failing tests and propose a fix")
	if err != nil {
		panic(err)
	}

	fmt.Println("thread:", thread.ID())
	fmt.Println(turn.FinalResponse)
}
```

## Thread examples

### Create a new thread

```go
client := codex.MustNew(codex.Options{})

thread := client.StartThread(codex.ThreadOptions{
	WorkingDirectory: "/path/to/repo",
	SandboxMode:      codex.SandboxModeWorkspaceWrite,
})

turn, err := thread.Run(context.Background(), "Inspect this repository")
if err != nil {
	return err
}

fmt.Println("new thread ID:", thread.ID())
fmt.Println(turn.FinalResponse)
```

### Resume a thread by ID

```go
client := codex.MustNew(codex.Options{})

thread := client.ResumeThread("thread-123", codex.ThreadOptions{
	WorkingDirectory: "/path/to/repo",
	SandboxMode:      codex.SandboxModeWorkspaceWrite,
})

turn, err := thread.Run(context.Background(), "Continue from the previous turn")
if err != nil {
	return err
}

fmt.Println("resumed thread ID:", thread.ID())
fmt.Println(turn.FinalResponse)
```

## Streaming output

`Run` waits for the turn to finish. Use `RunStream` when you want live text output chunks:

```go
chunks, errs := thread.RunStream(context.Background(), "Implement the fix")

for chunk := range chunks {
	fmt.Print(chunk.Text)
}
for err := range errs {
	if err != nil {
		return err
	}
}
```

If you need the raw Codex JSONL events instead of text chunks, use `RunStreamed`:

```go
events, errs := thread.RunStreamed(context.Background(), "Implement the fix")

for event := range events {
	fmt.Println(event.Type)
}
for err := range errs {
	if err != nil {
		panic(err)
	}
}
```

## Error handling

`Run`, `RunInput`, `RunStream`, and `RunInputStream` return typed errors that work with
`errors.Is` and `errors.As`:

```go
turn, err := thread.Run(context.Background(), "Diagnose the failing tests")
if err != nil {
	switch {
	case errors.Is(err, codex.ErrTurnFailed):
		var turnErr *codex.TurnFailedError
		if errors.As(err, &turnErr) {
			return fmt.Errorf("codex turn failed: %s", turnErr.ThreadError)
		}
	case errors.Is(err, codex.ErrThreadEvent):
		var eventErr *codex.ThreadEventError
		if errors.As(err, &eventErr) {
			return fmt.Errorf("codex stream error: %s", eventErr.Message)
		}
	case errors.Is(err, codex.ErrDecodeEvent):
		return fmt.Errorf("invalid codex JSONL event: %w", err)
	case errors.Is(err, codex.ErrExec):
		var execErr *codex.ExecError
		if errors.As(err, &execErr) {
			return fmt.Errorf("codex CLI failed during %s: %w", execErr.Op, err)
		}
	case errors.Is(err, codex.ErrConfig):
		return fmt.Errorf("invalid codex config: %w", err)
	case errors.Is(err, codex.ErrOutputSchema):
		return fmt.Errorf("output schema setup failed: %w", err)
	default:
		return err
	}
}

fmt.Println(turn.FinalResponse)
```

## Notes

- This package depends on the experimental Codex CLI JSONL format.
- The SDK does not bundle a Codex binary; set `Options.CodexPath` or keep `codex` on `PATH`.
- Treat Codex execution like shell execution: use isolated workspaces and conservative sandbox settings.
