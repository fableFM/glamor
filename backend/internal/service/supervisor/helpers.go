package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fableFM/glamor/internal/events"
	"github.com/fableFM/glamor/internal/harness"
)

// DefaultPromptBuilder — простой промпт по умолчанию (полные промпты
// дефолтного пайплайна — T-17).
func DefaultPromptBuilder(_ context.Context, lc LaunchContext) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Задача: %s\n\nЭтап пайплайна: %s.\n", lc.Run.TaskText, lc.Stage.StageKey)
	if lc.StageSpec.Artifact != nil && lc.StageSpec.Artifact.Required {
		fmt.Fprintf(&b, "Обязательный артефакт этапа: файл %s — без него этап не засчитывается.\n",
			lc.StageSpec.Artifact.Path)
	}
	if lc.IsResume {
		fmt.Fprintf(&b, "\nТы был прерван (попытка %d). Продолжи с места прерывания и заверши этап.\n",
			lc.Stage.ResumeCount+1)
	}
	for _, msg := range lc.SteerMessages {
		fmt.Fprintf(&b, "\nСообщение от пользователя: %s\n", msg)
	}
	return b.String(), nil
}

// streamPayload — payload события журнала из нормализованного события
// harness'а (само нормализованное имя — в поле normalized).
func streamPayload(ev harness.Event) string {
	payload := map[string]any{"normalized": ev.Kind}
	if ev.Text != "" {
		payload["text"] = ev.Text
	}
	if ev.Tool != "" {
		payload["tool"] = ev.Tool
	}
	if ev.CallID != "" {
		payload["call_id"] = ev.CallID
	}
	if len(ev.ToolInput) > 0 {
		payload["input"] = ev.ToolInput
	}
	if ev.ToolOutput != "" {
		payload["output"] = ev.ToolOutput
	}
	if ev.ToolIsErr {
		payload["is_error"] = true
	}
	if ev.Usage != nil {
		payload["tokens_in"] = ev.Usage.Input
		payload["tokens_out"] = ev.Usage.Output
		if ev.Usage.CostUSD != nil {
			payload["cost_usd"] = *ev.Usage.CostUSD
		}
	}
	if ev.Err != nil {
		payload["message"] = ev.Err.Message
		payload["retriable"] = ev.Err.Retriable
	}
	if ev.SessionID != "" {
		payload["session_id"] = ev.SessionID
	}
	if ev.Model != "" {
		payload["model"] = ev.Model
	}
	if ev.Result != nil {
		payload["status"] = ev.Result.Status
		if len(ev.Result.StructuredOutput) > 0 {
			payload["structured_output"] = ev.Result.StructuredOutput
		}
	}
	return marshalEventPayload(payload)
}

// stageBatcher — StreamBatcher с явным интерфейсом (для подмены в тестах).
func newStageBatcher(journal *events.Journal, runID string, stageID *int64) batchWriter {
	return &journalBatcher{inner: events.NewStreamBatcher(journal, runID, stageID)}
}

type journalBatcher struct {
	inner *events.StreamBatcher
}

func (b *journalBatcher) Append(kind string, payload []byte) { b.inner.Append(kind, payload) }
func (b *journalBatcher) Flush(ctx context.Context) error    { return b.inner.Flush(ctx) }
func (b *journalBatcher) Close(ctx context.Context) error    { return b.inner.Close(ctx) }

// --- файловые хелперы ---------------------------------------------------------

type fileInfo struct{ size int64 }

func (f fileInfo) Size() int64 { return f.size }

func statFile(path string) (fileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileInfo{}, err
	}
	return fileInfo{size: info.Size()}, nil
}

func joinPath(dir, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(dir, rel)
}
