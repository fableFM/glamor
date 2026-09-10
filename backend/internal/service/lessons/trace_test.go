package lessons_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/service/lessons"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

const verdictChanges = `{
  "verdict": "changes_required",
  "findings": [
    {"id": "REV-001", "severity": "blocking", "file": "x.go", "line": 84,
     "observed": "файлы для очереди", "expected": "sqlite",
     "required_fix": "перевести очередь на sqlite", "forbidden_fix": "не чинить flock-ом"},
    {"id": "REV-002", "severity": "minor", "file": "y.go", "line": 1,
     "observed": "a", "expected": "b", "required_fix": "c", "forbidden_fix": null}
  ],
  "questions": ["точно ли нужен WAL?"]
}`

// Парсинг verdict.json: валидный, битый, отсутствующий.
func TestCollectTraceVerdicts(t *testing.T) {
	runDir := t.TempDir()

	// отсутствующий verdict — не ошибка, вердиктов нет
	trace := lessons.CollectTrace(lessons.TraceInput{RunDir: runDir})
	assert.Empty(t, trace.Verdicts)
	assert.Empty(t, trace.ParseIssues)

	// валидный verdict.json — последняя итерация
	writeFile(t, runDir, "verdict.json", verdictChanges)
	trace = lessons.CollectTrace(lessons.TraceInput{RunDir: runDir})
	require.Len(t, trace.Verdicts, 1)
	assert.Equal(t, 1, trace.Verdicts[0].Iteration)
	assert.Equal(t, "changes_required", trace.Verdicts[0].Verdict.Verdict)
	require.Len(t, trace.Verdicts[0].Verdict.Findings, 2)
	assert.Equal(t, "перевести очередь на sqlite", trace.Verdicts[0].Verdict.Findings[0].RequiredFix)

	// битый verdict — ParseIssue, не ошибка сборки
	writeFile(t, runDir, "verdict.json", "{не json")
	trace = lessons.CollectTrace(lessons.TraceInput{RunDir: runDir})
	assert.Empty(t, trace.Verdicts)
	require.Len(t, trace.ParseIssues, 1)
	assert.Contains(t, trace.ParseIssues[0], "невалидный JSON")
}

// Per-итерационные вердикты (verdict-<n>.json + голый verdict.json) и
// fix-петля: finding, пропавший из следующего вердикта, закрыт.
func TestCollectTraceFixLoop(t *testing.T) {
	runDir := t.TempDir()
	writeFile(t, runDir, "verdict-1.json", verdictChanges)
	writeFile(t, runDir, "verdict-2.json", `{
  "verdict": "changes_required",
  "findings": [
    {"id": "REV-002", "severity": "minor", "file": "y.go", "line": 1,
     "observed": "a", "expected": "b", "required_fix": "c", "forbidden_fix": null}
  ]
}`)
	writeFile(t, runDir, "verdict.json", `{"verdict": "approved", "findings": []}`)

	trace := lessons.CollectTrace(lessons.TraceInput{RunDir: runDir})
	require.Len(t, trace.Verdicts, 3)
	assert.Equal(t, 1, trace.Verdicts[0].Iteration)
	assert.Equal(t, 2, trace.Verdicts[1].Iteration)
	assert.Equal(t, 3, trace.Verdicts[2].Iteration, "голый verdict.json — последняя итерация")

	require.Len(t, trace.FixLoop, 2)
	byID := map[string]lessons.FixLoopEntry{}
	for _, e := range trace.FixLoop {
		byID[e.FindingID] = e
	}
	assert.Equal(t, 2, byID["REV-001"].FixedAt, "blocking закрыт на 2-й итерации")
	assert.Equal(t, 3, byID["REV-002"].FixedAt, "minor закрыт на 3-й итерации")

	// незакрытый finding: FixedAt == 0
	writeFile(t, runDir, "verdict.json", `{
  "verdict": "changes_required",
  "findings": [{"id": "REV-002", "severity": "minor", "file": "y.go", "line": 1,
    "observed": "a", "expected": "b", "required_fix": "c", "forbidden_fix": null}]
}`)
	trace = lessons.CollectTrace(lessons.TraceInput{RunDir: runDir})
	for _, e := range trace.FixLoop {
		if e.FindingID == "REV-002" {
			assert.Equal(t, 0, e.FixedAt, "остался в последнем вердикте — не закрыт")
		}
	}
}

// Выжимка логов упавших этапов: exit code + хвост stage-<key>-<iter>.log.
func TestCollectTraceFailedStages(t *testing.T) {
	runDir := t.TempDir()

	var sb strings.Builder
	for i := range 100 {
		fmt.Fprintf(&sb, "строка лога номер %d\n", i)
	}
	sb.WriteString("FAIL: TestQueue\nexit status 1\n")
	writeFile(t, runDir, "stage-coder-2.log", sb.String())

	exit := int64(1)
	trace := lessons.CollectTrace(lessons.TraceInput{
		RunDir: runDir,
		Stages: []lessons.StageFact{
			{Key: "coder", Iteration: 2, State: "failed", ExitCode: &exit, Error: "process exited with code 1"},
			{Key: "planner", Iteration: 1, State: "succeeded"}, // успешные не попадают
		},
	})

	require.Len(t, trace.FailedStages, 1)
	fs := trace.FailedStages[0]
	assert.Equal(t, "coder", fs.Key)
	assert.Equal(t, int64(2), fs.Iteration)
	assert.Equal(t, int64(1), fs.ExitCode)
	assert.Contains(t, fs.LogExcerpt, "FAIL: TestQueue")
	// хвост: не более TraceLogTailLines строк
	assert.LessOrEqual(t, strings.Count(fs.LogExcerpt, "\n"), lessons.TraceLogTailLines)
}

// DISPUTED-споры фиксера собираются из handoff.md.
func TestCollectTraceDisputes(t *testing.T) {
	runDir := t.TempDir()
	writeFile(t, runDir, "handoff.md",
		"## Fix iteration 1\n- REV-001 исправлен\n- DISPUTED: REV-002 — finding основан на неверном чтении spec\n")

	trace := lessons.CollectTrace(lessons.TraceInput{RunDir: runDir})
	require.Len(t, trace.Disputes, 1)
	assert.Contains(t, trace.Disputes[0], "DISPUTED: REV-002")
}

// run_facts.json пишется атомарно и валиден; PromptText уважает лимит.
func TestRunFactsAndPromptText(t *testing.T) {
	runDir := t.TempDir()
	writeFile(t, runDir, "verdict.json", verdictChanges)

	trace := lessons.CollectTrace(lessons.TraceInput{
		RunID:    "run-1",
		TaskText: "очередь заметок",
		RunDir:   runDir,
		Gates:    []lessons.GateSignal{{Kind: "plan_approval", Question: "Ок?", Answer: "да, но sqlite"}},
		Notes:    []lessons.RunNote{{Kind: "note", Text: "не торопись"}},
		Outcome:  lessons.RunOutcome{State: "succeeded", FinalVerdict: "approved", Iterations: 1, GatesApproved: 2},
	})

	path, err := lessons.WriteRunFacts(runDir, trace)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "run-1", parsed["run_id"])

	text := trace.PromptText(0)
	assert.Contains(t, text, "Исход рана: succeeded")
	assert.Contains(t, text, "REV-001")
	assert.Contains(t, text, "ответ пользователя: да, но sqlite")
	assert.Contains(t, text, "Заметка (note): не торопись")
	assert.Contains(t, text, "не чинить flock-ом")

	// лимит размера
	short := trace.PromptText(200)
	assert.LessOrEqual(t, len(short), 200+len("\n…(трейс усечён, полный — run_facts.json)"))
	assert.Contains(t, short, "усечён")
}
