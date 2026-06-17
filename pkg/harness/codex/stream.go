package codex

import (
	"context"
	"strings"
)

// RunStreamChunk is a text chunk produced while a turn is running.
type RunStreamChunk struct {
	// Text contains only the newly observed output for the item.
	Text string
	// ItemID is the Codex item ID that produced this chunk.
	ItemID string
	// ItemType is the Codex item type that produced this chunk.
	ItemType string
	// EventType is the JSONL event type that produced this chunk.
	EventType string
	// Event is the original JSONL event for callers that need full metadata.
	Event ThreadEvent
}

// RunStream sends a text prompt to Codex and streams human-readable output chunks.
func (t *Thread) RunStream(
	ctx context.Context,
	prompt string,
	options ...TurnOptions,
) (<-chan RunStreamChunk, <-chan error) {
	return t.RunInputStream(ctx, Text(prompt), options...)
}

// RunInputStream sends structured input to Codex and streams human-readable output chunks.
func (t *Thread) RunInputStream(
	ctx context.Context,
	input Input,
	options ...TurnOptions,
) (<-chan RunStreamChunk, <-chan error) {
	events, eventsErrs := t.RunInputStreamed(ctx, input, options...)
	chunks := make(chan RunStreamChunk)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		seen := map[string]string{}
		var streamErr error

		for event := range events {
			if streamErr != nil {
				continue
			}

			switch event.Type {
			case EventError:
				streamErr = &ThreadEventError{Message: event.Message, Event: event}
				continue
			case EventTurnFailed:
				streamErr = &TurnFailedError{ThreadError: event.Error}
				continue
			}

			chunk, ok := runStreamChunkFromEvent(event, seen)
			if !ok {
				continue
			}

			select {
			case chunks <- chunk:
			case <-ctx.Done():
				streamErr = ctx.Err()
			}
		}

		for err := range eventsErrs {
			if err != nil && streamErr == nil {
				streamErr = err
			}
		}
		if streamErr != nil {
			errs <- streamErr
		}
	}()

	return chunks, errs
}

func runStreamChunkFromEvent(event ThreadEvent, seen map[string]string) (RunStreamChunk, bool) {
	if event.Item == nil {
		return RunStreamChunk{}, false
	}
	if event.Type != EventItemUpdated && event.Type != EventItemCompleted {
		return RunStreamChunk{}, false
	}

	text := streamTextFromItem(*event.Item)
	if text == "" {
		return RunStreamChunk{}, false
	}

	key := event.Item.ID
	if key == "" {
		key = event.Item.Type
	}

	previous := seen[key]
	seen[key] = text

	delta := text
	if strings.HasPrefix(text, previous) {
		delta = strings.TrimPrefix(text, previous)
	}
	if delta == "" {
		return RunStreamChunk{}, false
	}

	return RunStreamChunk{
		Text:      delta,
		ItemID:    event.Item.ID,
		ItemType:  event.Item.Type,
		EventType: event.Type,
		Event:     event,
	}, true
}

func streamTextFromItem(item ThreadItem) string {
	switch item.Type {
	case ItemAgentMessage, ItemReasoning:
		return item.Text
	case ItemCommandExecution:
		return item.AggregatedOutput
	case ItemError:
		if item.Message != "" {
			return item.Message
		}
		if item.Error != nil {
			return item.Error.Message
		}
		return ""
	default:
		if item.Text != "" {
			return item.Text
		}
		return item.AggregatedOutput
	}
}
