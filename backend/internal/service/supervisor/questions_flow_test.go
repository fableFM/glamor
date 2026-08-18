package supervisor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fableFM/glamor/internal/dto/dtorep"
)

// Регрессия живого рана 2026-08-17: этап с questions_path, записавший
// вопросы БЕЗ основного артефакта — это успех (D-20), а не
// «artifact is missing» → failed.
func TestQuestionsWithoutArtifact_Success(t *testing.T) {
	spec := `{"stages":[{"key":"plan","harness":"fake",
		"artifact":{"path":"{run_dir}/spec.md","required":true},
		"questions_path":"{run_dir}/questions.md"}]}`
	f := newFixture(t, "testdata/questions_only.sh", spec, nil)
	ctx := context.Background()

	// этап завершается succeeded, открывается гейт question, ран waiting_gate
	require.Eventually(t, func() bool {
		st, err := f.stages.GetLatestStage(ctx, f.runID, "plan")
		return err == nil && st.State == dtorep.StageStateSucceeded
	}, 10*time.Second, 50*time.Millisecond, "questions-only попытка — succeeded, не failed")

	require.Eventually(t, func() bool {
		gates, err := f.gates.ListOpenGates(ctx, f.runID)
		if err != nil || len(gates) != 1 {
			return false
		}
		return gates[0].Kind == dtorep.GateKindQuestion
	}, 10*time.Second, 50*time.Millisecond, "гейт question открыт")

	// текст вопросов — прямо в гейте (пользователь отвечает в чате, не
	// открывая файл; запрос 2026-08-17)
	gates, err := f.gates.ListOpenGates(ctx, f.runID)
	require.NoError(t, err)
	require.Len(t, gates, 1)
	assert.Contains(t, gates[0].Question, "Какой транспорт?")

	run, err := f.machineRun()
	require.NoError(t, err)
	assert.Equal(t, dtorep.RunStateWaitingGate, run.State)
}
