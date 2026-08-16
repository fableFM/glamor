package supervisor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
	"github.com/fableFM/glamor/internal/service/runsapi"
	"github.com/fableFM/glamor/internal/service/supervisor"
)

// E2E D-20/D-21: вопросы как артефакт → гейт question → ответ → resume с
// ответом → чистый spec → plan_approval → approve → succeeded.
func TestQuestionAnswerFlow(t *testing.T) {
	spec := `{"stages":[{"key":"plan","harness":"fake",
		"artifact":{"path":"spec.md","required":true},
		"questions_path":"questions.md","gate_after":"plan_approval"}]}`
	f := newFixture(t, "testdata/plan_dialog.sh", spec, nil)
	ctx := context.Background()

	// попытка 1 завершилась с questions.md → гейт question, ран waiting_gate
	var questionGate *dtorep.Gate
	require.Eventually(t, func() bool {
		gates, err := f.gates.ListGatesByRun(ctx, f.runID)
		if err != nil {
			return false
		}
		for i := range gates {
			if gates[i].Kind == dtorep.GateKindQuestion && gates[i].State == dtorep.GateStateOpen {
				questionGate = &gates[i]
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "question gate must open")

	run, err := f.machineRun()
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateWaitingGate, run.State)

	// ответ → ре-вход этапа с ответом (D-20)
	answer := "используй sqlite"
	_, already, err := f.runsSvc.ResolveGateAPI(ctx, questionGate.ID, runsapi.GateActionAnswer, &answer)
	require.NoError(t, err)
	assert.False(t, already)

	// попытка 2: промпт содержит ответ, вопросов нет → plan_approval
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(f.workDir, "prompt-2.log"))
		return err == nil && strings.Contains(string(data), answer)
	}, 10*time.Second, 50*time.Millisecond, "second attempt prompt must contain the answer")

	var approvalGate *dtorep.Gate
	require.Eventually(t, func() bool {
		gates, err := f.gates.ListGatesByRun(ctx, f.runID)
		if err != nil {
			return false
		}
		for i := range gates {
			if gates[i].Kind == dtorep.GateKindPlanApproval && gates[i].State == dtorep.GateStateOpen {
				approvalGate = &gates[i]
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "plan_approval gate must open")

	// approve → ран двигается к succeeded
	_, _, err = f.runsSvc.ResolveGateAPI(ctx, approvalGate.ID, runsapi.GateActionApprove, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 10*time.Second, 50*time.Millisecond)

	stage := f.latestStage(t)
	assert.Equal(t, int64(2), stage.Iteration)
	assert.Equal(t, int64(0), stage.ResumeCount, "диалог — не auto-resume")
	assert.True(t, f.hasEventKind(t, "gate.opened"))
	assert.True(t, f.hasEventKind(t, "gate.resolved"))
}

// E2E D-22: Interrupt & Steer — процесс прерван, сессия резюмлена с
// сообщением, стрим продолжился новой попыткой (без auto-resume счётчика).
func TestSteerInterruptFlow(t *testing.T) {
	f := newFixture(t, "testdata/steer.sh", specWithArtifact, func(cfg *supervisor.Config) {
		cfg.StallTimeout = 5 * time.Second // steer.sh печатает раз в секунду
	})
	ctx := context.Background()

	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(ctx, f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateRunning
	}, 10*time.Second, 50*time.Millisecond)

	stage := f.latestStage(t)
	message := "хватит, используй sqlite вместо файлов"
	_, err := f.runsSvc.InterruptStageSteer(ctx, stage.ID, message)
	require.NoError(t, err)

	// steer-резюм: новая попытка с сообщением в промпте
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(f.workDir, "prompt-2.log"))
		return err == nil && strings.Contains(string(data), message)
	}, 15*time.Second, 100*time.Millisecond, "steer message must reach the resumed attempt prompt")

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 10*time.Second, 50*time.Millisecond)

	stage = f.latestStage(t)
	assert.Equal(t, dtorep.StageStateSucceeded, stage.State)
	assert.Equal(t, int64(2), stage.Iteration)
	assert.Equal(t, int64(0), stage.ResumeCount, "steer — не auto-resume")
}

// E2E D-22: queue note приходит в промпт ближайшего этапа и consumed.
func TestQueueNote(t *testing.T) {
	f := newFixture(t, "testdata/success.sh", specWithArtifact, nil)
	ctx := context.Background()

	note, err := f.runsSvc.CreateNote(ctx, f.runID, "не забудь про миграции", "note-1")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(f.workDir, "prompt-1.log"))
		return err == nil && strings.Contains(string(data), "не забудь про миграции")
	}, 10*time.Second, 50*time.Millisecond, "queue note must reach the first stage prompt")

	require.Eventually(t, func() bool {
		run, err := f.machineRun()
		return err == nil && run.State == dtorep.RunStateSucceeded
	}, 10*time.Second, 50*time.Millisecond)

	notes, err := f.notes.ListNotesByRun(ctx, f.runID)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.True(t, notes[0].Consumed)
	assert.Equal(t, note.ID, notes[0].ID)
}
